package outbox_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/otelx/otelxtest"
	"github.com/raduolivas/dt/backend/pkg/outbox"
)

// testMeter swaps the package meter for one backed by a manual reader. The
// calling test must not be parallel.
func testMeter(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(outbox.SetMeterForTest(mp.Meter("test")))
	return reader
}

func collect(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	otelxtest.AssertAllowed(t, rm)
	return rm
}

func findMetric(rm metricdata.ResourceMetrics, name string) *metricdata.Metrics {
	for _, sm := range rm.ScopeMetrics {
		for i := range sm.Metrics {
			if sm.Metrics[i].Name == name {
				return &sm.Metrics[i]
			}
		}
	}
	return nil
}

func TestConsumeStartsLagMonitor(t *testing.T) {
	reader := testMeter(t)
	prev := outbox.LagPollInterval
	outbox.LagPollInterval = 50 * time.Millisecond
	t.Cleanup(func() { outbox.LagPollInterval = prev })

	e := setup(t)
	ev := newEnvelope(t)
	e.publish(t, marshal(t, ev), ev.GetEventId())
	// The handler blocks, so the message stays pending/ack-pending.
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	e.consume(t, func(ctx context.Context, _ pgx.Tx, _ *eventsv1.Envelope) error {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	})

	waitFor(t, 5*time.Second, "the lag gauge", func() bool {
		m := findMetric(collect(t, reader), outbox.LagGauge)
		if m == nil {
			return false
		}
		g, ok := m.Data.(metricdata.Gauge[int64])
		if !ok {
			return false
		}
		for _, dp := range g.DataPoints {
			s, _ := dp.Attributes.Value("stream")
			c, _ := dp.Attributes.Value("consumer")
			if s.AsString() == outbox.StreamAccounts.Name && c.AsString() == consumerName && dp.Value == 1 {
				return true
			}
		}
		return false
	})
}

func TestConsumeRecordsOutcomes(t *testing.T) {
	reader := testMeter(t)
	e := setup(t)
	a, b := newEnvelope(t), newEnvelope(t)
	e.publish(t, marshal(t, a), a.GetEventId())
	e.publish(t, marshal(t, b), b.GetEventId())
	r := &recorder{fail: func(call int) error {
		switch call {
		case 1:
			return fmt.Errorf("transient")
		case 3:
			return fmt.Errorf("bad payload: %w", outbox.ErrPermanent)
		}
		return nil
	}}
	e.consume(t, r.handle)
	waitFor(t, 10*time.Second, "three handler calls", func() bool { return r.count() == 3 })
	e.publish(t, []byte("garbage"), "garbage-1")

	want := map[string]int64{"ok": 1, "retry": 1, "dead": 2}
	waitFor(t, 10*time.Second, "all outcomes", func() bool {
		m := findMetric(collect(t, reader), "dt.consumer.messages")
		if m == nil {
			return false
		}
		got := map[string]int64{}
		for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
			s, _ := dp.Attributes.Value("stream")
			c, _ := dp.Attributes.Value("consumer")
			o, _ := dp.Attributes.Value("outcome")
			if s.AsString() != outbox.StreamAccounts.Name || c.AsString() != consumerName {
				t.Fatalf("unexpected attributes %v", dp.Attributes)
			}
			got[o.AsString()] += dp.Value
		}
		return fmt.Sprint(got) == fmt.Sprint(want)
	})
	m := findMetric(collect(t, reader), "dt.consumer.process.duration")
	if m == nil {
		t.Fatal("dt.consumer.process.duration not reported")
	}
}

func TestRelayRecordsPublishMetrics(t *testing.T) {
	reader := testMeter(t)
	e := setup(t)
	e.enqueue(t, context.Background(), newEnvelope(t), true)

	relayCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.relay(e.js).Run(relayCtx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})

	waitFor(t, 5*time.Second, "the publish duration", func() bool {
		m := findMetric(collect(t, reader), "dt.outbox.publish.duration")
		if m == nil {
			return false
		}
		h := m.Data.(metricdata.Histogram[float64])
		return len(h.DataPoints) == 1 && h.DataPoints[0].Count >= 1 && h.DataPoints[0].Attributes.Len() == 0
	})
	p := findMetric(collect(t, reader), "dt.outbox.pending")
	if p == nil {
		t.Fatal("dt.outbox.pending not reported")
	}
	if dp := p.Data.(metricdata.Gauge[int64]).DataPoints; len(dp) != 1 || dp[0].Value != 0 {
		t.Fatalf("pending = %v, want one point with 0 (everything published)", dp)
	}
}
