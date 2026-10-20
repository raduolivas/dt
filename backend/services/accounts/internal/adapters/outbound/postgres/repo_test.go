package postgres_test

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/migratex"
	"github.com/raduolivas/dt/backend/pkg/testkit"
	"github.com/raduolivas/dt/backend/services/accounts/internal/adapters/outbound/postgres"
	"github.com/raduolivas/dt/backend/services/accounts/internal/app"
	"github.com/raduolivas/dt/backend/services/accounts/internal/domain"
	"github.com/raduolivas/dt/backend/services/accounts/migrations"
)

// t0 has microsecond precision, like timestamptz.
var t0 = time.Date(2026, 10, 20, 12, 0, 0, 123456000, time.UTC)

// starting limits concurrent container starts: rootless Docker's port
// forwarder fails with "address already in use" when many start at once.
var starting = make(chan struct{}, 2)

// setup starts Postgres, applies the accounts migrations as svc_accounts and
// returns a pool and a repo over it.
func setup(t *testing.T) (*pgxpool.Pool, *postgres.Repo) {
	t.Helper()
	pg := func() *testkit.Postgres {
		starting <- struct{}{}
		defer func() { <-starting }()
		return testkit.StartPostgres(t)
	}()
	ctx := context.Background()
	url := pg.URL("accounts")
	if err := migratex.Up(ctx, url, migrations.FS); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, postgres.New(pool, func() time.Time { return t0 })
}

func tenant(id string) domain.Tenant {
	return domain.Tenant{ID: id, Name: "Acme " + id, CreatedAt: t0}
}

func key(id, tenantID, lookup string) domain.APIKey {
	return domain.APIKey{
		ID: id, TenantID: tenantID, Name: "ci", Lookup: lookup, DisplayPrefix: "dt_sk_test_" + lookup,
		Env: domain.EnvTest, Hash: []byte{1, 2, 3, 4}, Scopes: []string{"deliveries:read", "webhooks:write"},
		AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("2001:db8::1/128")},
		CreatedBy:  domain.Actor{Type: "console_user", ID: "usr_1", Role: domain.RoleAdmin},
		CreatedAt:  t0,
	}
}

func inTx(t *testing.T, r *postgres.Repo, fn func(ctx context.Context, tx app.Tx) error) error {
	t.Helper()
	return r.InTx(context.Background(), fn)
}

func mustTx(t *testing.T, r *postgres.Repo, fn func(ctx context.Context, tx app.Tx) error) {
	t.Helper()
	if err := inTx(t, r, fn); err != nil {
		t.Fatal(err)
	}
}

func TestTenantAndKeyRoundTrip(t *testing.T) {
	t.Parallel()
	_, r := setup(t)
	want := key("ak_1", "ten_1", "lookup000001")
	mustTx(t, r, func(ctx context.Context, tx app.Tx) error {
		if err := tx.InsertTenant(ctx, tenant("ten_1")); err != nil {
			return err
		}
		return tx.InsertKey(ctx, want)
	})

	mustTx(t, r, func(ctx context.Context, tx app.Tx) error {
		got, err := tx.GetTenant(ctx, "ten_1")
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(got, tenant("ten_1")) {
			t.Errorf("tenant = %+v, want %+v", got, tenant("ten_1"))
		}
		if err := tx.SetLiveEnabled(ctx, "ten_1", true); err != nil {
			return err
		}
		if got, _ := tx.GetTenant(ctx, "ten_1"); !got.LiveEnabled {
			t.Error("live_enabled not set")
		}

		k, err := tx.GetKeyForUpdate(ctx, "ten_1", "ak_1")
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(k, want) {
			t.Errorf("key =\n %+v\nwant\n %+v", k, want)
		}

		exp, rev := t0.Add(time.Hour), t0.Add(2*time.Hour)
		k.ExpiresAt, k.RevokedAt = &exp, &rev
		if err := tx.UpdateKey(ctx, k); err != nil {
			return err
		}
		list, err := tx.ListKeys(ctx, "ten_1", domain.EnvTest)
		if err != nil {
			return err
		}
		if len(list) != 1 || !reflect.DeepEqual(list[0], k) {
			t.Errorf("list = %+v, want [%+v]", list, k)
		}
		if live, _ := tx.ListKeys(ctx, "ten_1", domain.EnvLive); len(live) != 0 {
			t.Errorf("live list = %+v, want empty", live)
		}
		return nil
	})
}

