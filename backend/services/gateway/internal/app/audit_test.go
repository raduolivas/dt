package app_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
)

// fakeAuditDeleter records each DeleteAuditBefore call and answers from n.
type fakeAuditDeleter struct {
	mu      sync.Mutex
	befores []time.Time
	limits  []int
	n       []int64 // answers, in order; then 0
	err     error
	cancel  context.CancelFunc // called after the first sweep, if set
}

func (f *fakeAuditDeleter) DeleteAuditBefore(_ context.Context, before time.Time, limit int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.befores = append(f.befores, before)
	f.limits = append(f.limits, limit)
	if f.err != nil {
		if f.cancel != nil {
			f.cancel()
		}
		return 0, f.err
	}
	var n int64
	if len(f.n) > 0 {
		n, f.n = f.n[0], f.n[1:]
	}
	if n < int64(limit) && f.cancel != nil {
		f.cancel()
	}
	return n, nil
}

type clockAt time.Time

func (c clockAt) Now() time.Time { return time.Time(c) }

func TestAuditRetentionConstants(t *testing.T) {
	if app.AuditRetentionPeriod != 365*24*time.Hour {
		t.Fatalf("AuditRetentionPeriod = %v, want 365 days", app.AuditRetentionPeriod)
	}
	if app.AuditRetentionInterval != 24*time.Hour {
		t.Fatalf("AuditRetentionInterval = %v, want daily", app.AuditRetentionInterval)
	}
}

func TestAuditRetentionDeletesInBatchesBeforeCutoff(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &fakeAuditDeleter{n: []int64{10, 10, 3}, cancel: cancel}
	r := &app.AuditRetention{Store: f, Clock: clockAt(now), Batch: 10}

	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v on cancel, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.befores) != 3 {
		t.Fatalf("DeleteAuditBefore called %d times, want 3 (two full batches, one short)", len(f.befores))
	}
	want := now.Add(-365 * 24 * time.Hour)
	for i, b := range f.befores {
		if !b.Equal(want) || f.limits[i] != 10 {
			t.Fatalf("call %d: before %v limit %d, want %v limit 10", i, b, f.limits[i], want)
		}
	}
}

func TestAuditRetentionSurvivesErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &fakeAuditDeleter{err: errors.New("db down"), cancel: cancel}
	r := &app.AuditRetention{Store: f}
	if err := r.Run(ctx); err != nil {
		t.Fatalf("Run = %v, want nil: a failed sweep must not stop the service", err)
	}
	if len(f.limits) != 1 || f.limits[0] != app.AuditRetentionBatch {
		t.Fatalf("limits = %v, want one call with the default batch %d", f.limits, app.AuditRetentionBatch)
	}
}
