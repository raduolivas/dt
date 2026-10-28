package consumer

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Lag watching: the key consumer applies one event at a time, so a stuck or
// slow handler shows up as a growing backlog long before a revoked key still
// authenticates.
const (
	// LagPollInterval is how often the consumer's backlog is read.
	LagPollInterval = 10 * time.Second
	// LagWarnPending and LagWarnAfter: warn when more than LagWarnPending
	// messages are pending for longer than LagWarnAfter.
	LagWarnPending = 100
	LagWarnAfter   = 30 * time.Second

	// LagGauge is the metric name; its "consumer" attribute is the durable name.
	LagGauge = "dt_consumer_pending_messages"
)

// LagMonitor reports the consumer's backlog as the LagGauge gauge and logs a
// warning while it stays high.
type LagMonitor struct {
	log      *slog.Logger
	consumer string
	pending  atomic.Int64
	// highSince is when pending first exceeded LagWarnPending in the current
	// stretch; zero when it is not high. Touched only by Record's caller.
	highSince time.Time
	warned    bool
}

// NewLagMonitor registers the gauge on meter for the durable consumer name.
func NewLagMonitor(meter metric.Meter, log *slog.Logger, name string) (*LagMonitor, error) {
	m := &LagMonitor{log: log, consumer: name}
	_, err := meter.Int64ObservableGauge(LagGauge,
		metric.WithDescription("Messages not yet applied by a durable JetStream consumer."),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(m.pending.Load(), metric.WithAttributes(attribute.String("consumer", m.consumer)))
			return nil
		}))
	if err != nil {
		return nil, err
	}
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
		m.log.WarnContext(ctx, "consumer_lagging", slog.String("consumer", m.consumer),
			slog.Int64("pending", pending), slog.Duration("for", now.Sub(m.highSince)))
	}
}

// Run polls the consumer's info every LagPollInterval until ctx is done. A
// failed poll is logged and retried; it never stops the service.
func (m *LagMonitor) Run(ctx context.Context, js jetstream.JetStream, stream string) error {
	t := time.NewTicker(LagPollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		cons, err := js.Consumer(ctx, stream, m.consumer)
		var info *jetstream.ConsumerInfo
		if err == nil {
			info, err = cons.Info(ctx)
		}
		if err != nil {
			if ctx.Err() == nil {
				m.log.WarnContext(ctx, "consumer_lag_poll_failed", slog.String("consumer", m.consumer))
			}
			continue
		}
		m.Record(ctx, int64(info.NumPending)+int64(info.NumAckPending), time.Now()) //nolint:gosec // counts fit int64
	}
}
