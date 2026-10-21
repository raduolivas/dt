package postgres_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/raduolivas/dt/backend/services/gateway/internal/adapters/outbound/postgres"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

func idemSetup(t *testing.T) (*pgxpool.Pool, *postgres.IdempotencyStore) {
	t.Helper()
	pool, _ := setup(t)
	return pool, postgres.NewIdempotencyStore(pool)
}

var (
	scopeA = app.IdempotencyScope{TenantID: "ten_a", Env: domain.EnvTest, Key: "key-1"}
	hash1  = []byte("hash-1-xxxxxxxxxxxxxxxxxxxxxxxxx")
	hash2  = []byte("hash-2-xxxxxxxxxxxxxxxxxxxxxxxxx")
	stored = app.StoredResponse{
		Status: 201,
		Header: map[string][]string{"Content-Type": {"application/json"}},
		Body:   []byte(`{"id":"x"}`),
	}
)

func begin(t *testing.T, s *postgres.IdempotencyStore, sc app.IdempotencyScope, h []byte, at time.Time) app.IdempotencyOutcome {
	t.Helper()
	o, err := s.Begin(context.Background(), sc, h, at)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	return o
}

func wantKind(t *testing.T, o app.IdempotencyOutcome, k app.OutcomeKind) {
	t.Helper()
	if o.Kind != k {
		t.Fatalf("outcome = %v, want %v", o.Kind, k)
	}
}

func TestIdempotencyBeginOutcomes(t *testing.T) {
	t.Parallel()
	_, s := idemSetup(t)
	ctx := context.Background()

	o := begin(t, s, scopeA, hash1, t0)
	wantKind(t, o, app.OutcomeStarted)
	if !o.Lease.Equal(t0.Add(app.IdempotencyLockTTL)) {
		t.Fatalf("lease = %v, want now + 60 s", o.Lease)
	}
	wantKind(t, begin(t, s, scopeA, hash1, t0.Add(time.Second)), app.OutcomeInProgress)
	wantKind(t, begin(t, s, scopeA, hash2, t0.Add(time.Second)), app.OutcomeMismatch)

	if err := s.Complete(ctx, scopeA, o.Lease, stored); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	r := begin(t, s, scopeA, hash1, t0.Add(2*time.Second))
	wantKind(t, r, app.OutcomeReplay)
	if !reflect.DeepEqual(r.Stored, stored) {
		t.Fatalf("stored = %+v, want %+v", r.Stored, stored)
	}
	wantKind(t, begin(t, s, scopeA, hash2, t0.Add(2*time.Second)), app.OutcomeMismatch)

	// The lease is spent once the record is completed.
	if err := s.Complete(ctx, scopeA, o.Lease, stored); !errors.Is(err, app.ErrIdempotencyLeaseLost) {
		t.Fatalf("second Complete = %v, want ErrIdempotencyLeaseLost", err)
	}
	if err := s.Abandon(ctx, scopeA, o.Lease); !errors.Is(err, app.ErrIdempotencyLeaseLost) {
		t.Fatalf("Abandon after Complete = %v, want ErrIdempotencyLeaseLost", err)
	}
	wantKind(t, begin(t, s, scopeA, hash1, t0.Add(3*time.Second)), app.OutcomeReplay)
}

func TestIdempotencyEmptyStoredResponse(t *testing.T) {
	t.Parallel()
	_, s := idemSetup(t)
	o := begin(t, s, scopeA, hash1, t0)
	if err := s.Complete(context.Background(), scopeA, o.Lease, app.StoredResponse{Status: 204}); err != nil {
		t.Fatal(err)
	}
	r := begin(t, s, scopeA, hash1, t0)
	wantKind(t, r, app.OutcomeReplay)
	if r.Stored.Status != 204 || len(r.Stored.Body) != 0 || len(r.Stored.Header) != 0 {
		t.Fatalf("stored = %+v", r.Stored)
	}
}

func TestIdempotencyAbandonFreesTheKey(t *testing.T) {
	t.Parallel()
	_, s := idemSetup(t)
	ctx := context.Background()
	o := begin(t, s, scopeA, hash1, t0)
	if err := s.Abandon(ctx, scopeA, o.Lease.Add(time.Microsecond)); !errors.Is(err, app.ErrIdempotencyLeaseLost) {
		t.Fatalf("Abandon with a foreign lease = %v", err)
	}
	wantKind(t, begin(t, s, scopeA, hash1, t0), app.OutcomeInProgress)
	if err := s.Abandon(ctx, scopeA, o.Lease); err != nil {
		t.Fatalf("Abandon: %v", err)
	}
	// A retry, even with another body, starts afresh.
	wantKind(t, begin(t, s, scopeA, hash2, t0.Add(time.Second)), app.OutcomeStarted)
}

