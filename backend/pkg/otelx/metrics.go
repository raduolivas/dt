package otelx

import (
	"context"
	"errors"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// HTTPBuckets are the explicit histogram bounds (seconds) for HTTP and RPC
// request durations, 5 ms to 5 s.
var HTTPBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.2, 0.3, 0.5, 1, 2.5, 5}

// FixToViewerBuckets are the bounds (seconds) for dt.tracking.fix_to_viewer.duration,
// 50 ms to 5 s.
var FixToViewerBuckets = []float64{0.05, 0.1, 0.25, 0.5, 0.75, 1, 1.5, 2.5, 5}

// Views returns the metric views that apply the SLO bucket boundaries.
func Views() []sdkmetric.View {
	hist := func(name string, bounds []float64) sdkmetric.View {
		return sdkmetric.NewView(
			sdkmetric.Instrument{Name: name, Kind: sdkmetric.InstrumentKindHistogram},
			sdkmetric.Stream{Aggregation: sdkmetric.AggregationExplicitBucketHistogram{Boundaries: bounds}},
		)
	}
	return []sdkmetric.View{
		hist("http.server.request.duration", HTTPBuckets),
		hist("rpc.server.duration", HTTPBuckets),
		hist("rpc.client.duration", HTTPBuckets),
		hist("dt.tracking.fix_to_viewer.duration", FixToViewerBuckets),
	}
}

// Meter returns a meter from the global MeterProvider installed by Init.
func Meter(name string) metric.Meter { return otel.Meter(name) }

// shutdownAll runs every shutdown function concurrently under ctx and joins
// their errors.
func shutdownAll(ctx context.Context, fns ...func(context.Context) error) error {
	errs := make([]error, len(fns))
	var wg sync.WaitGroup
	for i, fn := range fns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = fn(ctx)
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}
