package postgres_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/raduolivas/dt/backend/services/tracking/internal/adapters/outbound/postgres"
	"github.com/raduolivas/dt/backend/services/tracking/internal/app"
	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

// retentionSvc is the tracking service as the nightly job runs it.
func retentionSvc(e env) *app.Service {
	return &app.Service{Retention: e.repo, Log: slog.New(slog.DiscardHandler)}
}

// zigzag stores n fixes for del in s from start, one a minute, heading
// north with a 50 m swing east every other fix (so simplification keeps
// the corners).
func zigzag(t *testing.T, e env, s app.Scope, del string, start time.Time, n int) {
	t.Helper()
	rows := make([]app.TrailRow, n)
	for i := range rows {
		r := row(s, del, "cou_"+del, start.Add(time.Duration(i)*time.Minute), 52.36+float64(i)*0.001)
		if i%2 == 1 {
			r.Fix.Pos.Lng += 0.0007
		}
		rows[i] = r
	}
	w := postgres.NewTrailWriter(e.svc, nil)
	w.Append(rows)
	if err := w.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, e env, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.admin.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// partitionsBefore counts the trail's day partitions for days before day.
func partitionsBefore(t *testing.T, e env, day time.Time) int {
	t.Helper()
	return count(t, e, `SELECT count(*) FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
		WHERE i.inhparent = 'tracking.trail_fixes'::regclass AND c.relname < $1`,
		"trail_fixes_"+day.UTC().Format("20060102"))
}

func partitionExists(t *testing.T, e env, day time.Time) bool {
	t.Helper()
	return count(t, e, `SELECT count(*) FROM pg_class WHERE relname = $1`, "trail_fixes_"+day.UTC().Format("20060102")) == 1
}

// finalize makes del final at at, as the consumer does.
func finalize(t *testing.T, e env, s app.Scope, del string, at time.Time) {
	t.Helper()
	inScope(t, e, s, func(ctx context.Context, p app.Projection) error {
		rec, _, err := p.Delivery(ctx, del)
		if err != nil {
			return err
		}
		rec.Status, rec.FinalAt = domain.StatusDelivered, &at
		return p.PutDelivery(ctx, rec)
	})
}

func inScope(t *testing.T, e env, s app.Scope, fn func(context.Context, app.Projection) error) {
	t.Helper()
	ctx := context.Background()
	err := pgx.BeginFunc(ctx, e.svc, func(tx pgx.Tx) error {
		p, err := e.repo.Bind(ctx, tx, s)
		if err != nil {
			return err
		}
		return fn(ctx, p)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func trailOf(t *testing.T, e env, s app.Scope, del string) app.TrailRead {
	t.Helper()
	tr, err := e.repo.Trail(context.Background(), s, del, app.TrailEntry{}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestRetentionSimplifiesBeforeDropping(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old, oldB, fresh := now.AddDate(0, 0, -31), now.AddDate(0, 0, -40), now.Add(-2*time.Hour)
	seed(t, e, scopeA, "cou_del_old", "del_old", old)
	seed(t, e, scopeB, "cou_del_old_b", "del_old_b", oldB)
	seed(t, e, scopeA, "cou_del_new", "del_new", fresh)
	zigzag(t, e, scopeA, "del_old", old, 7)
	zigzag(t, e, scopeB, "del_old_b", oldB, 5)
	zigzag(t, e, scopeA, "del_new", fresh, 4)

	if err := retentionSvc(e).RunRetention(ctx, now); err != nil {
		t.Fatal(err)
	}

	// No delivery lost its route: each old trail is now its simplified route,
	// endpoints kept, in every scope.
	for _, c := range []struct {
		s   app.Scope
		del string
		n   int
	}{{scopeA, "del_old", 7}, {scopeB, "del_old_b", 5}} {
		tr := trailOf(t, e, c.s, c.del)
		if tr.RawAvailable || len(tr.Entries) != 0 || len(tr.Simplified) < 2 || len(tr.Simplified) > c.n {
			t.Fatalf("%s trail = %+v", c.del, tr)
		}
		if first := tr.Simplified[0]; first.Lat != 52.36 || first.Lng != 4.9 {
			t.Fatalf("%s starts at %+v", c.del, first)
		}
	}
	// The raw fixes older than 30 days are gone with their partitions.
	cutoff := now.AddDate(0, 0, -30)
	if n := partitionsBefore(t, e, cutoff); n != 0 {
		t.Fatalf("%d partitions older than 30 days left", n)
	}
	if n := count(t, e, `SELECT count(*) FROM tracking.trail_fixes WHERE delivery_id IN ('del_old', 'del_old_b')`); n != 0 {
		t.Fatalf("%d old raw fixes left", n)
	}
	// A recent trail is untouched.
	if tr := trailOf(t, e, scopeA, "del_new"); !tr.RawAvailable || len(tr.Entries) != 4 || tr.Simplified != nil {
		t.Fatalf("recent trail = %+v", tr)
	}
	// Partitions exist three days ahead.
	for d := range 4 {
		if !partitionExists(t, e, now.AddDate(0, 0, d)) {
			t.Fatalf("no partition %d days ahead", d)
		}
	}
}

func TestRetentionIsIdempotent(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -35)
	seed(t, e, scopeA, "cou_del_old", "del_old", old)
	zigzag(t, e, scopeA, "del_old", old, 6)
	finalize(t, e, scopeA, "del_old", now.AddDate(0, -13, -2))
	seed(t, e, scopeA, "cou_del_kept", "del_kept", old)
	zigzag(t, e, scopeA, "del_kept", old, 6)

	svc := retentionSvc(e)
	if err := svc.RunRetention(ctx, now); err != nil {
		t.Fatal(err)
	}
	var created time.Time
	if err := e.admin.QueryRow(ctx, `SELECT created_at FROM tracking.simplified_routes WHERE delivery_id = 'del_kept'`).Scan(&created); err != nil {
		t.Fatal(err)
	}
	before := trailOf(t, e, scopeA, "del_kept")
	if err := svc.RunRetention(ctx, now.Add(time.Minute)); err != nil {
		t.Fatalf("second run: %v", err)
	}
	var again time.Time
	if err := e.admin.QueryRow(ctx, `SELECT created_at FROM tracking.simplified_routes WHERE delivery_id = 'del_kept'`).Scan(&again); err != nil || !again.Equal(created) {
		t.Fatalf("route rewritten: %v -> %v, %v", created, again, err)
	}
	if after := trailOf(t, e, scopeA, "del_kept"); len(after.Simplified) != len(before.Simplified) || after.RawAvailable {
		t.Fatalf("trail changed: %+v -> %+v", before, after)
	}
	if n := count(t, e, `SELECT count(*) FROM tracking.deliveries WHERE id = 'del_old'`); n != 0 {
		t.Fatalf("expired delivery kept")
	}
}

func TestRetention13Months(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	now := time.Now().UTC()
	at := now.AddDate(-1, -2, 0)
	for _, del := range []string{"del_kept", "del_gone", "del_open"} {
		seed(t, e, scopeA, "cou_"+del, del, at)
		inScope(t, e, scopeA, func(ctx context.Context, p app.Projection) error {
			fix := domain.Fix{T: at, Pos: domain.LatLng{Lat: 52.37, Lng: 4.89}, AccuracyM: 5}
			return p.PutProofPoint(ctx, app.ProofPoint{DeliveryID: del, Status: "delivered", At: at, Actor: "courier", Fix: &fix})
		})
		if _, err := e.admin.Exec(ctx, `INSERT INTO tracking.simplified_routes (delivery_id, tenant_id, environment, points, created_at)
			VALUES ($1, 'ten_a', 'test', '[[52.36, 4.9], [52.37, 4.89]]', $2)`, del, at); err != nil {
			t.Fatal(err)
		}
	}
	finalize(t, e, scopeA, "del_kept", now.AddDate(0, -12, -29))
	finalize(t, e, scopeA, "del_gone", now.AddDate(0, -13, -1))

	if err := retentionSvc(e).RunRetention(ctx, now); err != nil {
		t.Fatal(err)
	}

	for del, want := range map[string]int{"del_kept": 1, "del_gone": 0, "del_open": 1} {
		for _, table := range []string{"deliveries", "simplified_routes", "proof_points"} {
			col := "delivery_id"
			if table == "deliveries" {
				col = "id"
			}
			if n := count(t, e, `SELECT count(*) FROM tracking.`+table+` WHERE `+col+` = $1`, del); n != want {
				t.Errorf("%s: %d rows in %s, want %d", del, n, table, want)
			}
		}
	}
}

func TestErasureReplacesRawTrail(t *testing.T) {
	e := setup(t)
	now := time.Now().UTC()
	start := now.Add(-time.Hour)
	seed(t, e, scopeA, "cou_del_a", "del_a", start)
	seed(t, e, scopeA, "cou_del_b", "del_b", start)
	zigzag(t, e, scopeA, "del_a", start, 9)
	zigzag(t, e, scopeA, "del_b", start, 3)
	inScope(t, e, scopeA, func(ctx context.Context, p app.Projection) error {
		return p.PutProofPoint(ctx, app.ProofPoint{DeliveryID: "del_a", Status: "at_pickup", At: start, Actor: "courier",
			Fix: &domain.Fix{T: start, Pos: domain.LatLng{Lat: 52.36, Lng: 4.9}, AccuracyM: 5}})
	})
	svc := retentionSvc(e)
	erase := func() {
		inScope(t, e, scopeA, func(ctx context.Context, p app.Projection) error {
			return svc.OnPersonalDataErased(ctx, p, scopeA, 9, "del_a", now)
		})
	}
	erase()

	if n := count(t, e, `SELECT count(*) FROM tracking.trail_fixes WHERE delivery_id = 'del_a'`); n != 0 {
		t.Fatalf("raw fixes after erasure = %d", n)
	}
	tr := trailOf(t, e, scopeA, "del_a")
	if tr.RawAvailable || len(tr.Simplified) < 2 || len(tr.Simplified) > 9 {
		t.Fatalf("erased trail = %+v", tr)
	}
	pts, err := e.repo.ProofPoints(context.Background(), scopeA, "del_a")
	if err != nil || len(pts) != 1 || pts[0].Fix == nil {
		t.Fatalf("proof points = %+v, %v", pts, err)
	}
	// Another delivery's trail is untouched.
	if tr := trailOf(t, e, scopeA, "del_b"); !tr.RawAvailable || len(tr.Entries) != 3 {
		t.Fatalf("other trail = %+v", tr)
	}

	// A redelivery (or a late fix flushed after the erasure) ends in the
	// same state: no raw fix, the first simplified route.
	zigzag(t, e, scopeA, "del_a", now, 2)
	erase()
	if n := count(t, e, `SELECT count(*) FROM tracking.trail_fixes WHERE delivery_id = 'del_a'`); n != 0 {
		t.Fatalf("raw fixes after a repeated erasure = %d", n)
	}
	if again := trailOf(t, e, scopeA, "del_a"); len(again.Simplified) != len(tr.Simplified) {
		t.Fatalf("route changed: %+v -> %+v", tr.Simplified, again.Simplified)
	}
}
