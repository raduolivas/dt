package app

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/raduolivas/dt/backend/pkg/otelx"
	"github.com/raduolivas/dt/backend/pkg/otelx/otelxtest"
	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

// fixCounts collects the reader, asserts the allow-list and returns
// dt.tracking.fixes as "source/outcome" -> value.
func fixCounts(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	otelxtest.AssertAllowed(t, rm)
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "dt.tracking.fixes" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("%s is %T", m.Name, m.Data)
			}
			for _, dp := range sum.DataPoints {
				src, _ := dp.Attributes.Value(attribute.Key("source"))
				oc, _ := dp.Attributes.Value(attribute.Key("outcome"))
				if dp.Attributes.Len() != 2 {
					t.Fatalf("attributes = %v", dp.Attributes)
				}
				out[src.AsString()+"/"+oc.AsString()] += dp.Value
			}
		}
	}
	return out
}

func testMeter(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader), sdkmetric.WithView(otelx.Views()...))
	t.Cleanup(setMeterForTest(mp.Meter("test")))
	return reader
}

func TestIngestCountsOutcomes(t *testing.T) {
	reader := testMeter(t)
	f := newFixture(t)
	f.courier(t, "cou_1")
	f.delivery(t, "del_1", domain.ModelDirect, "cou_1", domain.StatusPickedUp)
	f.clock.Advance(1_000_000_000)
	now := f.clock.Now()
	// One good fix and one with an invalid latitude.
	acc, drop, err := f.svc.Ingest(context.Background(), sc, "cou_1",
		[]domain.Fix{fixAt(now, 52.369, 4.9), fixAt(now, 200, 4.9)}, "sdk")
	if err != nil || acc != 1 || drop != 1 {
		t.Fatalf("ingest = %d %d %v", acc, drop, err)
	}
	got := fixCounts(t, reader)
	want := map[string]int64{"sdk/accepted": 1, "sdk/invalid": 1}
	if len(got) != len(want) || got["sdk/accepted"] != 1 || got["sdk/invalid"] != 1 {
		t.Fatalf("counts = %v, want %v", got, want)
	}

	// A courier without an active delivery, and a source outside the set.
	f.courier(t, "cou_2")
	if _, _, err := f.svc.Ingest(context.Background(), sc, "cou_2", []domain.Fix{fixAt(now, 52.369, 4.9)}, "mystery"); err != nil {
		t.Fatal(err)
	}
	got = fixCounts(t, reader)
	if got["other/no_active_delivery"] != 1 || len(got) != 3 {
		t.Fatalf("counts = %v", got)
	}
}
