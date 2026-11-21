package metrics_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/config"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/metrics"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/topology"
)

const wireRulesPath = "../../../../prometheus/rules/mc-wires.yaml"

// wireSeries are the three series the prometheus adapter reads, each with
// one rule per wire.
var wireSeries = []string{"mc:wire_requests:rate1m", "mc:wire_errors:ratio1m", "mc:wire_latency_p95_ms:1m"}

// wantWires are exactly the wires of spec §5.5.
var wantWires = []string{
	"partner>gateway.partner_api",
	"courier>gateway.courier_api",
	"viewer>gateway.viewer_api",
	"gateway.partner_api>deliveries.rpc",
	"gateway.partner_api>tracking.ingest",
	"tracking.simulator>deliveries.rpc",
}

type ruleFile struct {
	Groups []struct {
		Rules []struct {
			Record string            `yaml:"record"`
			Labels map[string]string `yaml:"labels"`
		} `yaml:"rules"`
	} `yaml:"groups"`
}

func loadWireRules(t *testing.T) ruleFile {
	t.Helper()
	raw, err := os.ReadFile(wireRulesPath)
	require.NoError(t, err)
	var rf ruleFile
	require.NoError(t, yaml.Unmarshal(raw, &rf))
	return rf
}

func TestWireRulesNameKnownWires(t *testing.T) {
	known := map[string]bool{}
	for _, w := range topology.Default().Wires {
		known[w.ID] = true
	}
	rf := loadWireRules(t)
	perSeries := map[string]map[string]bool{}
	seen := map[string]bool{}
	for _, g := range rf.Groups {
		for _, r := range g.Rules {
			wire := r.Labels["wire"]
			require.NotEmpty(t, wire, "rule %s has no wire label", r.Record)
			require.True(t, known[wire], "rule %s names %q, which topology.Default() does not have", r.Record, wire)
			seen[wire] = true
			if perSeries[r.Record] == nil {
				perSeries[r.Record] = map[string]bool{}
			}
			perSeries[r.Record][wire] = true
		}
	}
	got := make([]string, 0, len(seen))
	for w := range seen {
		got = append(got, w)
	}
	sort.Strings(got)
	want := append([]string(nil), wantWires...)
	sort.Strings(want)
	require.Equal(t, want, got, "wire rules cover exactly the wires of spec §5.5")
	for _, s := range wireSeries {
		require.Len(t, perSeries[s], len(wantWires), "%s must have one rule per wire", s)
	}
}

func TestPrometheusQueriesReadWireSeries(t *testing.T) {
	var mu sync.Mutex
	var got []string
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.URL.Query().Get("query"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"wire":"partner>gateway.partner_api"},"value":[1,"1"]}]}}`))
	}))
	defer prom.Close()
	p := metrics.New(config.Config{MetricsSource: config.MetricsPrometheus, PrometheusURL: prom.URL}, nil, topology.Default())
	_, err := p.WireTraffic(context.Background(), time.Minute)
	require.NoError(t, err)
	sort.Strings(got)
	want := append([]string(nil), wireSeries...)
	sort.Strings(want)
	require.Equal(t, want, got, "the three queries are the recorded series, with no window template")
}
