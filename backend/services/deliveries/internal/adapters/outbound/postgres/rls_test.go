package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/raduolivas/dt/backend/pkg/migratex"
	"github.com/raduolivas/dt/backend/pkg/testkit"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/app"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/domain"
	"github.com/raduolivas/dt/backend/services/deliveries/migrations"
)

// TestRLSHidesOtherTenantAndEnvironment runs as svc_deliveries, which does
// not own the tables (mig_deliveries does), so the policy applies to it.
// The queries do not filter on tenant themselves, so this test fails if the
// policy is dropped.
func TestRLSHidesOtherTenantAndEnvironment(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	d := create(t, e, scopeA, fullInput("ord-1"), domain.DefaultTenantSettings(), t0)
	c := upsertCourier(t, e, scopeA, "cour-1")

	for name, s := range map[string]app.Scope{"other tenant": scopeBTest, "other environment": scopeALive} {
		if _, err := e.repo.Get(ctx, s, d.ID); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("%s: Get = %v, want ErrNotFound", name, err)
		}
		ds, more, err := e.repo.List(ctx, s, app.ListFilter{Limit: 100})
		if err != nil || len(ds) != 0 || more {
			t.Errorf("%s: List = %d rows, more=%v, %v; want none", name, len(ds), more, err)
		}
		called := false
		_, err = e.repo.Mutate(ctx, s, d.ID, func(d *domain.Delivery) ([]domain.Event, error) {
			called = true
			return d.Cancel("", t0)
		})
		if !errors.Is(err, app.ErrNotFound) || called {
			t.Errorf("%s: Mutate = %v (fn called: %v), want ErrNotFound", name, err, called)
		}
		if _, err := e.repo.Courier(ctx, s, c.ID); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("%s: Courier = %v, want ErrNotFound", name, err)
		}
	}
	// The owner still sees it, untouched.
	got, err := e.repo.Get(ctx, scopeA, d.ID)
	if err != nil || got.Status != domain.StatusConfirmed {
		t.Fatalf("own Get = %+v, %v", got, err)
	}

	// Outside a tenant transaction the service role sees nothing, both on a
	// fresh connection and on one that already ran a tenant transaction
	// (where the settings are reset to '').
	conn, err := e.svc.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	for _, when := range []string{"fresh connection", "after a tenant transaction"} {
		for _, table := range []string{"deliveries", "delivery_history", "couriers"} {
			var n int
			err := conn.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n)
			if err == nil && n != 0 {
				t.Errorf("%s: %s outside a tenant transaction: %d rows visible", when, table, n)
			}
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('dt.tenant_id', 'ten_a', true), set_config('dt.environment', 'test', true)`); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM deliveries").Scan(&n); err != nil || n != 1 {
			t.Fatalf("inside the tenant transaction: %d rows, %v; want 1", n, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRuntimeRoleCannotWeakenRLS pins OQ16: migrations run as mig_deliveries,
// the tables' owner, so svc_deliveries can use the tables but cannot turn
// row-level security off.
func TestRuntimeRoleCannotWeakenRLS(t *testing.T) {
	t.Parallel()
	pg := func() *testkit.Postgres {
		starting <- struct{}{}
		defer func() { <-starting }()
		return testkit.StartPostgres(t)
	}()
	ctx := context.Background()
	if err := migratex.Up(ctx, pg.MigratorURL("deliveries"), migrations.FS); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, pg.URL("deliveries"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()

	for _, table := range []string{"couriers", "deliveries", "delivery_history"} {
		for _, stmt := range []string{
			"ALTER TABLE " + table + " NO FORCE ROW LEVEL SECURITY",
			"ALTER TABLE " + table + " DISABLE ROW LEVEL SECURITY",
		} {
			_, err := conn.Exec(ctx, stmt)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
				t.Errorf("%s = %v, want insufficient privilege (42501)", stmt, err)
			}
		}
	}

	// The runtime role still reads and writes inside a tenant transaction.
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('dt.tenant_id', 'ten_a', true), set_config('dt.environment', 'test', true)`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO couriers (id, tenant_id, environment, external_id, display_name, vehicle, created_at, updated_at)
			VALUES ('cour_1', 'ten_a', 'test', 'ext-1', 'Ana', 'bicycle', now(), now())`); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM couriers").Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Errorf("couriers visible in the tenant transaction = %d, want 1", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tenant transaction: %v", err)
	}
}
