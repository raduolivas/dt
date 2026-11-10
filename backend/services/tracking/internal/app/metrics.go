package app

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/raduolivas/dt/backend/pkg/otelx"
)

// meterName is the instrumentation scope of this package's metrics.
const meterName = "github.com/raduolivas/dt/backend/services/tracking/internal/app"

// OutcomeAccepted is the outcome label of a fix some delivery kept. Every
// other outcome is one of the Reason* constants or a domain verdict reason
// (invalid, future, speed, jump): a fixed set, never free text.
const OutcomeAccepted = "accepted"

// Fix sources, the only values of the source label. The rpc layer admits
// exactly these; anything else maps to sourceOther.
const sourceOther = "other"

var knownSources = map[string]bool{"relay": true, "sdk": true, "link": true, "simulator": true}

func sourceLabel(source string) string {
	if knownSources[source] {
		return source
	}
	return sourceOther
}

var (
	metricsMu sync.Mutex
	fixesCtr  metric.Int64Counter
	// newMeter returns the meter for this package's metrics; tests replace it.
	newMeter = func() metric.Meter { return otelx.Meter(meterName) }
)

// countFixes adds n fixes with the given outcome. The counter is created
// once; without a provider it is a no-op, so recording never fails or
// blocks an ingest.
func countFixes(ctx context.Context, source, outcome string, n int) {
	if n <= 0 {
		return
	}
	metricsMu.Lock()
	if fixesCtr == nil {
		// Creation only fails on an invalid name; the returned no-op
		// instrument keeps a telemetry fault out of the ingest path.
		fixesCtr, _ = newMeter().Int64Counter("dt.tracking.fixes",
			metric.WithDescription("Location fixes judged by ingest, by source and outcome."))
	}
	c := fixesCtr
	metricsMu.Unlock()
	c.Add(ctx, int64(n), metric.WithAttributes(
		attribute.String("source", sourceLabel(source)), attribute.String("outcome", outcome)))
}
