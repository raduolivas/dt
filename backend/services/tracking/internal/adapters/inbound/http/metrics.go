package httpapi

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/raduolivas/dt/backend/pkg/otelx"
)

// meterName is the instrumentation scope of this package's metrics.
const meterName = "github.com/raduolivas/dt/backend/services/tracking/internal/adapters/inbound/http"

var (
	metricsMu sync.Mutex
	cur       metric.Int64UpDownCounter
	// newMeter returns the meter for this package's metrics; tests replace it.
	newMeter = func() metric.Meter { return otelx.Meter(meterName) }
)

// countViewer adds delta to the open viewer streams of a transport ("ws" or
// "sse"). Without a provider it is a no-op.
func countViewer(ctx context.Context, transport string, delta int64) {
	metricsMu.Lock()
	if cur == nil {
		// Creation only fails on an invalid name; the no-op fallback keeps a
		// telemetry fault out of the stream path.
		cur, _ = newMeter().Int64UpDownCounter("dt.tracking.viewer.connections",
			metric.WithDescription("Open viewer streams, by transport."))
	}
	c := cur
	metricsMu.Unlock()
	c.Add(ctx, delta, metric.WithAttributes(attribute.String("transport", transport)))
}
