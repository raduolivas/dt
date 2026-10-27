package nightly_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/raduolivas/dt/backend/pkg/nightly"
	"github.com/raduolivas/dt/backend/pkg/testkit"
)

func TestNextIsTheFollowing0200UTC(t *testing.T) {
	ams := time.FixedZone("CEST", 2*3600)
	cases := []struct{ now, want time.Time }{
		{time.Date(2026, 10, 27, 1, 59, 59, 0, time.UTC), time.Date(2026, 10, 27, 2, 0, 0, 0, time.UTC)},
		{time.Date(2026, 10, 27, 2, 0, 0, 0, time.UTC), time.Date(2026, 10, 28, 2, 0, 0, 0, time.UTC)},
		{time.Date(2026, 10, 27, 23, 0, 0, 0, time.UTC), time.Date(2026, 10, 28, 2, 0, 0, 0, time.UTC)},
		// 03:30 in UTC+2 is 01:30 UTC: the same UTC day's 02:00.
		{time.Date(2026, 10, 27, 3, 30, 0, 0, ams), time.Date(2026, 10, 27, 2, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		if got := nightly.Next(c.now); !got.Equal(c.want) || got.Location() != time.UTC {
			t.Errorf("Next(%v) = %v, want %v", c.now, got, c.want)
		}
	}
}

type fakeLease struct {
	mu     sync.Mutex
	holder string
	ttl    time.Duration
	err    error
}

func (l *fakeLease) Hold(_ context.Context, holder string, ttl time.Duration) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return false, l.err
	}
	if l.holder != "" && l.holder != holder {
		return false, nil
	}
	l.holder, l.ttl = holder, ttl
	return true, nil
}

func TestTickRunsOnlyOnTheLeaseHolder(t *testing.T) {
	lease := &fakeLease{}
	now := time.Date(2026, 10, 27, 2, 0, 0, 0, time.UTC)
	var runs []time.Time
	job := func(holder string) *nightly.Job {
		return &nightly.Job{Name: "test_retention", Lease: lease, Holder: holder,
			Now: func() time.Time { return now },
			Do:  func(_ context.Context, at time.Time) error { runs = append(runs, at); return nil }}
	}
	if ran, err := job("a").Tick(context.Background()); !ran || err != nil {
		t.Fatalf("a: ran %v, %v", ran, err)
	}
	if ran, err := job("b").Tick(context.Background()); ran || err != nil {
		t.Fatalf("b ran while a holds the lease: %v, %v", ran, err)
	}
	if len(runs) != 1 || !runs[0].Equal(now) {
		t.Fatalf("runs = %v", runs)
	}
	// The lease outlives the run, so no instance runs the same night twice.
	if lease.ttl != nightly.LeaseTTL || nightly.LeaseTTL <= nightly.RunTimeout {
		t.Fatalf("lease ttl %v, run timeout %v", lease.ttl, nightly.RunTimeout)
	}
}

func TestTickReportsLeaseAndRunErrors(t *testing.T) {
	boom := errors.New("boom")
	j := &nightly.Job{Name: "x", Lease: &fakeLease{err: boom}, Holder: "a",
		Do: func(context.Context, time.Time) error { t.Fatal("ran without the lease"); return nil }}
	if _, err := j.Tick(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("lease error = %v", err)
	}
	j = &nightly.Job{Name: "x", Lease: &fakeLease{}, Holder: "a",
		Do: func(context.Context, time.Time) error { return boom }}
	if ran, err := j.Tick(context.Background()); !ran || !errors.Is(err, boom) {
		t.Fatalf("run error = %v, %v", ran, err)
	}
}

func TestRunStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	j := &nightly.Job{Name: "x", Lease: &fakeLease{}, Holder: "a", Log: slog.New(slog.DiscardHandler),
		Do: func(context.Context, time.Time) error { return nil }}
	go func() { done <- j.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestPostgresLease(t *testing.T) {
	pg := testkit.StartPostgres(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, pg.URL("admin"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(ctx, nightly.SchemaSQL); err != nil {
		t.Fatal(err)
	}
	a := nightly.NewPostgresLease(admin, "retention")
	if ok, err := a.Hold(ctx, "a", time.Hour); !ok || err != nil {
		t.Fatalf("a: %v, %v", ok, err)
	}
	if ok, err := a.Hold(ctx, "b", time.Hour); ok || err != nil {
		t.Fatalf("b took a held lease: %v, %v", ok, err)
	}
	if ok, err := a.Hold(ctx, "a", time.Hour); !ok || err != nil {
		t.Fatalf("a could not renew: %v, %v", ok, err)
	}
	// Another job's lease is independent.
	if ok, err := nightly.NewPostgresLease(admin, "other").Hold(ctx, "b", time.Hour); !ok || err != nil {
		t.Fatalf("other job: %v, %v", ok, err)
	}
	// An expired lease goes to the next holder.
	if _, err := admin.Exec(ctx, `UPDATE job_leases SET expires_at = now() - interval '1 second' WHERE name = 'retention'`); err != nil {
		t.Fatal(err)
	}
	if ok, err := a.Hold(ctx, "b", time.Hour); !ok || err != nil {
		t.Fatalf("b after expiry: %v, %v", ok, err)
	}
}
