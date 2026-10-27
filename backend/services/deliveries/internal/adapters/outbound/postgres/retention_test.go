package postgres_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/raduolivas/dt/backend/services/deliveries/internal/app"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/domain"
)

func rows(t *testing.T, e env, sql, id string) int {
	t.Helper()
	var n int
	if err := e.admin.QueryRow(context.Background(), sql, id).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func TestDeliveriesRetention13Months(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	st := domain.DefaultTenantSettings()
	now := time.Now().UTC().Truncate(time.Microsecond)
	created := now.AddDate(-1, -3, 0)
	cancelled := func(s app.Scope, ext string, at time.Time) string {
		d := create(t, e, s, fullInput(ext), st, created)
		mutate(t, e, s, d.ID, func(d *domain.Delivery) ([]domain.Event, error) { return d.Cancel("gone", at) })
		return d.ID
	}
	kept := cancelled(scopeA, "kept", now.AddDate(0, -12, -29))
	gone := cancelled(scopeA, "gone", now.AddDate(0, -13, -1))
	goneB := cancelled(scopeBTest, "gone-b", now.AddDate(0, -13, -1))
	open := create(t, e, scopeA, fullInput("open"), st, created).ID
	// A pickup confirmation watch left on an expired delivery goes with it.
	if _, err := e.admin.Exec(ctx, `INSERT INTO deliveries.pickup_watch (delivery_id, tenant_id, environment, since, due_at)
		VALUES ($1, 'ten_a', 'test', $2, $2)`, gone, created); err != nil {
		t.Fatal(err)
	}

	svc := &app.Service{Retention: e.repo, Log: slog.New(slog.DiscardHandler)}
	for range 2 { // idempotent
		if err := svc.RunRetention(ctx, now); err != nil {
			t.Fatal(err)
		}
	}

	for id, want := range map[string]int{kept: 1, gone: 0, goneB: 0, open: 1} {
		if n := rows(t, e, `SELECT count(*) FROM deliveries.deliveries WHERE id = $1`, id); n != want {
			t.Errorf("%s: %d deliveries rows, want %d", id, n, want)
		}
		if n := rows(t, e, `SELECT count(*) FROM deliveries.delivery_history WHERE delivery_id = $1`, id); (n > 0) != (want == 1) {
			t.Errorf("%s: %d history rows", id, n)
		}
	}
	if n := rows(t, e, `SELECT count(*) FROM deliveries.pickup_watch WHERE delivery_id = $1`, gone); n != 0 {
		t.Errorf("pickup watch kept")
	}
	if _, err := e.repo.Get(ctx, scopeA, kept); err != nil {
		t.Fatalf("kept delivery: %v", err)
	}
}
