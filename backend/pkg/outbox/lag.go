package outbox

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Lag watching: every Consume starts one. A consumer applies one event at a time, so a stuck or
// slow handler shows up as a growing backlog long before a revoked key still
// authenticates.
const (
	// LagWarnPending and LagWarnAfter: warn when more than LagWarnPending
	// messages are pending for longer than LagWarnAfter.
	LagWarnPending = 100
	LagWarnAfter   = 30 * time.Second

	// LagGauge is the metric name; its "stream" and "consumer" attributes are
	// the stream and the durable name.
	LagGauge = "dt_consumer_pending_messages"
)

// LagPollInterval is how often a consumer's backlog is read. A variable so
// tests can shorten it; set it before starting Consume.
var LagPollInterval = 10 * time.Second

// LagMonitor reports the consumer's backlog as the LagGauge gauge and logs a
// warning while it stays high.
type LagMonitor struct {
	log      *slog.Logger
	stream   string
	consumer string
	reg      *lagRegistry
	pending  atomic.Int64
	// highSince is when pending first exceeded LagWarnPending in the current
	// stretch; zero when it is not high. Touched only by Record's caller.
	highSince time.Time
	warned    bool
}

// lagRegistry is the set of monitors that share one meter's gauge. The SDK
// keeps only the first callback registered on a same-named instrument, so one
// callback per meter reports every monitor's point.
type lagRegistry struct {
	mu       sync.Mutex
	monitors map[*LagMonitor]struct{}
}

var (
	lagRegistries   = map[metric.Meter]*lagRegistry{}
	lagRegistriesMu sync.Mutex
)

func registryFor(meter metric.Meter) (*lagRegistry, error) {
	lagRegistriesMu.Lock()
	defer lagRegistriesMu.Unlock()
	if r, ok := lagRegistries[meter]; ok {
		return r, nil
	}
	r := &lagRegistry{monitors: map[*LagMonitor]struct{}{}}
	_, err := meter.Int64ObservableGauge(LagGauge,
		metric.WithDescription("Messages not yet applied by a durable JetStream consumer."),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			for m := range r.monitors {
				o.Observe(m.pending.Load(), metric.WithAttributes(
					attribute.String("stream", m.stream), attribute.String("consumer", m.consumer)))
			}
			return nil
		}))
	if err != nil {
		return nil, err
	}
	lagRegistries[meter] = r
	return r, nil
}

// NewLagMonitor reports the durable consumer on stream through the LagGauge
// gauge of meter. Monitors sharing a meter share the instrument; each reports
// its own (stream, consumer) point until its Run returns.
func NewLagMonitor(meter metric.Meter, log *slog.Logger, stream, consumer string) (*LagMonitor, error) {
	reg, err := registryFor(meter)
	if err != nil {
		return nil, err
	}
	m := &LagMonitor{log: log, stream: stream, consumer: consumer, reg: reg}
	reg.mu.Lock()
	reg.monitors[m] = struct{}{}
	reg.mu.Unlock()
	return m, nil
}

// Record stores the current backlog and warns once per high stretch.
func (m *LagMonitor) Record(ctx context.Context, pending int64, now time.Time) {
	m.pending.Store(pending)
	if pending <= LagWarnPending {
		m.highSince, m.warned = time.Time{}, false
		return
	}
	if m.highSince.IsZero() {
		m.highSince = now
	}
	if !m.warned && now.Sub(m.highSince) > LagWarnAfter {
		m.warned = true
		m.log.WarnContext(ctx, "consumer_lagging", slog.String("consumer", m.consumer), slog.String("stream", m.stream),
			slog.Int64("pending", pending), slog.Duration("for", now.Sub(m.highSince)))
	}
}

// Run polls the consumer's info every LagPollInterval until ctx is done. A
// failed poll is logged and retried; it never stops the service.
func (m *LagMonitor) Run(ctx context.Context, js jetstream.JetStream) error {
	defer func() {
		m.reg.mu.Lock()
		delete(m.reg.monitors, m)
		m.reg.mu.Unlock()
	}()
	t := time.NewTicker(LagPollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		cons, err := js.Consumer(ctx, m.stream, m.consumer)
		var info *jetstream.ConsumerInfo
		if err == nil {
			info, err = cons.Info(ctx)
		}
		if err != nil {
			if ctx.Err() == nil {
				m.log.WarnContext(ctx, "consumer_lag_poll_failed", slog.String("consumer", m.consumer),
					slog.String("stream", m.stream))
			}
			continue
		}
		m.Record(ctx, int64(info.NumPending)+int64(info.NumAckPending), time.Now()) //nolint:gosec // counts fit int64
	}
}