func TestStaleLockIsTakenOver(t *testing.T) {
	t.Parallel()
	_, s := idemSetup(t)
	ctx := context.Background()
	first := begin(t, s, scopeA, hash1, t0)

	// Still locked at exactly locked_until - 1 µs.
	wantKind(t, begin(t, s, scopeA, hash1, first.Lease.Add(-time.Microsecond)), app.OutcomeInProgress)

	later := t0.Add(app.IdempotencyLockTTL + time.Second)
	second := begin(t, s, scopeA, hash1, later)
	wantKind(t, second, app.OutcomeStarted)
	if !second.Lease.Equal(later.Add(app.IdempotencyLockTTL)) {
		t.Fatalf("takeover lease = %v", second.Lease)
	}
	wantKind(t, begin(t, s, scopeA, hash1, later), app.OutcomeInProgress)

	// The crashed (or slow) first owner can no longer touch the record.
	if err := s.Complete(ctx, scopeA, first.Lease, stored); !errors.Is(err, app.ErrIdempotencyLeaseLost) {
		t.Fatalf("stale Complete = %v", err)
	}
	if err := s.Abandon(ctx, scopeA, first.Lease); !errors.Is(err, app.ErrIdempotencyLeaseLost) {
		t.Fatalf("stale Abandon = %v", err)
	}
	if err := s.Complete(ctx, scopeA, second.Lease, stored); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	wantKind(t, begin(t, s, scopeA, hash1, later), app.OutcomeReplay)
}

func TestIdempotencyStaleLockTakeoverWithAnotherBody(t *testing.T) {
	t.Parallel()
	_, s := idemSetup(t)
	begin(t, s, scopeA, hash1, t0)
	later := t0.Add(app.IdempotencyLockTTL)
	o := begin(t, s, scopeA, hash2, later)
	wantKind(t, o, app.OutcomeStarted)
	if err := s.Complete(context.Background(), scopeA, o.Lease, stored); err != nil {
		t.Fatal(err)
	}
	wantKind(t, begin(t, s, scopeA, hash2, later), app.OutcomeReplay)
	wantKind(t, begin(t, s, scopeA, hash1, later), app.OutcomeMismatch)
}

func TestIdempotencyExpiredRecordIsReused(t *testing.T) {
	t.Parallel()
	_, s := idemSetup(t)
	old := t0.Add(-app.IdempotencyTTL - time.Second)
	o := begin(t, s, scopeA, hash1, old)
	if err := s.Complete(context.Background(), scopeA, o.Lease, stored); err != nil {
		t.Fatal(err)
	}
	// Not swept yet, but past expires_at: the key is free again.
	wantKind(t, begin(t, s, scopeA, hash2, t0), app.OutcomeStarted)
}

func TestIdempotencyKeysScoped(t *testing.T) {
	t.Parallel()
	_, s := idemSetup(t)
	for _, sc := range []app.IdempotencyScope{
		scopeA,
		{TenantID: "ten_b", Env: domain.EnvTest, Key: "key-1"},
		{TenantID: "ten_a", Env: domain.EnvLive, Key: "key-1"},
		{TenantID: "ten_a", Env: domain.EnvTest, Key: "key-2"},
	} {
		wantKind(t, begin(t, s, sc, hash1, t0), app.OutcomeStarted)
	}
}

func TestIdempotencyConcurrentBegin(t *testing.T) {
	t.Parallel()
	_, s := idemSetup(t)
	const n = 10
	var wg sync.WaitGroup
	kinds := make([]app.OutcomeKind, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			o, err := s.Begin(context.Background(), scopeA, hash1, t0)
			kinds[i], errs[i] = o.Kind, err
		})
	}
	close(start)
	wg.Wait()
	started := 0
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("Begin %d: %v", i, errs[i])
		}
		switch kinds[i] {
		case app.OutcomeStarted:
			started++
		case app.OutcomeInProgress:
		default:
			t.Fatalf("Begin %d = %v", i, kinds[i])
		}
	}
	if started != 1 {
		t.Fatalf("%d Begins started, want exactly 1", started)
	}
}

func count(t *testing.T, pool *pgxpool.Pool, where string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM idempotency_records WHERE "+where).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSweeperDeletesExpired(t *testing.T) {
	t.Parallel()
	pool, s := idemSetup(t)
	ctx := context.Background()

	// 2500 expired rows (more than two batches) and 3 live ones.
	if _, err := pool.Exec(ctx, `
		INSERT INTO idempotency_records (tenant_id, environment, idem_key, request_hash, state, expires_at)
		SELECT 'ten_old', 'test', 'k' || g, '\x00', 'completed', $1
		FROM generate_series(1, 2500) g`, t0.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"a", "b", "c"} {
		begin(t, s, app.IdempotencyScope{TenantID: "ten_new", Env: domain.EnvLive, Key: k}, hash1, t0)
	}

	n, err := s.DeleteExpired(ctx, t0, 1000)
	if err != nil || n != 1000 {
		t.Fatalf("DeleteExpired = %d, %v; want one batch of 1000", n, err)
	}

	sw := &app.IdempotencySweeper{Store: s, Clock: fixedClock{t0}, Interval: time.Hour}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- sw.Run(runCtx) }()
	deadline := time.Now().Add(10 * time.Second)
	for count(t, pool, "tenant_id = 'ten_old'") > 0 {
		if time.Now().After(deadline) {
			t.Fatal("sweeper left expired rows")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if got := count(t, pool, "tenant_id = 'ten_new'"); got != 3 {
		t.Fatalf("live rows left = %d, want 3", got)
	}
}

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }
