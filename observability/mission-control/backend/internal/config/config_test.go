package config_test

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/config"
)

// clearEnv blanks every MC_ variable so tests do not see the host's.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "MC_") {
			t.Setenv(k, "")
			require.NoError(t, os.Unsetenv(k))
		}
	}
}

func TestDefaultsBindLoopback(t *testing.T) {
	clearEnv(t)
	cfg, err := config.Load()
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(cfg.HTTPAddr, "127.0.0.1:"), cfg.HTTPAddr)
	require.Equal(t, "0.0.0.0:4318", cfg.OTLPAddr)
	require.Equal(t, "local", cfg.Env)
	require.False(t, cfg.Simulation)
	require.Equal(t, "builtin", cfg.MetricsSource)
	require.Equal(t, 7*24*time.Hour, cfg.Retention)
	require.Equal(t, 30*time.Second, cfg.Thresholds.CourierStale)
	require.Equal(t, 300*time.Second, cfg.Thresholds.CourierLost)
	require.Equal(t, time.Second, cfg.Thresholds.Slow)
}

func TestLoadRefusesSimulationOutsideLocal(t *testing.T) {
	clearEnv(t)
	t.Setenv("MC_SIMULATION", "1")
	t.Setenv("MC_ENV", "staging")
	t.Setenv("MC_SANDBOX_KEY", "dt_sk_test_abc")
	_, err := config.Load()
	require.Error(t, err)
	require.NotContains(t, err.Error(), "dt_sk_test_abc")
}

func TestLoadRefusesNonSandboxKey(t *testing.T) {
	clearEnv(t)
	t.Setenv("MC_SIMULATION", "1")
	t.Setenv("MC_SANDBOX_KEY", "dt_sk_live_secretvalue")
	_, err := config.Load()
	require.Error(t, err)
	require.NotContains(t, err.Error(), "secretvalue")

	t.Setenv("MC_SANDBOX_KEY", "")
	_, err = config.Load()
	require.Error(t, err, "simulation on with an empty key")

	t.Setenv("MC_SANDBOX_KEY", "dt_sk_test_ok")
	cfg, err := config.Load()
	require.NoError(t, err)
	require.True(t, cfg.Simulation)
}

func TestLoadIgnoresKeyWhenSimulationOff(t *testing.T) {
	clearEnv(t)
	t.Setenv("MC_SANDBOX_KEY", "dt_sk_live_whatever")
	_, err := config.Load()
	require.NoError(t, err)
}

func TestLoadRejectsBadMetricsSource(t *testing.T) {
	clearEnv(t)
	t.Setenv("MC_METRICS_SOURCE", "influx")
	_, err := config.Load()
	require.Error(t, err)
}

func TestLoadDropsNonHTTPLinks(t *testing.T) {
	clearEnv(t)
	t.Setenv("MC_LINK_GRAFANA_TRACE", "javascript:alert({trace_id})")
	t.Setenv("MC_LINK_GRAFANA_LOGS", "data:text/html,x")
	t.Setenv("MC_LINK_XRAY_TRACE", "HTTPS://x.example/{trace_id}")
	cfg, err := config.Load()
	require.NoError(t, err)
	require.NotContains(t, cfg.Links, "grafana_trace")
	require.NotContains(t, cfg.Links, "grafana_logs")
	require.Contains(t, cfg.Links, "xray_trace")
}

func TestLoadWarnsOnUnknownLinkName(t *testing.T) {
	clearEnv(t)
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)
	t.Setenv("MC_LINK_GRAFANA", "http://localhost:3000/x")
	t.Setenv("MC_LINK_GRAFANA_TRACE", "http://localhost:3000/t/{trace_id}")
	cfg, err := config.Load()
	require.NoError(t, err)
	require.Contains(t, buf.String(), "unknown")
	require.Contains(t, buf.String(), "grafana")
	require.Contains(t, cfg.Links, "grafana_trace")
}

func TestLoadLinksAndThresholdsOverride(t *testing.T) {
	clearEnv(t)
	t.Setenv("MC_LINK_GRAFANA", "http://localhost:3000/d/{trace_id}")
	f := filepath.Join(t.TempDir(), "t.yaml")
	require.NoError(t, os.WriteFile(f, []byte("courier_stale: 10s\ncourier_lost: 60s\nslow: 2s\n"), 0o600))
	t.Setenv("MC_THRESHOLDS_FILE", f)
	cfg, err := config.Load()
	require.NoError(t, err)
	require.Equal(t, "http://localhost:3000/d/{trace_id}", cfg.Links["grafana"])
	require.Equal(t, 10*time.Second, cfg.Thresholds.CourierStale)
	require.Equal(t, 2*time.Second, cfg.Thresholds.Slow)
}
