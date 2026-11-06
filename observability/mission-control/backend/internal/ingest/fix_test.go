package ingest_test

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/ingest"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
)

func evs(n int) []store.Event {
	now := time.Now().UTC()
	out := make([]store.Event, n)
	for i := range out {
		out[i] = store.Event{ObservedAt: now, OccurredAt: now, Kind: "health", Source: "x", Origin: "live"}
	}
	return out
}

func TestWriterBatchesMoreThanOneBatch(t *testing.T) {
	st := newStore(t)
	self := ingest.NewSelf(st, nil)
	w := ingest.NewWriter(st, self, 5000)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); w.Run(ctx) }()
	w.Add("t", evs(1300)...)
	require.Eventually(t, func() bool { n, _ := st.CountEvents(ctx); return n == 1300 }, 10*time.Second, 50*time.Millisecond)
	cancel()
	<-done
	require.Zero(t, self.Snapshot()["t"].Drops)
}

func TestWriterFlushesOnShutdownAndDropsAfterwards(t *testing.T) {
	st := newStore(t)
	self := ingest.NewSelf(st, nil)
	w := ingest.NewWriter(st, self, 100)
	w.Add("t", evs(10)...) // queued before Run even starts
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already shutting down
	w.Run(ctx)
	n, err := st.CountEvents(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 10, n, "everything accepted before shutdown is flushed")

	w.Add("t", evs(3)...) // the writer is closed: lost events are counted, not silent
	require.EqualValues(t, 3, self.Snapshot()["t"].Drops)
	require.Zero(t, w.Len())
}

func TestWriterCountsStorageErrors(t *testing.T) {
	st := newStore(t)
	self := ingest.NewSelf(st, nil)
	w := ingest.NewWriter(st, self, 100)
	st.Close() // every insert now fails
	w.Add("otlp", evs(2)...)
	w.Add("health", evs(1)...)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.Run(ctx)
	snap := self.Snapshot()
	require.EqualValues(t, 1, snap["otlp"].StorageErrors)
	require.EqualValues(t, 1, snap["health"].StorageErrors)
}

func TestSelfFinalFlushOnExit(t *testing.T) {
	st := newStore(t)
	self := ingest.NewSelf(st, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = self.Run(ctx) }()
	require.Eventually(t, func() bool { rows, _ := st.SelfRows(context.Background()); return len(rows) > 0 }, 5*time.Second, 20*time.Millisecond)
	self.Drop("late", 4) // after the last periodic flush
	cancel()
	<-done
	rows, err := st.SelfRows(context.Background())
	require.NoError(t, err)
	var got int64
	for _, r := range rows {
		if r.Source == "late" {
			got = r.Drops
		}
	}
	require.EqualValues(t, 4, got)
}

// proxy forwards a fixed local address to target, once started.
type proxy struct {
	addr   string
	target string
	ln     net.Listener
	wg     sync.WaitGroup
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	a := l.Addr().String()
	require.NoError(t, l.Close())
	return a
}

func (p *proxy) start(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("tcp", p.addr)
	require.NoError(t, err)
	p.ln = ln
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			up, err := net.Dial("tcp", p.target)
			if err != nil {
				_ = c.Close()
				continue
			}
			go func() { _, _ = io.Copy(up, c); _ = up.Close() }()
			go func() { _, _ = io.Copy(c, up); _ = c.Close() }()
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
}

func TestNATSDownAtStartThenUp(t *testing.T) {
	st := newStore(t)
	realURL, js := newNATS(t)
	publish(t, js, "delivery.status_changed", "del_late")

	addr := freeAddr(t) // nothing listens here yet: NATS is "down" at start
	p := &proxy{addr: addr, target: realURL[len("nats://"):]}
	self := ingest.NewSelf(st, nil)
	var mu sync.Mutex
	var states []string
	emit := func(outcome, reason string) {
		mu.Lock()
		defer mu.Unlock()
		states = append(states, outcome+"/"+reason)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ingest.RunNATSLoop(ctx, "nats://"+addr, st, newNorm(self),
			ingest.NATSOptions{Self: self, RetryEvery: 100 * time.Millisecond}, emit)
	}()
	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(states) > 0 }, 5*time.Second, 20*time.Millisecond)
	mu.Lock()
	require.Equal(t, "failed/nats_unreachable", states[0])
	mu.Unlock()
	n, _ := st.CountEvents(ctx)
	require.Zero(t, n)

	p.start(t) // NATS comes up
	require.Eventually(t, func() bool { n, _ := st.CountEvents(ctx); return n == 1 }, 15*time.Second, 100*time.Millisecond)
	mu.Lock()
	require.Contains(t, states, "ok/")
	mu.Unlock()
	cancel()
	<-done
}
