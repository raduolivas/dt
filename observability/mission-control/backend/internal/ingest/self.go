// Package ingest brings DT's telemetry into Mission Control: read-only NATS
// consumers, the OTLP/HTTP receiver, health and consumer pollers, and the
// self-health counters that say when Mission Control itself is behind.
package ingest

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
)

// SourceRedacted is the mc_self row that holds the redaction counter (ruling R9).
const SourceRedacted = "normalize"

// selfInterval is how often counters are flushed to mc_self and the heartbeat is written.
const selfInterval = 5 * time.Second

// Counts are the cumulative self-health values of one source since start.
type Counts struct {
	Drops, Redacted, StorageErrors int64
	LagMS                          int64
	Unavailable                    bool
}

// SourceMetrics is the mc_self row that holds the metrics-source flag.
const SourceMetrics = "metrics"

// MetricsSourceUnavailable sets metrics_source_unavailable (metrics.Flag).
func (s *Self) MetricsSourceUnavailable(v bool) {
	s.with(SourceMetrics, func(c *Counts) { c.Unavailable = v })
}

// Self holds Mission Control's own health counters per source. All methods are
// safe for concurrent use and are no-ops on a nil *Self.
type Self struct {
	st  *store.Store
	log *slog.Logger
	mu  sync.Mutex
	src map[string]*Counts
}

// NewSelf returns counters that flush to st (st may be nil in tests).
func NewSelf(st *store.Store, log *slog.Logger) *Self {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Self{st: st, log: log, src: map[string]*Counts{}}
}

func (s *Self) with(source string, f func(c *Counts)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.src[source]
	if c == nil {
		c = &Counts{}
		s.src[source] = c
	}
	f(c)
}

// Drop counts n events dropped from source.
func (s *Self) Drop(source string, n int) { s.with(source, func(c *Counts) { c.Drops += int64(n) }) }

// Redacted counts n attributes the deny-list removed. It is the Normalizer's OnRedacted hook.
func (s *Self) Redacted(n int) {
	s.with(SourceRedacted, func(c *Counts) { c.Redacted += int64(n) })
}

// StorageError counts one failed write for source.
func (s *Self) StorageError(source string) { s.with(source, func(c *Counts) { c.StorageErrors++ }) }

// Lag records the latest ingest lag of source.
func (s *Self) Lag(source string, d time.Duration) {
	if d < 0 {
		d = 0
	}
	s.with(source, func(c *Counts) { c.LagMS = d.Milliseconds() })
}

// Snapshot copies the current counters.
func (s *Self) Snapshot() map[string]Counts {
	out := map[string]Counts{}
	if s == nil {
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, c := range s.src {
		out[k] = *c
	}
	return out
}

// Flush writes every source's counters to mc_self.
func (s *Self) Flush(ctx context.Context, now time.Time) {
	if s == nil || s.st == nil {
		return
	}
	for src, c := range s.Snapshot() {
		err := s.st.SaveSelf(ctx, store.SelfRow{Source: src, LastSeen: now, LagMS: c.LagMS,
			Drops: c.Drops, Redacted: c.Redacted, StorageErrors: c.StorageErrors, Unavailable: c.Unavailable})
		if err != nil {
			s.log.Warn("self-health flush failed", "source", src, "error", err.Error())
		}
	}
}

// Run records the startup (closing the pre-start downtime window), then every
// 5 s writes the heartbeat and flushes the counters, until ctx ends.
func (s *Self) Run(ctx context.Context) error {
	if err := s.st.RecordStartup(ctx, time.Now().UTC()); err != nil {
		return err
	}
	beat := func(now time.Time) {
		if err := s.st.Heartbeat(ctx, now.UTC()); err != nil {
			s.log.Warn("heartbeat failed", "error", err.Error())
		}
		s.Flush(ctx, now.UTC())
	}
	beat(time.Now())
	t := time.NewTicker(selfInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			// Final flush so drops counted during shutdown are not lost.
			fc, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			s.Flush(fc, time.Now().UTC())
			cancel()
			return nil
		case now := <-t.C:
			beat(now)
		}
	}
}
