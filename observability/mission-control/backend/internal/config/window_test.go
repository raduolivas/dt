package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/config"
)

// detect.window is the lookback of every event-based rule: a shorter one would
// silently blind a rule.
func TestDetectWindowMustCoverEveryRuleWindow(t *testing.T) {
	clearEnv(t)
	f := filepath.Join(t.TempDir(), "t.yaml")
	t.Setenv("MC_THRESHOLDS_FILE", f)
	head := "courier_stale: 10s\ncourier_lost: 60s\nslow: 2s\ndetect:\n"
	for name, body := range map[string]string{
		"window below processed_not_published": "  window: 20s\n",
		"window below storm window":            "  window: 2m\n  storm: {window: 3m}\n",
		"window below consumer_lagging":        "  window: 4m\n  consumer_lagging: 5m\n",
	} {
		require.NoError(t, os.WriteFile(f, []byte(head+body), 0o600), name)
		_, err := config.Load()
		require.Error(t, err, name)
		require.Contains(t, err.Error(), "detect.window", name)
	}
	require.NoError(t, os.WriteFile(f, []byte(head+"  window: 10m\n  storm: {window: 3m}\n"), 0o600))
	_, err := config.Load()
	require.NoError(t, err)
}
