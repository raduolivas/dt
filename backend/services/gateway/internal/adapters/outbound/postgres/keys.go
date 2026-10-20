// Package postgres implements the gateway key read model on PostgreSQL with
// sqlc-generated queries (package db). Reads go through the pool; writes run
// in the transaction the event consumer hands them, so they commit together
// with the event's inbox row.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/raduolivas/dt/backend/services/gateway/internal/adapters/outbound/postgres/db"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

// Store is the api_keys read model.
type Store struct {
	q *db.Queries
}

var _ app.KeyStore = (*Store)(nil)

// New returns a Store whose reads use pool.
func New(pool *pgxpool.Pool) *Store { return &Store{q: db.New(pool)} }

// FindByLookup returns the key with lookup, or app.ErrNotFound.
func (s *Store) FindByLookup(ctx context.Context, lookup string) (domain.KeyRecord, error) {
	row, err := s.q.GetKeyByLookup(ctx, lookup)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.KeyRecord{}, app.ErrNotFound
	}
	if err != nil {
		return domain.KeyRecord{}, fmt.Errorf("get api key by lookup: %w", err)
	}
	k := domain.KeyRecord{
		ID: row.ID, TenantID: row.TenantID, Lookup: row.Lookup, Env: domain.Environment(row.Environment),
		Hash: row.Hash, ExpiresAt: utc(row.ExpiresAt), RevokedAt: utc(row.RevokedAt),
	}
	if len(row.Scopes) > 0 {
		k.Scopes = make([]domain.Scope, len(row.Scopes))
		for i, sc := range row.Scopes {
			k.Scopes[i] = domain.Scope(sc)
		}
	}
	if len(row.AllowedIps) > 0 {
		k.AllowedIPs = row.AllowedIps
	}
	return k, nil
}

// InsertKey adds k in tx; a key with the same id already present is left as is.
func (s *Store) InsertKey(ctx context.Context, tx pgx.Tx, k domain.KeyRecord) error {
	scopes := make([]string, len(k.Scopes))
	for i, sc := range k.Scopes {
		scopes[i] = string(sc)
	}
	ips := k.AllowedIPs
	if ips == nil {
		ips = []netip.Prefix{}
	}
	err := s.q.WithTx(tx).InsertKey(ctx, db.InsertKeyParams{
		ID: k.ID, TenantID: k.TenantID, Environment: string(k.Env), Lookup: k.Lookup, Hash: k.Hash,
		Scopes: scopes, AllowedIps: ips, ExpiresAt: k.ExpiresAt, RevokedAt: k.RevokedAt,
	})
	if err != nil {
		return fmt.Errorf("insert api key: %w", err)
	}
	return nil
}

// SetExpiry sets the key's expiry in tx. An unknown id is a no-op.
func (s *Store) SetExpiry(ctx context.Context, tx pgx.Tx, id string, at time.Time) error {
	if _, err := s.q.WithTx(tx).SetKeyExpiry(ctx, db.SetKeyExpiryParams{ID: id, ExpiresAt: &at}); err != nil {
		return fmt.Errorf("set api key expiry: %w", err)
	}
	return nil
}

// Revoke marks the key revoked at at in tx; the first revocation time wins.
// An unknown id is a no-op.
func (s *Store) Revoke(ctx context.Context, tx pgx.Tx, id string, at time.Time) error {
	if _, err := s.q.WithTx(tx).RevokeKey(ctx, db.RevokeKeyParams{ID: id, RevokedAt: &at}); err != nil {
		return fmt.Errorf("revoke api key: %w", err)
	}
	return nil
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}
