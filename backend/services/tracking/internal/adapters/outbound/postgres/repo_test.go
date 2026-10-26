package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/raduolivas/dt/backend/pkg/migratex"
	"github.com/raduolivas/dt/backend/pkg/testkit"
	"github.com/raduolivas/dt/backend/services/tracking/internal/adapters/outbound/postgres"
	"github.com/raduolivas/dt/backend/services/tracking/internal/app"
	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
	"github.com/raduolivas/dt/backend/services/tracking/migrations"
)

var (
	scopeA = app.Scope{TenantID: "ten_a", Env: "test"}
	scopeB = app.Scope{TenantID: "ten_b", Env: "test"}
)

// starting limits concurrent container starts (rootless Docker port
// forwarder).
var starting = make(chan struct{}, 2)

type env struct {
	svc   *pgxpool.Pool // svc_tracking: subject to RLS
	admin *pgxpool.Pool // superuser, for assertions only
	repo  *postgres.Repo
}

func setup(t *testing.T) env {
	t.Helper()
	pg := func() *testkit.Postgres {
		starting <- struct{}{}
		defer func() { <-starting }()
		return testkit.StartPostgres(t)
	}()
	ctx := context.Background()
	if err := migratex.Up(ctx, pg.MigratorURL("tracking"), migrations.FS); err != nil {
		t.Fatal(err)
	}
	svc, err := pgxpool.New(ctx, pg.URL("tracking"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	admin, err := pgxpool.New(ctx, pg.URL("admin"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	return env{svc: svc, admin: admin, repo: postgres.New(svc)}
}

// seed stores a courier and a delivery assigned to it in s, through the
// consumer projection.
func seed(t *testing.T, e env, s app.Scope, courierID, deliveryID string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	err := pgx.BeginFunc(ctx, e.svc, func(tx pgx.Tx) error {
		p, err := e.repo.Bind(ctx, tx, s)
		if err != nil {
			return err
		}
		if err := p.PutCourier(ctx, domain.Courier{ID: courierID, ExternalID: "x", DisplayName: "Sam", Vehicle: "car"}); err != nil {
			return err
		}
		cid := courierID
		return p.PutDelivery(ctx, app.DeliveryRecord{
			ID: deliveryID, TenantID: s.TenantID, Env: s.Env, Model: domain.ModelEndToEnd, Status: domain.StatusCourierAssigned,
			Pickup: domain.LatLng{Lat: 52.36, Lng: 4.9}, PickupName: "Hub", Dropoff: domain.LatLng{Lat: 52.37, Lng: 4.89},
			CreatedAt: at, CourierID: &cid, Timeline: []domain.TimelineEntry{{Status: domain.StatusConfirmed, At: at}},
			UpdatedAt: at, EventVersion: 2,
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func row(s app.Scope, del, cou string, t time.Time, lat float64) app.TrailRow {
	sp := 4.2
	return app.TrailRow{DeliveryID: del, TenantID: s.TenantID, Env: s.Env, CourierID: cou,
		Fix: domain.Fix{T: t, Pos: domain.LatLng{Lat: lat, Lng: 4.9}, AccuracyM: 5, SpeedMps: &sp, Source: "relay"}}
}

func TestRLSHidesOtherTenantTrail(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := e.repo.EnsurePartitions(ctx, now, 3); err != nil {
		t.Fatal(err)
	}
	seed(t, e, scopeA, "cou_a", "del_a", now)
	seed(t, e, scopeB, "cou_b", "del_b", now)
	w := postgres.NewTrailWriter(e.svc, nil)
	w.Append([]app.TrailRow{row(scopeA, "del_a", "cou_a", now, 52.361), row(scopeB, "del_b", "cou_b", now, 52.362)})
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	// A's scope sees A's fix, not B's, even when asking for B's delivery.
	got, err := e.repo.RecentTrail(ctx, scopeA, "del_a", "cou_a", now.Add(-time.Minute), now)
	if err != nil || len(got) != 1 || got[0].Pos.Lat != 52.361 || got[0].SpeedMps == nil {
		t.Fatalf("A trail = %+v, %v", got, err)
	}
	if got, _ := e.repo.RecentTrail(ctx, scopeA, "del_b", "cou_b", now.Add(-time.Minute), now); len(got) != 0 {
		t.Fatalf("A sees B's trail: %+v", got)
	}
	if _, err := e.repo.LoadState(ctx, scopeA, "del_b"); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("A loads B's delivery: %v", err)
	}
	if _, err := e.repo.CourierActiveDeliveries(ctx, scopeA, "cou_b"); !errors.Is(err, app.ErrCourierNotFound) {
		t.Fatalf("A sees B's courier: %v", err)
	}
	// Outside a tenant transaction the runtime role sees nothing (fail
	// closed), through the parent and through a partition directly.
	part := "trail_fixes_" + now.Format("20060102")
	for _, table := range []string{"trail_fixes", part, "deliveries", "couriers"} {
		var n int
		if err := e.svc.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("%s: %d rows visible without a tenant", table, n)
		}
	}
	var n int
	if err := e.admin.QueryRow(ctx, "SELECT count(*) FROM tracking."+part).Scan(&n); err != nil || n != 2 {
		t.Fatalf("admin count = %d, %v", n, err)
	}
	// A tenant transaction cannot write another tenant's rows.
	err = pgx.BeginFunc(ctx, e.svc, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('dt.tenant_id', 'ten_a', true), set_config('dt.environment', 'test', true)`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO trail_fixes (delivery_id, tenant_id, environment, courier_id, t, lat, lng, accuracy_m, source)
			VALUES ('del_b', 'ten_b', 'test', 'cou_b', now(), 1, 1, 1, 'relay')`)
		return err
	})
	if err == nil {
		t.Fatal("inserted a row for another tenant")
	}
}

func TestTrailWriterFlushesOnShutdown(t *testing.T) {
	e := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Now().UTC().Truncate(time.Microsecond)
	seed(t, e, scopeA, "cou_a", "del_a", now)
	w := postgres.NewTrailWriter(e.svc, nil)
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	// No partition exists yet: the writer creates the day's partition itself.
	w.Append([]app.TrailRow{row(scopeA, "del_a", "cou_a", now, 52.361), row(scopeA, "del_a", "cou_a", now.Add(time.Millisecond), 52.362)})
	cancel() // before the first 1 s tick
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	var n int
	if err := e.admin.QueryRow(context.Background(), "SELECT count(*) FROM tracking.trail_fixes").Scan(&n); err != nil || n != 2 {
		t.Fatalf("rows after shutdown = %d, %v", n, err)
	}
}

func TestTrailWriterFlushesAtBatchSize(t *testing.T) {
	e := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := e.repo.EnsurePartitions(ctx, now, 0); err != nil {
		t.Fatal(err)
	}
	seed(t, e, scopeA, "cou_a", "del_a", now)
	w := postgres.NewTrailWriter(e.svc, nil)
	go func() { _ = w.Run(ctx) }()
	rows := make([]app.TrailRow, postgres.TrailFlushRows)
	for i := range rows {
		rows[i] = row(scopeA, "del_a", "cou_a", now.Add(time.Duration(i)*time.Microsecond), 52.36)
	}
	w.Append(rows)
	deadline := time.Now().Add(900 * time.Millisecond) // before the 1 s tick
	for {
		var n int
		_ = e.admin.QueryRow(ctx, "SELECT count(*) FROM tracking.trail_fixes").Scan(&n)
		if n == len(rows) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("rows = %d, want %d before the timer", n, len(rows))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLoadStateRebuildsFromReadModel(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := e.repo.EnsurePartitions(ctx, now, 0); err != nil {
		t.Fatal(err)
	}
	seed(t, e, scopeA, "cou_a", "del_a", now)
	w := postgres.NewTrailWriter(e.svc, nil)
	w.Append([]app.TrailRow{
		row(scopeA, "del_a", "cou_a", now, 52.361),
		row(scopeA, "del_a", "cou_old", now.Add(time.Second), 52.369), // the previous courier's: ignored
	})
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	st, err := e.repo.LoadState(ctx, scopeA, "del_a")
	if err != nil {
		t.Fatal(err)
	}
	if st.Courier == nil || st.Courier.DisplayName != "Sam" || st.LastFix == nil || st.LastFix.Pos.Lat != 52.361 ||
		st.Status != domain.StatusCourierAssigned || len(st.Timeline) != 1 || st.Pickup.Lat != 52.36 {
		t.Fatalf("state = %+v (fix %+v)", st, st.LastFix)
	}
	// The trail is per courier: the previous courier's fix is not returned.
	tr, err := e.repo.RecentTrail(ctx, scopeA, "del_a", "cou_a", now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil || len(tr) != 1 || tr[0].Pos.Lat != 52.361 {
		t.Fatalf("courier trail = %+v, %v", tr, err)
	}
	ids, err := e.repo.CourierActiveDeliveries(ctx, scopeA, "cou_a")
	if err != nil || len(ids) != 1 || ids[0] != "del_a" {
		t.Fatalf("active = %v, %v", ids, err)
	}
}

func TestEnsurePartitionsIsIdempotent(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	now := time.Now()
	for range 2 {
		if err := e.repo.EnsurePartitions(ctx, now, 3); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := e.admin.QueryRow(ctx, `SELECT count(*) FROM pg_inherits WHERE inhparent = 'tracking.trail_fixes'::regclass`).Scan(&n); err != nil || n != 4 {
		t.Fatalf("partitions = %d, %v", n, err)
	}
}

// TestPartitionFunctionIsLockedDown: the SECURITY DEFINER helper runs as the
// schema owner, so only the tracking runtime role may call it, and its
// search_path is pinned with pg_temp last.
func TestPartitionFunctionIsLockedDown(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	const fn = "tracking.ensure_trail_partition(date)"
	for role, want := range map[string]bool{"svc_tracking": true, "svc_deliveries": false, "svc_gateway": false} {
		var got bool
		if err := e.admin.QueryRow(ctx, `SELECT has_function_privilege($1, $2, 'EXECUTE')`, role, fn).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s EXECUTE = %v, want %v", role, got, want)
		}
	}
	var cfg []string
	if err := e.admin.QueryRow(ctx, `SELECT proconfig FROM pg_proc WHERE oid = $1::regprocedure`, fn).Scan(&cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg) != 1 || cfg[0] != "search_path=tracking, extensions, pg_temp" {
		t.Fatalf("proconfig = %v", cfg)
	}
}
