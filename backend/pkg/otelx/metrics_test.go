package otelx_test

import (
	"context"
	"slices"
	"testing"

	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/raduolivas/dt/backend/pkg/otelx"
)

func TestViewsApplyBuckets(t *testing.T) {
	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader), metric.WithView(otelx.Views()...))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	m := mp.Meter("test")

	want := map[string][]float64{
		"http.server.request.duration":       otelx.HTTPBuckets,
		"rpc.server.duration":                otelx.HTTPBuckets,
		"rpc.client.duration":                otelx.HTTPBuckets,
		"dt.routing.duration":                otelx.HTTPBuckets,
		"dt.tracking.fix_to_viewer.duration": otelx.FixToViewerBuckets,
	}
	for name := range want {
		h, err := m.Float64Histogram(name)
		if err != nil {
			t.Fatal(err)
		}
		v := 0.15
		if name == "dt.tracking.fix_to_viewer.duration" {
			v = 0.8
		}
		h.Record(context.Background(), v)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, sm := range rm.ScopeMetrics {
		for _, mt := range sm.Metrics {
			hist, ok := mt.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("%s: not a float64 histogram", mt.Name)
			}
			seen++
			if got := hist.DataPoints[0].Bounds; !slices.Equal(got, want[mt.Name]) {
				t.Errorf("%s bounds = %v, want %v", mt.Name, got, want[mt.Name])
			}
		}
	}
	if seen != len(want) {
		t.Fatalf("saw %d metrics, want %d", seen, len(want))
	}
}
