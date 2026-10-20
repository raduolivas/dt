package postgres

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/raduolivas/dt/backend/pkg/ids"
	"github.com/raduolivas/dt/backend/services/gateway/internal/adapters/outbound/postgres/db"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
)

// AuditStore appends to audit_log. The migration revokes UPDATE on the table
// from the service role, so entries can be inserted, read and deleted (for
// retention) but never changed.
type AuditStore struct {
	q *db.Queries
}

var (
	_ app.AuditStore   = (*AuditStore)(nil)
	_ app.AuditDeleter = (*AuditStore)(nil)
)

// NewAuditStore returns an AuditStore over pool.
func NewAuditStore(pool *pgxpool.Pool) *AuditStore { return &AuditStore{q: db.New(pool)} }

// Record inserts e under a new aud_ id. Empty Target and UserAgent and an
// invalid IP are stored as NULL.
func (s *AuditStore) Record(ctx context.Context, e app.AuditEntry) error {
	var ip *netip.Addr
	if e.IP.IsValid() {
		a := e.IP
		ip = &a
	}
	err := s.q.InsertAuditEntry(ctx, db.InsertAuditEntryParams{
		ID: ids.New(ids.Audit), TenantID: e.TenantID, Environment: string(e.Env),
		ActorType: e.ActorType, ActorID: e.ActorID, Action: e.Action, Target: optText(e.Target),
		Status: int32(e.Status), //nolint:gosec // an HTTP status always fits
		Ip:     ip, UserAgent: optText(e.UserAgent), RequestID: e.RequestID, OccurredAt: e.At,
	})
	if err != nil {
		return fmt.Errorf("insert audit entry: %w", err)
	}
	return nil
}

// DeleteAuditBefore deletes up to limit entries that occurred before before,
// oldest first.
func (s *AuditStore) DeleteAuditBefore(ctx context.Context, before time.Time, limit int) (int64, error) {
	n, err := s.q.DeleteAuditEntriesBefore(ctx, db.DeleteAuditEntriesBeforeParams{
		Before: before,
		Batch:  int32(min(limit, 1<<30)), //nolint:gosec // clamped above
	})
	if err != nil {
		return 0, fmt.Errorf("delete audit entries: %w", err)
	}
	return n, nil
}

func optText(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }
