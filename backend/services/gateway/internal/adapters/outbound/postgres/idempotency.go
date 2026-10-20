package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/raduolivas/dt/backend/services/gateway/internal/adapters/outbound/postgres/db"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
)

// IdempotencyStore keeps Idempotency-Key records in idempotency_records.
type IdempotencyStore struct {
	pool *pgxpool.Pool
	q    *db.Queries
}

var (
	_ app.IdempotencyStore          = (*IdempotencyStore)(nil)
	_ app.ExpiredIdempotencyDeleter = (*IdempotencyStore)(nil)
)

// NewIdempotencyStore returns an IdempotencyStore over pool.
func NewIdempotencyStore(pool *pgxpool.Pool) *IdempotencyStore {
	return &IdempotencyStore{pool: pool, q: db.New(pool)}
}

// beginAttempts bounds the insert/lock loop: a conflicting row can vanish
// (abandoned or swept) between the INSERT and the SELECT FOR UPDATE, and then
// the INSERT is tried again.
const beginAttempts = 3

// Begin decides the request's outcome in one transaction: INSERT … ON
// CONFLICT DO NOTHING RETURNING claims a new key; otherwise the existing row
// is locked with SELECT … FOR UPDATE, so concurrent duplicates and lock
// takeovers are serialised on it.
func (s *IdempotencyStore) Begin(ctx context.Context, sc app.IdempotencyScope, hash []byte, now time.Time) (app.IdempotencyOutcome, error) {
	// timestamptz keeps microseconds; the lease must round-trip exactly.
	now = now.UTC().Truncate(time.Microsecond)
	lease := now.Add(app.IdempotencyLockTTL)
	expires := now.Add(app.IdempotencyTTL)
	var out app.IdempotencyOutcome
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		for range beginAttempts {
			_, err := q.InsertIdempotencyRecord(ctx, db.InsertIdempotencyRecordParams{
				TenantID: sc.TenantID, Environment: string(sc.Env), IdemKey: sc.Key, RequestHash: hash,
				LockedUntil: &lease, CreatedAt: now, ExpiresAt: expires,
			})
			if err == nil {
				out = app.IdempotencyOutcome{Kind: app.OutcomeStarted, Lease: lease}
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("insert idempotency record: %w", err)
			}
			row, err := q.LockIdempotencyRecord(ctx, db.LockIdempotencyRecordParams{
				TenantID: sc.TenantID, Environment: string(sc.Env), IdemKey: sc.Key,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				continue // deleted since the INSERT; claim it again
			}
			if err != nil {
				return fmt.Errorf("lock idempotency record: %w", err)
			}
			kind, err := decide(row, hash, now)
			if err != nil {
				return err
			}
			switch kind {
			case app.OutcomeStarted:
				if err := q.ReclaimIdempotencyRecord(ctx, db.ReclaimIdempotencyRecordParams{
					TenantID: sc.TenantID, Environment: string(sc.Env), IdemKey: sc.Key, RequestHash: hash,
					LockedUntil: &lease, CreatedAt: now, ExpiresAt: expires,
				}); err != nil {
					return fmt.Errorf("reclaim idempotency record: %w", err)
				}
				out = app.IdempotencyOutcome{Kind: kind, Lease: lease}
			case app.OutcomeReplay:
				stored, err := storedFrom(row)
				if err != nil {
					return err
				}
				out = app.IdempotencyOutcome{Kind: kind, Stored: stored}
			default:
				out = app.IdempotencyOutcome{Kind: kind}
			}
			return nil
		}
		return errors.New("idempotency record kept vanishing")
	})
	if err != nil {
		return app.IdempotencyOutcome{}, fmt.Errorf("begin idempotency: %w", err)
	}
	return out, nil
}

