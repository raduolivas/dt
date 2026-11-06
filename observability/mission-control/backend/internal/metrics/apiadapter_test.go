package metrics_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/ingest"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/metrics"
)

func TestTrafficAdapter(t *testing.T) {
	src := fake{out: []metrics.WireStat{{WireID: "a>b", Rate: 1, ErrorRate: 0.5, P95ms: 12}}}
	got, err := metrics.Traffic(src).Traffic(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.EqualValues(t, 300, got[0].Count)
	require.EqualValues(t, 150, got[0].Failed)
	require.Equal(t, 12.0, got[0].P95Ms)
}

func TestLinkSourceTraceAndComponent(t *testing.T) {
	tpl := map[string]string{
		"grafana_trace": "https://g/t/{trace_id}",
		"grafana_logs":  "https://g/l?svc={service}",
	}
	ls := metrics.Links(tpl)
	comp := ls.Links("gateway.partner_api", "")
	require.Len(t, comp, 1, "trace link needs a trace id")
	require.Equal(t, "https://g/l?svc=gateway", comp[0].URL)
	require.Len(t, ls.Links("", "t1"), 1) // trace link only: no service var for logs
	require.Empty(t, metrics.Links(nil).Links("x", "y"))
}

func TestSelfFlagIsRecorded(t *testing.T) {
	s := ingest.NewSelf(nil, nil)
	s.MetricsSourceUnavailable(true)
	require.True(t, s.Snapshot()[ingest.SourceMetrics].Unavailable)
	s.MetricsSourceUnavailable(false)
	require.False(t, s.Snapshot()[ingest.SourceMetrics].Unavailable)
}
