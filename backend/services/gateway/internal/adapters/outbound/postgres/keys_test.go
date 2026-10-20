package postgres_test

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/raduolivas/dt/backend/pkg/migratex"
	"github.com/raduolivas/dt/backend/pkg/testkit"
	"github.com/raduolivas/dt/backend/services/gateway/internal/adapters/outbound/postgres"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
	"github.com/raduolivas/dt/backend/services/gateway/migrations"
)

// t0 has microsecond precision, like timestamptz.
var t0 = time.Date(2026, 10, 20, 12, 0, 0, 123456000, time.UTC)

// starting limits concurrent container starts: rootless Docker's port
// forwarder fails with "address already in use" when many start at once.
var starting = make(chan struct{}, 2)

// setup starts Postgres, applies the gateway migrations as svc_gateway and
// returns a pool and a store over it.
func setup(t *testing.T) (*pgxpool.Pool, *postgres.Store) {
	t.Helper()
	pg := func() *testkit.Postgres {
		starting <- struct{}{}
		defer func() { <-starting }()
		return testkit.StartPostgres(t)
	}()
	ctx := context.Background()
	url := pg.URL("gateway")
	if err := migratex.Up(ctx, url, migrations.FS); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, postgres.New(pool)
}

func inTx(t *testing.T, pool *pgxpool.Pool, fn func(ctx context.Context, tx pgx.Tx) error) {
	t.Helper()
	if err := pgx.BeginFunc(context.Background(), pool, func(tx pgx.Tx) error {
		return fn(context.Background(), tx)
	}); err != nil {
		t.Fatal(err)
	}
}

func record(id, lookup string) domain.KeyRecord {
	return domain.KeyRecord{
		ID: id, TenantID: "ten_01", Lookup: lookup, Env: domain.EnvLive, Hash: []byte{1, 2, 3, 4},
		Scopes: []domain.Scope{domain.ScopeDeliveriesRead, domain.ScopeWebhooksWrite},
		AllowedIPs: []netip.Prefix{
			netip.MustParsePrefix("203.0.113.0/24"),
			netip.MustParsePrefix("2001:db8::1/128"),
		},
	}
}

func TestFindByLookupRoundTrip(t *testing.T) {
	t.Parallel()
	pool, s := setup(t)
	ctx := context.Background()
	want := record("ak_1", "AAAAAAAAAAAA")
	bare := domain.KeyRecord{ID: "ak_2", TenantID: "ten_02", Lookup: "BBBBBBBBBBBB", Env: domain.EnvTest, Hash: []byte{9}}
	inTx(t, pool, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.InsertKey(ctx, tx, want); err != nil {
			return err
		}
		return s.InsertKey(ctx, tx, bare)
	})

	got, err := s.FindByLookup(ctx, "AAAAAAAAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FindByLookup =\n %+v\nwant\n %+v", got, want)
	}
	// Empty arrays come back as nil, matching an unset record.
	got, err = s.FindByLookup(ctx, "BBBBBBBBBBBB")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, bare) {
		t.Fatalf("FindByLookup(bare) = %+v, want %+v", got, bare)
	}
	if _, err := s.FindByLookup(ctx, "CCCCCCCCCCCC"); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("unknown lookup: err = %v, want app.ErrNotFound", err)
	}
}

func TestInsertIsIdempotentAndUpdatesApply(t *testing.T) {
	t.Parallel()
	pool, s := setup(t)
	ctx := context.Background()
	rec := record("ak_1", "AAAAAAAAAAAA")
	inTx(t, pool, func(ctx context.Context, tx pgx.Tx) error { return s.InsertKey(ctx, tx, rec) })

	// A second create of the same id changes nothing.
	changed := rec
	changed.Hash = []byte{7, 7}
	inTx(t, pool, func(ctx context.Context, tx pgx.Tx) error { return s.InsertKey(ctx, tx, changed) })

	exp := t0.Add(24 * time.Hour)
	inTx(t, pool, func(ctx context.Context, tx pgx.Tx) error { return s.SetExpiry(ctx, tx, "ak_1", exp) })
	inTx(t, pool, func(ctx context.Context, tx pgx.Tx) error { return s.Revoke(ctx, tx, "ak_1", t0) })
	// A later revocation does not move revoked_at.
	inTx(t, pool, func(ctx context.Context, tx pgx.Tx) error { return s.Revoke(ctx, tx, "ak_1", t0.Add(time.Hour)) })
	// Updates of an unknown key are no-ops, not errors.
	inTx(t, pool, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.SetExpiry(ctx, tx, "ak_missing", exp); err != nil {
			return err
		}
		return s.Revoke(ctx, tx, "ak_missing", t0)
	})

	got, err := s.FindByLookup(ctx, "AAAAAAAAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Hash, rec.Hash) {
		t.Errorf("hash = %v, want the first insert's %v", got.Hash, rec.Hash)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(exp) || got.ExpiresAt.Location() != time.UTC {
		t.Errorf("expires_at = %v, want %v UTC", got.ExpiresAt, exp)
	}
	if got.RevokedAt == nil || !got.RevokedAt.Equal(t0) {
		t.Errorf("revoked_at = %v, want %v", got.RevokedAt, t0)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM api_keys`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("api_keys has %d rows, want 1", n)
	}
}