// decide classifies a locked existing row for a request with hash at now.
func decide(row db.LockIdempotencyRecordRow, hash []byte, now time.Time) (app.OutcomeKind, error) {
	if !row.ExpiresAt.After(now) {
		return app.OutcomeStarted, nil // expired, not yet swept: the key is free
	}
	switch row.State {
	case "in_progress":
		if row.LockedUntil == nil || !row.LockedUntil.After(now) {
			// The owner crashed or overran its lease. It never produced a
			// response, so the key is free, whatever the new body.
			return app.OutcomeStarted, nil
		}
		if !bytes.Equal(row.RequestHash, hash) {
			return app.OutcomeMismatch, nil
		}
		return app.OutcomeInProgress, nil
	case "completed":
		if !bytes.Equal(row.RequestHash, hash) {
			return app.OutcomeMismatch, nil
		}
		return app.OutcomeReplay, nil
	}
	return 0, fmt.Errorf("idempotency record in unknown state %q", row.State)
}

func storedFrom(row db.LockIdempotencyRecordRow) (app.StoredResponse, error) {
	if !row.ResponseStatus.Valid {
		return app.StoredResponse{}, errors.New("completed idempotency record without a status")
	}
	st := app.StoredResponse{Status: int(row.ResponseStatus.Int32)}
	if len(row.ResponseHeaders) > 0 {
		if err := json.Unmarshal(row.ResponseHeaders, &st.Header); err != nil {
			return app.StoredResponse{}, fmt.Errorf("decode stored headers: %w", err)
		}
	}
	if len(st.Header) == 0 {
		st.Header = nil
	}
	if len(row.ResponseBody) > 0 {
		st.Body = row.ResponseBody
	}
	return st, nil
}

// Complete stores resp on the record lease owns.
func (s *IdempotencyStore) Complete(ctx context.Context, sc app.IdempotencyScope, lease time.Time, resp app.StoredResponse) error {
	header := resp.Header
	if header == nil {
		header = map[string][]string{}
	}
	h, err := json.Marshal(header)
	if err != nil {
		return fmt.Errorf("encode stored headers: %w", err)
	}
	body := resp.Body
	if body == nil {
		body = []byte{}
	}
	lease = lease.UTC()
	n, err := s.q.CompleteIdempotencyRecord(ctx, db.CompleteIdempotencyRecordParams{
		ResponseStatus:  pgtype.Int4{Int32: int32(resp.Status), Valid: true}, //nolint:gosec // HTTP status, 100–599
		ResponseHeaders: h, ResponseBody: body,
		TenantID: sc.TenantID, Environment: string(sc.Env), IdemKey: sc.Key, Lease: &lease,
	})
	if err != nil {
		return fmt.Errorf("complete idempotency record: %w", err)
	}
	if n == 0 {
		return app.ErrIdempotencyLeaseLost
	}
	return nil
}

// Abandon deletes the record lease owns.
func (s *IdempotencyStore) Abandon(ctx context.Context, sc app.IdempotencyScope, lease time.Time) error {
	lease = lease.UTC()
	n, err := s.q.AbandonIdempotencyRecord(ctx, db.AbandonIdempotencyRecordParams{
		TenantID: sc.TenantID, Environment: string(sc.Env), IdemKey: sc.Key, Lease: &lease,
	})
	if err != nil {
		return fmt.Errorf("abandon idempotency record: %w", err)
	}
	if n == 0 {
		return app.ErrIdempotencyLeaseLost
	}
	return nil
}

// DeleteExpired deletes up to limit records whose expires_at is before before.
func (s *IdempotencyStore) DeleteExpired(ctx context.Context, before time.Time, limit int) (int64, error) {
	n, err := s.q.DeleteExpiredIdempotencyRecords(ctx, db.DeleteExpiredIdempotencyRecordsParams{
		Before: before, Batch: int32(min(limit, 1<<30)), //nolint:gosec // bounded above
	})
	if err != nil {
		return 0, fmt.Errorf("delete expired idempotency records: %w", err)
	}
	return n, nil
}
