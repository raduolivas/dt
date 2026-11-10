package httpapi_test

import (
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/raduolivas/dt/backend/pkg/otelx"
	"github.com/raduolivas/dt/backend/pkg/otelx/otelxtest"
	httpapi "github.com/raduolivas/dt/backend/services/tracking/internal/adapters/inbound/http"
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
