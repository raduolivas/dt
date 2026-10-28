package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/raduolivas/dt/backend/services/gateway/internal/adapters/outbound/postgres/db"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

// TrackingLinkStore keeps tracking links in tracking_links and the link
// expiry of final deliveries in tracking_link_expiry. Replace and Expire of
// one delivery are serialized with a transaction-scoped advisory lock, so a
// link rotated while the delivery becomes final always ends up with the
// expiry.
type TrackingLinkStore struct {
	pool *pgxpool.Pool
	q    *db.Queries
}

var _ app.LinkStore = (*TrackingLinkStore)(nil)

// NewTrackingLinkStore returns a TrackingLinkStore over pool.
func NewTrackingLinkStore(pool *pgxpool.Pool) *TrackingLinkStore {
	return &TrackingLinkStore{pool: pool, q: db.New(pool)}
}

// Replace implements app.LinkStore.
func (s *TrackingLinkStore) Replace(ctx context.Context, l domain.TrackingLink, tokenHash []byte) (domain.TrackingLink, error) {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if err := q.LockDeliveryLinks(ctx, l.DeliveryID); err != nil {
			return fmt.Errorf("lock delivery links: %w", err)
		}
		exp, err := q.GetLinkExpiry(ctx, l.DeliveryID)
		switch {
		case err == nil:
			l.ExpiresAt = utc(&exp)
		case errors.Is(err, pgx.ErrNoRows):
		default:
			return fmt.Errorf("get link expiry: %w", err)
		}
		if err := q.RevokeUnrevokedLinks(ctx, db.RevokeUnrevokedLinksParams{DeliveryID: l.DeliveryID, RevokedAt: &l.CreatedAt}); err != nil {
			return fmt.Errorf("revoke links: %w", err)
		}
		if err := q.InsertTrackingLink(ctx, db.InsertTrackingLinkParams{ID: l.ID, TenantID: l.TenantID,
			Environment: string(l.Env), DeliveryID: l.DeliveryID, TokenHash: tokenHash, CreatedAt: l.CreatedAt,
			ExpiresAt: l.ExpiresAt}); err != nil {
			return fmt.Errorf("insert link: %w", err)
		}
		return nil
	})
	if err != nil {
		return domain.TrackingLink{}, err
	}
	return l, nil
}

// FindByHash implements app.LinkStore.
func (s *TrackingLinkStore) FindByHash(ctx context.Context, tokenHash []byte) (domain.TrackingLink, error) {
	row, err := s.q.GetTrackingLinkByHash(ctx, tokenHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TrackingLink{}, app.ErrNotFound
	}
	if err != nil {
		return domain.TrackingLink{}, fmt.Errorf("get tracking link by hash: %w", err)
	}
	return toLink(db.GetTrackingLinkRow(row)), nil
}

// Get implements app.LinkStore.
func (s *TrackingLinkStore) Get(ctx context.Context, id string) (domain.TrackingLink, error) {
	row, err := s.q.GetTrackingLink(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TrackingLink{}, app.ErrNotFound
	}
	if err != nil {
		return domain.TrackingLink{}, fmt.Errorf("get tracking link: %w", err)
	}
	return toLink(row), nil
}

// Expire implements app.LinkStore.
func (s *TrackingLinkStore) Expire(ctx context.Context, deliveryID string, expiresAt time.Time) (time.Time, []string, error) {
	var ids []string
	var exp time.Time
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if err := q.LockDeliveryLinks(ctx, deliveryID); err != nil {
			return fmt.Errorf("lock delivery links: %w", err)
		}
		if err := q.InsertLinkExpiry(ctx, db.InsertLinkExpiryParams{DeliveryID: deliveryID, ExpiresAt: expiresAt}); err != nil {
			return fmt.Errorf("insert link expiry: %w", err)
		}
		var err error
		exp, err = q.GetLinkExpiry(ctx, deliveryID) // the first recorded wins
		if err != nil {
			return fmt.Errorf("get link expiry: %w", err)
		}
		ids, err = q.ExpireUnrevokedLinks(ctx, db.ExpireUnrevokedLinksParams{DeliveryID: deliveryID, ExpiresAt: &exp})
		if err != nil {
			return fmt.Errorf("expire links: %w", err)
		}
		return nil
	})
	if err != nil {
		return time.Time{}, nil, err
	}
	return exp.UTC(), ids, nil
}

// RevokedSince implements app.LinkStore.
func (s *TrackingLinkStore) RevokedSince(ctx context.Context, deliveryID string, since time.Time) ([]string, error) {
	ids, err := s.q.LinksRevokedSince(ctx, db.LinksRevokedSinceParams{DeliveryID: deliveryID, Since: &since})
	if err != nil {
		return nil, fmt.Errorf("list revoked links: %w", err)
	}
	return ids, nil
}

// DeleteExpired implements app.LinkStore, in one transaction.
func (s *TrackingLinkStore) DeleteExpired(ctx context.Context, revokedBefore, expiresBefore time.Time) (int64, error) {
	var n int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		var err error
		if n, err = q.DeleteExpiredLinks(ctx, db.DeleteExpiredLinksParams{RevokedBefore: revokedBefore, ExpiresBefore: expiresBefore}); err != nil {
			return fmt.Errorf("delete expired links: %w", err)
		}
		if err := q.DeleteLinkExpiriesBefore(ctx, expiresBefore); err != nil {
			return fmt.Errorf("delete link expiries: %w", err)
		}
		return nil
	})
	return n, err
}

func toLink(r db.GetTrackingLinkRow) domain.TrackingLink {
	return domain.TrackingLink{ID: r.ID, TenantID: r.TenantID, Env: domain.Environment(r.Environment),
		DeliveryID: r.DeliveryID, CreatedAt: r.CreatedAt.UTC(), RevokedAt: utc(r.RevokedAt), ExpiresAt: utc(r.ExpiresAt)}
}
