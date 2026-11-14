package httpapi_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/coder/websocket"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/raduolivas/dt/backend/pkg/otelx"
	"github.com/raduolivas/dt/backend/pkg/otelx/otelxtest"
	httpapi "github.com/raduolivas/dt/backend/services/tracking/internal/adapters/inbound/http"
	"github.com/raduolivas/dt/backend/services/tracking/internal/app"
	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

// viewers collects dt.tracking.viewer.connections as transport -> value.
func viewers(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	otelxtest.AssertAllowed(t, rm)
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "dt.tracking.viewer.connections" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("%s is %T", m.Name, m.Data)
			}
			if sum.IsMonotonic {
				t.Fatal("viewer connections must be an up-down counter")
			}
			for _, dp := range sum.DataPoints {
				if dp.Attributes.Len() != 1 {
					t.Fatalf("attributes = %v", dp.Attributes)
				}
				v, _ := dp.Attributes.Value(attribute.Key("transport"))
				out[v.AsString()] += dp.Value
			}
		}
	}
	return out
}

func waitViewers(t *testing.T, reader *sdkmetric.ManualReader, transport string, want int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := viewers(t, reader)
		if got[transport] == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s connections = %v, want %d", transport, got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHubViewerConnections(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader), sdkmetric.WithView(otelx.Views()...))
	t.Cleanup(httpapi.SetMeterForTest(mp.Meter("test")))
	e := newEnv(t)
	tok := e.token(claimsA)

	c := e.dial()
	auth(t, c, tok)
	mustRead(t, c)
	waitViewers(t, reader, "ws", 1)

	ch, res := e.sse(tok)
	if res.StatusCode != 200 {
		t.Fatalf("sse status %d", res.StatusCode)
	}
	next(t, ch)
	waitViewers(t, reader, "sse", 1)
	if got := viewers(t, reader); got["ws"] != 1 {
		t.Fatalf("ws = %v", got)
	}

	// A refused connection (bad token) never counts.
	bad := e.dial()
	auth(t, bad, "garbage")
	mustClose(t, bad, 4401, 3*time.Second)

	_ = c.Close(websocket.StatusNormalClosure, "")
	waitViewers(t, reader, "ws", 0)

	// Revoking the link ends the SSE stream.
	e.revoke(claimsA.LinkID)
	waitViewers(t, reader, "sse", 0)
}

// fixLatency collects dt.tracking.fix_to_viewer.duration as source -> (count, sum).
func fixLatency(t *testing.T, reader *sdkmetric.ManualReader) map[string]struct {
	n   uint64
	sum float64
} {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	otelxtest.AssertAllowed(t, rm)
	out := map[string]struct {
		n   uint64
		sum float64
	}{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "dt.tracking.fix_to_viewer.duration" {
				continue
			}
			h, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("%s is %T", m.Name, m.Data)
			}
			if m.Unit != "s" {
				t.Fatalf("unit = %q", m.Unit)
			}
			for _, dp := range h.DataPoints {
				if dp.Attributes.Len() != 1 {
					t.Fatalf("attributes = %v", dp.Attributes)
				}
				v, _ := dp.Attributes.Value(attribute.Key("source"))
				out[v.AsString()] = struct {
					n   uint64
					sum float64
				}{dp.Count, dp.Sum}
			}
		}
	}
	return out
}

func TestHubObservesOncePerSnapshot(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader), sdkmetric.WithView(otelx.Views()...))
	t.Cleanup(httpapi.SetMeterForTest(mp.Meter("test")))
	received := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	now := received.Add(250 * time.Millisecond)
	e := newEnv(t, func(d *httpapi.Deps) { d.Clock = func() time.Time { return now } })
	tok := e.token(claimsA)
	snapJSON, err := json.Marshal(snapshot(delA, 11, domain.StatusPickedUp, true))
	if err != nil {
		t.Fatal(err)
	}
	withFix := app.WithFixReceived(context.Background(), received, "relay")

	// No viewers: nothing to measure.
	e.hub.Snapshot(withFix, delA, snapJSON)
	if got := fixLatency(t, reader); len(got) != 0 {
		t.Fatalf("observed with no viewers: %v", got)
	}

	conns := make([]*websocket.Conn, 3)
	for i := range conns {
		conns[i] = e.dial()
		auth(t, conns[i], tok)
		mustRead(t, conns[i])
	}
	waitViewers(t, reader, "ws", 3)

	// A snapshot without a received time (a state change) is not a fix.
	e.hub.Snapshot(context.Background(), delA, snapJSON)
	if got := fixLatency(t, reader); len(got) != 0 {
		t.Fatalf("observed without a received time: %v", got)
	}

	// Three viewers, one observation of hub clock - received time.
	e.hub.Snapshot(withFix, delA, snapJSON)
	got := fixLatency(t, reader)
	if len(got) != 1 || got["relay"].n != 1 || got["relay"].sum < 0.249 || got["relay"].sum > 0.251 {
		t.Fatalf("observations = %+v, want one 0.25 s for relay", got)
	}

	// An unknown source label is bounded to "other".
	e.hub.Snapshot(app.WithFixReceived(context.Background(), received, "weird-source"), delA, snapJSON)
	if got := fixLatency(t, reader); got["other"].n != 1 || got["relay"].n != 1 {
		t.Fatalf("observations = %+v", got)
	}
}
