package osrm

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/raduolivas/dt/backend/pkg/otelx"
)

const (
	meterName  = "github.com/raduolivas/dt/backend/services/tracking/internal/adapters/outbound/osrm"
	tracerName = meterName
)

// Outcomes, the only values of the outcome label.
const (
	outcomeOK      = "ok"
	outcomeError   = "error"
	outcomeTimeout = "timeout"
)

type routingMetrics struct {
	duration metric.Float64Histogram
	errors   metric.Int64Counter
}

var (
	metricsMu sync.Mutex
	cur       *routingMetrics
	// newMeter returns the meter for this package's metrics; tests replace it.
	newMeter = func() metric.Meter { return otelx.Meter(meterName) }
)

func instruments() *routingMetrics {
	metricsMu.Lock()
	defer metricsMu.Unlock()
	if cur != nil {
		return cur
	}
	m := newMeter()
	// Creation only fails on an invalid name; the no-op fallbacks keep a
	// telemetry fault out of the routing path.
	d, _ := m.Float64Histogram("dt.routing.duration", metric.WithUnit("s"),
		metric.WithDescription("Duration of OSRM route and match calls, by operation and outcome."))
	e, _ := m.Int64Counter("dt.routing.errors",
		metric.WithDescription("OSRM route and match calls that did not succeed, by operation and outcome."))
	cur = &routingMetrics{duration: d, errors: e}
	return cur
}

// outcomeOf classifies a call's error: a deadline or client timeout is
// "timeout", anything else that failed is "error".
func outcomeOf(err error) string {
	var ne net.Error
	switch {
	case err == nil:
		return outcomeOK
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return outcomeTimeout
	default:
		return outcomeError
	}
}

// observe starts the client span of one OSRM call and returns the function
// that ends it and records the metrics. The span carries the operation and
// profile only: OSRM URLs hold coordinates, and so do transport errors, so
// neither the URL nor the error text ever reaches a span. Only route and
// match are measured; nearest gets a span alone.
func observe(ctx context.Context, operation, profile string) (context.Context, func(error)) {
	start := time.Now()
	ctx, span := otel.Tracer(tracerName).Start(ctx, "osrm."+operation, trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("osrm.operation", operation), attribute.String("osrm.profile", profile)))
	return ctx, func(err error) {
		outcome := outcomeOf(err)
		span.SetAttributes(attribute.String("osrm.outcome", outcome))
		if err != nil {
			span.SetStatus(codes.Error, outcome)
		}
		span.End()
		if operation != "route" && operation != "match" {
			return
		}
		ins := instruments()
		attrs := metric.WithAttributes(attribute.String("operation", operation), attribute.String("outcome", outcome))
		ins.duration.Record(ctx, time.Since(start).Seconds(), attrs)
		if err != nil {
			ins.errors.Add(ctx, 1, attrs)
		}
	}
}
