package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/config"
)

func TestDetectThresholdsDefaults(t *testing.T) {
	clearEnv(t)
	cfg, err := config.Load()
	require.NoError(t, err)
	d := cfg.Thresholds.Detect
	require.Equal(t, 30*time.Second, cfg.Thresholds.CourierStale, "DT's own stale value is unchanged")
	require.Equal(t, 5*time.Minute, cfg.Thresholds.CourierLost)
	require.Equal(t, 30*time.Second, d.ProcessedNotPublished)
	require.Equal(t, 60*time.Second, d.ConsumerLagging)
	require.Equal(t, 10, d.Storm.Count)
	require.Equal(t, time.Minute, d.Storm.Window)
	require.Equal(t, 3, d.DependencyFailing.Count)
	require.Equal(t, 10*time.Second, d.MCBehind.Lag)
	require.Equal(t, 10*time.Minute, d.StuckStatus["at_pickup"])
	for _, s := range []string{"confirmed", "courier_assigned", "at_pickup", "picked_up", "arriving"} {
		require.Positive(t, d.StuckStatus[s], s)
	}
}

func TestDetectThresholdsPartialOverrideKeepsDefaults(t *testing.T) {
	clearEnv(t)
	f := filepath.Join(t.TempDir(), "t.yaml")
	// An older file without a detect section, then one overriding a single key.
	require.NoError(t, os.WriteFile(f, []byte("courier_stale: 10s\ncourier_lost: 60s\nslow: 2s\n"), 0o600))
	t.Setenv("MC_THRESHOLDS_FILE", f)
	cfg, err := config.Load()
	require.NoError(t, err)
	require.Equal(t, 10, cfg.Thresholds.Detect.Storm.Count, "no detect section: defaults")

	require.NoError(t, os.WriteFile(f, []byte("courier_stale: 10s\ncourier_lost: 60s\nslow: 2s\ndetect:\n  storm: {count: 20}\n"), 0o600))
	cfg, err = config.Load()
	require.NoError(t, err)
	require.Equal(t, 20, cfg.Thresholds.Detect.Storm.Count)
	require.Equal(t, time.Minute, cfg.Thresholds.Detect.Storm.Window, "unset keys keep defaults")

	require.NoError(t, os.WriteFile(f, []byte("courier_stale: 10s\ncourier_lost: 60s\nslow: 2s\ndetect:\n  storm: {count: -1}\n"), 0o600))
	_, err = config.Load()
	require.Error(t, err)
}
