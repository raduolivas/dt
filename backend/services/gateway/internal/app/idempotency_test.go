package app_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
)

func TestValidIdempotencyKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		key  string
		want bool
	}{
		{"a", true},
		{"order-123_ABC.x", true},
		{"with space", true},
		{" ~!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}", true},
		{strings.Repeat("k", 255), true},
		{"", false},
		{strings.Repeat("k", 256), false},
		{"line\nbreak", false},
		{"tab\there", false},
		{"nul\x00", false},
		{"del\x7f", false},
		{"café", false},
	}
	for _, c := range cases {
		if got := app.ValidIdempotencyKey(c.key); got != c.want {
			t.Errorf("ValidIdempotencyKey(%q) = %v, want %v", c.key, got, c.want)
		}
	}
}

func TestIdempotencyRequestHash(t *testing.T) {
	t.Parallel()
	body := []byte(`{"a":1}`)
	want := sha256.Sum256([]byte("POST\n/v1/things\n\n" + `{"a":1}`))
	got := app.IdempotencyRequestHash("POST", "/v1/things", "", body)
	if !bytes.Equal(got, want[:]) {
		t.Fatalf("hash = %x, want %x", got, want)
	}
	for name, other := range map[string][]byte{
		"method": app.IdempotencyRequestHash("PUT", "/v1/things", "", body),
		"path":   app.IdempotencyRequestHash("POST", "/v1/other", "", body),
		"body":   app.IdempotencyRequestHash("POST", "/v1/things", "", []byte(`{"a":2}`)),
	} {
		if bytes.Equal(got, other) {
			t.Errorf("hash ignores the %s", name)
		}
	}
}

func TestRequestHashIncludesQuery(t *testing.T) {
	t.Parallel()
	body := []byte(`{"a":1}`)
	a1 := app.IdempotencyRequestHash("POST", "/v1/things", "a=1", body)
	a2 := app.IdempotencyRequestHash("POST", "/v1/things", "a=2", body)
	if bytes.Equal(a1, a2) {
		t.Error("hash ignores the query")
	}
	if !bytes.Equal(app.IdempotencyRequestHash("POST", "/v1/things", "", body),
		app.IdempotencyRequestHash("POST", "/v1/things", "", body)) {
		t.Error("equal requests hash differently")
	}
}

func TestIdempotencyConstants(t *testing.T) {
	t.Parallel()
	if app.IdempotencyTTL != 24*time.Hour || app.IdempotencyLockTTL != 60*time.Second ||
		app.IdempotencySweepInterval != 10*time.Minute || app.IdempotencySweepBatch != 1000 ||
		app.MaxIdempotentResponseBytes != 64<<10 {
		t.Fatal("idempotency constants drifted from the spec")
	}
}

// deleter is a fake expired-record store: each call deletes up to limit of
// the remaining rows, or fails while err is set.
type deleter struct {
	mu      sync.Mutex
	left    int
	err     error
	calls   []int // limit per call
	befores []time.Time
	called  chan struct{}
}

func (d *deleter) DeleteExpired(_ context.Context, before time.Time, limit int) (int64, error) {
	d.mu.Lock()
	defer func() {
		d.mu.Unlock()
		select {
		case d.called <- struct{}{}:
		default:
		}
	}()
	d.calls = append(d.calls, limit)
	d.befores = append(d.befores, before)
	if d.err != nil {
		return 0, d.err
	}
	n := min(d.left, limit)
	d.left -= n
	return int64(n), nil
}

func TestIdempotencySweeperDeletesInBatchesUntilDone(t *testing.T) {
	t.Parallel()
	d := &deleter{left: 2003, called: make(chan struct{}, 16)}
	s := &app.IdempotencySweeper{
		Store: d, Clock: fixedClock{now}, Interval: time.Hour,
		Log: slog.New(slog.DiscardHandler),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	for range 3 {
		select {
		case <-d.called:
		case <-time.After(5 * time.Second):
			t.Fatal("sweeper did not sweep at start")
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v, want nil on cancel", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop on cancel")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.calls) != 3 || d.left != 0 {
		t.Fatalf("calls = %v, left = %d; want 3 batches until empty", d.calls, d.left)
	}
	for i, l := range d.calls {
		if l != app.IdempotencySweepBatch || !d.befores[i].Equal(now) {
			t.Fatalf("call %d: limit %d before %v", i, l, d.befores[i])
		}
	}
}

func TestIdempotencySweeperSurvivesErrors(t *testing.T) {
	t.Parallel()
	logs := &bytes.Buffer{}
	d := &deleter{err: errors.New("db down"), called: make(chan struct{}, 16)}
	s := &app.IdempotencySweeper{
		Store: d, Clock: fixedClock{now}, Interval: 5 * time.Millisecond,
		Log: slog.New(slog.NewTextHandler(&lockedWriter{w: logs}, nil)),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	for range 3 { // keeps ticking after failures
		select {
		case <-d.called:
		case <-time.After(5 * time.Second):
			t.Fatal("sweeper stopped after an error")
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
	if !strings.Contains(logs.String(), "idempotency_sweep_failed") {
		t.Fatalf("no failure log: %s", logs.String())
	}
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
