package links_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/metrics/links"
)

func TestLinksOnlyWhenConfigured(t *testing.T) {
	tpl := map[string]string{"grafana_trace": "https://g.example/explore?trace={trace_id}"}
	got := links.Render(tpl, "grafana_trace", map[string]string{"trace_id": "abc123"})
	require.Len(t, got, 1)
	require.Equal(t, "https://g.example/explore?trace=abc123", got[0].URL)
	require.Equal(t, "grafana_trace", got[0].Kind)

	for _, k := range []string{"grafana_logs", "xray_trace", "cloudwatch_logs", "unknown"} {
		require.Empty(t, links.Render(tpl, k, map[string]string{"trace_id": "abc123"}), k)
	}
	require.Empty(t, links.Render(nil, "grafana_trace", nil))
}

func TestLinkVarsEscaped(t *testing.T) {
	tpl := map[string]string{"grafana_logs": "https://g.example/logs?q={service}&t={trace_id}"}
	got := links.Render(tpl, "grafana_logs", map[string]string{"service": "a b&c=d/../?", "trace_id": "x#y"})
	require.Len(t, got, 1)
	require.Equal(t, "https://g.example/logs?q=a%20b%26c%3Dd%2F..%2F%3F&t=x%23y", got[0].URL)
}

func TestLinkNeverUnsafeScheme(t *testing.T) {
	for _, tpl := range []string{"javascript:alert({trace_id})", "data:text/html,{trace_id}", "{trace_id}", "ftp://x/{trace_id}"} {
		require.Empty(t, links.Render(map[string]string{"grafana_trace": tpl}, "grafana_trace",
			map[string]string{"trace_id": "javascript:alert(1)"}), tpl)
	}
	require.True(t, links.SafeTemplate("HTTP://x/{a}"))
	require.False(t, links.SafeTemplate(" javascript:x"))
}

func TestMissingVarSkipsLink(t *testing.T) {
	tpl := map[string]string{"grafana_trace": "https://g/{trace_id}"}
	require.Empty(t, links.Render(tpl, "grafana_trace", map[string]string{}))
}