func TestNotFound(t *testing.T) {
	t.Parallel()
	_, r := setup(t)
	mustTx(t, r, func(ctx context.Context, tx app.Tx) error {
		if err := tx.InsertTenant(ctx, tenant("ten_1")); err != nil {
			return err
		}
		if err := tx.InsertTenant(ctx, tenant("ten_2")); err != nil {
			return err
		}
		return tx.InsertKey(ctx, key("ak_1", "ten_1", "lookup000001"))
	})
	tests := []struct {
		name string
		fn   func(ctx context.Context, tx app.Tx) error
	}{
		{"key of another tenant", func(ctx context.Context, tx app.Tx) error {
			_, err := tx.GetKeyForUpdate(ctx, "ten_2", "ak_1")
			return err
		}},
		{"missing key", func(ctx context.Context, tx app.Tx) error {
			_, err := tx.GetKeyForUpdate(ctx, "ten_1", "ak_missing")
			return err
		}},
		{"missing tenant", func(ctx context.Context, tx app.Tx) error {
			_, err := tx.GetTenant(ctx, "ten_missing")
			return err
		}},
		{"set live on missing tenant", func(ctx context.Context, tx app.Tx) error {
			return tx.SetLiveEnabled(ctx, "ten_missing", true)
		}},
		{"update key of another tenant", func(ctx context.Context, tx app.Tx) error {
			k := key("ak_1", "ten_2", "lookup000001")
			return tx.UpdateKey(ctx, k)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := inTx(t, r, tc.fn); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("err = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestGetKeyForUpdateLocksRow(t *testing.T) {
	t.Parallel()
	_, r := setup(t)
	mustTx(t, r, func(ctx context.Context, tx app.Tx) error {
		if err := tx.InsertTenant(ctx, tenant("ten_1")); err != nil {
			return err
		}
		return tx.InsertKey(ctx, key("ak_1", "ten_1", "lookup000001"))
	})
	locked := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- r.InTx(context.Background(), func(ctx context.Context, tx app.Tx) error {
			if _, err := tx.GetKeyForUpdate(ctx, "ten_1", "ak_1"); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := r.InTx(ctx, func(ctx context.Context, tx app.Tx) error {
		_, err := tx.GetKeyForUpdate(ctx, "ten_1", "ak_1")
		return err
	})
	if err == nil {
		t.Fatal("second GetKeyForUpdate did not block on the row lock")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func countOutbox(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPublishIsTransactional(t *testing.T) {
	t.Parallel()
	pool, r := setup(t)
	payload := &eventsv1.ApiKeyRevoked{KeyId: "ak_1"}
	errRollback := errors.New("rollback")
	err := inTx(t, r, func(ctx context.Context, tx app.Tx) error {
		if err := tx.Publish(ctx, app.EventKeyRevoked, "ten_1", domain.EnvLive, payload); err != nil {
			return err
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("err = %v", err)
	}
	if n := countOutbox(t, pool); n != 0 {
		t.Fatalf("outbox has %d rows after rollback, want 0", n)
	}

	mustTx(t, r, func(ctx context.Context, tx app.Tx) error {
		return tx.Publish(ctx, app.EventKeyRevoked, "ten_1", domain.EnvLive, payload)
	})
	var subject string
	var raw []byte
	if err := pool.QueryRow(context.Background(), `SELECT subject, payload FROM outbox`).Scan(&subject, &raw); err != nil {
		t.Fatal(err)
	}
	if subject != "accounts.apikey.revoked" {
		t.Errorf("subject = %q", subject)
	}
	var env eventsv1.Envelope
	if err := proto.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if env.GetType() != app.EventKeyRevoked || env.GetTenantId() != "ten_1" ||
		env.GetEnvironment() != eventsv1.Environment_ENVIRONMENT_LIVE || env.GetSource() != "accounts" ||
		!env.GetOccurredAt().AsTime().Equal(t0) {
		t.Errorf("envelope = %v", &env)
	}
	var got eventsv1.ApiKeyRevoked
	if err := env.GetPayload().UnmarshalTo(&got); err != nil || got.GetKeyId() != "ak_1" {
		t.Errorf("payload = %v, %v", &got, err)
	}
}

func TestDuplicateLookupIsAnError(t *testing.T) {
	t.Parallel()
	_, r := setup(t)
	mustTx(t, r, func(ctx context.Context, tx app.Tx) error {
		if err := tx.InsertTenant(ctx, tenant("ten_1")); err != nil {
			return err
		}
		return tx.InsertKey(ctx, key("ak_1", "ten_1", "lookup000001"))
	})
	err := inTx(t, r, func(ctx context.Context, tx app.Tx) error {
		return tx.InsertKey(ctx, key("ak_2", "ten_1", "lookup000001"))
	})
	if err == nil {
		t.Fatal("duplicate lookup accepted")
	}
	if errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want a storage error", err)
	}
}
