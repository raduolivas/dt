package metrics_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/config"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/metrics"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/topology"
)

type fake struct {
	name string
	out  []metrics.WireStat
	err  error
}

func (f fake) WireTraffic(context.Context, time.Duration) ([]metrics.WireStat, error) {
	return f.out, f.err
}
func (f fake) Name() string { return f.name }

type flag struct{ v atomic.Bool }

func (f *flag) MetricsSourceUnavailable(b bool) { f.v.Store(b) }

func TestFallbackOnPrometheusError(t *testing.T) {
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer prom.Close()
	p := metrics.New(config.Config{MetricsSource: config.MetricsPrometheus, PrometheusURL: prom.URL}, nil, topology.Default())
	require.NotNil(t, p)
	require.Equal(t, "prometheus", p.Name())
	_, err := p.WireTraffic(context.Background(), time.Minute)
	require.Error(t, err)

	b := fake{name: "builtin", out: []metrics.WireStat{{WireID: "a>b", Rate: 2}}}
	f := &flag{}
	s := metrics.WithFallback(p, b, f)
	require.NotNil(t, s)
	got, err := s.WireTraffic(context.Background(), time.Minute)
	require.NoError(t, err)
	require.Equal(t, b.out, got)
	require.True(t, f.v.Load(), "metrics_source_unavailable set")

	// primary recovers: flag clears
	s = metrics.WithFallback(fake{name: "prometheus", out: []metrics.WireStat{{WireID: "x>y"}}}, b, f)
	_, err = s.WireTraffic(context.Background(), time.Minute)
	require.NoError(t, err)
	require.False(t, f.v.Load())
}

func TestPrometheusParsesVectors(t *testing.T) {
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/query", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"wire":"gateway.partner_api>deliveries.rpc"},"value":[1,"3.5"]}]}}`))
	}))
	defer prom.Close()
	p := metrics.New(config.Config{MetricsSource: config.MetricsPrometheus, PrometheusURL: prom.URL}, nil, topology.Default())
	got, err := p.WireTraffic(context.Background(), time.Minute)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "gateway.partner_api>deliveries.rpc", got[0].WireID)
	require.InDelta(t, 3.5, got[0].Rate, 1e-9)
}

func TestPrometheusEmptyIsError(t *testing.T) {
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer prom.Close()
	p := metrics.New(config.Config{MetricsSource: config.MetricsPrometheus, PrometheusURL: prom.URL}, nil, topology.Default())
	_, err := p.WireTraffic(context.Background(), time.Minute)
	require.True(t, errors.Is(err, metrics.ErrNoData), "%v", err)
}

func TestCloudWatchNotImplemented(t *testing.T) {
	s := metrics.New(config.Config{MetricsSource: config.MetricsCloudWatch}, nil, topology.Default())
	_, err := s.WireTraffic(context.Background(), time.Minute)
	require.ErrorIs(t, err, metrics.ErrNotImplemented)
}

func TestBuiltinRatesFromEvents(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: needs Docker")
	}
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgis/postgis:18-3.6", postgres.WithDatabase("mc"),
		postgres.WithUsername("mc"), postgres.WithPassword("mc"), postgres.BasicWaitStrategies())
	testcontainers.CleanupContainer(t, c)
	require.NoError(t, err)
	url, err := c.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	st, err := store.Open(ctx, url)
	require.NoError(t, err)
	defer st.Close()

	now := time.Now().UTC()
	ev := func(src, tgt, outcome string, ms float64, ago time.Duration, kind string) store.Event {
		attrs, _ := json.Marshal(map[string]any{"duration_ms": ms})
		return store.Event{ObservedAt: now.Add(-ago), OccurredAt: now.Add(-ago), Kind: kind, Source: src, Target: tgt,
			Outcome: outcome, Origin: "live", Attrs: attrs}
	}
	var evs []store.Event
	for i := 1; i <= 10; i++ { // 10 events on a known wire, 2 failed, durations 10..100
		o := "ok"
		if i <= 2 {
			o = "failed"
		}
		evs = append(evs, ev("gateway.partner_api", "deliveries.rpc", o, float64(i*10), time.Duration(i)*time.Second, "request"))
	}
	evs = append(evs,
		ev("gateway.partner_api", "deliveries.rpc", "ok", 5, 2*time.Hour, "request"), // outside window
		ev("gateway.partner_api", "deliveries.rpc", "ok", 5, time.Second, "health"),  // not traffic
		ev("nope", "nowhere", "ok", 5, time.Second, "request"),                       // not a wire
	)
	require.NoError(t, st.InsertEvents(ctx, evs))

	src := metrics.New(config.Config{MetricsSource: config.MetricsBuiltin}, st, topology.Default())
	require.Equal(t, "builtin", src.Name())
	got, err := src.WireTraffic(ctx, time.Minute)
	require.NoError(t, err)
	require.Len(t, got, 1)
	w := got[0]
	require.Equal(t, "gateway.partner_api>deliveries.rpc", w.WireID)
	require.InDelta(t, 10.0/60, w.Rate, 1e-9)
	require.InDelta(t, 0.2, w.ErrorRate, 1e-9)
	require.InDelta(t, 95.5, w.P95ms, 0.01)
}
