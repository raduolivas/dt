package consumer_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/raduolivas/dt/backend/services/gateway/internal/adapters/inbound/consumer"
)

func TestLagMonitorGaugeAndWarning(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	var buf bytes.Buffer
	m, err := consumer.NewLagMonitor(mp.Meter("test"), slog.New(slog.NewTextHandler(&buf, nil)), "gateway-apikeys")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	t0 := time.Now()

	gauge := func() int64 {
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(ctx, &rm); err != nil {
			t.Fatal(err)
		}
		for _, sm := range rm.ScopeMetrics {
			for _, mt := range sm.Metrics {
				if g, ok := mt.Data.(metricdata.Gauge[int64]); ok && mt.Name == "dt_consumer_pending_messages" {
					dp := g.DataPoints[0]
					if v, _ := dp.Attributes.Value("consumer"); v.AsString() != "gateway-apikeys" {
						t.Fatalf("consumer attribute = %q", v.AsString())
					}
					return dp.Value
				}
			}
		}
		t.Fatal("gauge not reported")
		return 0
	}

	m.Record(ctx, 150, t0)
	if got := gauge(); got != 150 {
		t.Fatalf("gauge = %d, want 150", got)
	}
	m.Record(ctx, 150, t0.Add(consumer.LagWarnAfter)) // exactly 30 s: not yet "more than"
	if strings.Contains(buf.String(), "consumer_lagging") {
		t.Fatal("warned before the backlog was high for longer than 30 s")
	}
	m.Record(ctx, 160, t0.Add(consumer.LagWarnAfter+time.Second))
	m.Record(ctx, 170, t0.Add(consumer.LagWarnAfter+2*time.Second))
	if n := strings.Count(buf.String(), "consumer_lagging"); n != 1 {
		t.Fatalf("got %d warnings, want exactly 1 per high stretch", n)
	}

	// Recovering resets the stretch.
	buf.Reset()
	m.Record(ctx, 5, t0.Add(time.Minute))
	m.Record(ctx, 500, t0.Add(time.Minute+time.Second))
	m.Record(ctx, 500, t0.Add(time.Minute+10*time.Second))
	if strings.Contains(buf.String(), "consumer_lagging") {
		t.Fatal("warned although the new stretch is only 9 s old")
	}
}
