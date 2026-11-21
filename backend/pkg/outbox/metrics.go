package outbox

import (
	"context"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/raduolivas/dt/backend/pkg/otelx"
)

// meterName is the instrumentation scope of this package's metrics.
const meterName = "github.com/raduolivas/dt/backend/pkg/outbox"

// newMeter returns the meter for this package's metrics; tests replace it.
var newMeter = func() metric.Meter { return otelx.Meter(meterName) }

// Consumer outcomes for dt.consumer.messages and dt.consumer.process.duration.
const (
	outcomeOK    = "ok"
	outcomeRetry = "retry"
	outcomeDead  = "dead" // terminated: undecodable or ErrPermanent
)

// consumerMetrics are the per-message instruments of one Consume call.
// Instruments with the same name on one meter are the same instrument, so
// concurrent consumers add to the same series.
type consumerMetrics struct {
	duration metric.Float64Histogram
	messages metric.Int64Counter
}

func newConsumerMetrics(m metric.Meter) (*consumerMetrics, error) {
	d, err := m.Float64Histogram("dt.consumer.process.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Time to handle one JetStream message, including the inbox transaction."),
		metric.WithExplicitBucketBoundaries(0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5))
	if err != nil {
		return nil, err
	}
	n, err := m.Int64Counter("dt.consumer.messages",
		metric.WithDescription("JetStream messages handled, by outcome."))
	if err != nil {
		return nil, err
	}
	return &consumerMetrics{duration: d, messages: n}, nil
}

func (cm *consumerMetrics) count(ctx context.Context, stream, consumer, outcome string) {
	if cm == nil {
		return
	}
	cm.messages.Add(ctx, 1, metric.WithAttributes(consumerAttrs(stream, consumer, outcome)...))
}

func (cm *consumerMetrics) record(ctx context.Context, stream, consumer, outcome string, d time.Duration) {
	if cm == nil {
		return
	}
	cm.duration.Record(ctx, d.Seconds(), metric.WithAttributes(consumerAttrs(stream, consumer, outcome)...))
	cm.count(ctx, stream, consumer, outcome)
}

func consumerAttrs(stream, consumer, outcome string) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("stream", stream),
		attribute.String("consumer", consumer),
		attribute.String("outcome", outcome),
	}
}

// relayMetrics are the instruments of one Relay.
type relayMetrics struct {
	publish metric.Float64Histogram
	pending atomic.Int64
}

func newRelayMetrics(m metric.Meter) (*relayMetrics, error) {
	rm := &relayMetrics{}
	h, err := m.Float64Histogram("dt.outbox.publish.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Time to publish one outbox batch to JetStream."),
		metric.WithExplicitBucketBoundaries(0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5))
	if err != nil {
		return nil, err
	}
	rm.publish = h
	_, err = m.Int64ObservableGauge("dt.outbox.pending",
		metric.WithDescription("Outbox rows the relay selected and did not publish in its last batch."),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(rm.pending.Load())
			return nil
		}))
	if err != nil {
		return nil, err
	}
	return rm, nil
}

// setPending and observePublish are no-ops on a nil receiver (metrics could
// not be created, or Publish ran outside Run).
func (rm *relayMetrics) setPending(n int64) {
	if rm != nil {
		rm.pending.Store(n)
	}
}

func (rm *relayMetrics) observePublish(ctx context.Context, d time.Duration) {
	if rm != nil {
		rm.publish.Record(ctx, d.Seconds())
	}
}
