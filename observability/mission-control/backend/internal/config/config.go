// Package config reads Mission Control's settings from the environment and
// the thresholds file. Errors never echo the sandbox key.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	mcconfig "github.com/raduolivas/dt/observability/mission-control/backend/config"
)

// SandboxKeyPrefix is the only API key prefix simulation may use.
const SandboxKeyPrefix = "dt_sk_test_"

// Metrics sources (spec §4.5).
const (
	MetricsBuiltin    = "builtin"
	MetricsPrometheus = "prometheus"
	MetricsCloudWatch = "cloudwatch"
)

// Thresholds is config/thresholds.yaml.
type Thresholds struct {
	CourierStale time.Duration  `yaml:"courier_stale"`
	CourierLost  time.Duration  `yaml:"courier_lost"`
	Slow         time.Duration  `yaml:"slow"`
	Edges        map[string]any `yaml:"edges"`
	Rules        map[string]any `yaml:"rules"`
}

// Config is Mission Control's configuration.
type Config struct {
	// HTTPAddr serves the API and UI; loopback by default.
	HTTPAddr string
	// OTLPAddr receives the collector's OTLP/HTTP export. 0.0.0.0 is for the
	// container only: compose publishes no OTLP port.
	OTLPAddr string

	DatabaseURL    string
	NATSURL        string
	NATSMonitorURL string
	GatewayURL     string

	Env        string
	Simulation bool
	SandboxKey string

	MetricsSource string
	PrometheusURL string
	// Links maps a name to a URL template (MC_LINK_<NAME>, lower-cased).
	Links map[string]string

	Retention  time.Duration
	Thresholds Thresholds
}

// Retention is how long events are kept.
const Retention = 7 * 24 * time.Hour

// Load reads the configuration from the environment.
func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:       get("MC_HTTP_ADDR", "127.0.0.1:8095"),
		OTLPAddr:       get("MC_OTLP_ADDR", "0.0.0.0:4318"),
		DatabaseURL:    get("MC_DATABASE_URL", "postgres://mc:mc@127.0.0.1:5433/mc?sslmode=disable"),
		NATSURL:        get("MC_NATS_URL", "nats://127.0.0.1:4222"),
		NATSMonitorURL: get("MC_NATS_MONITOR_URL", "http://127.0.0.1:8222"),
		GatewayURL:     get("MC_GATEWAY_URL", "http://127.0.0.1:8080"),
		Env:            get("MC_ENV", "local"),
		Simulation:     os.Getenv("MC_SIMULATION") == "1",
		SandboxKey:     os.Getenv("MC_SANDBOX_KEY"),
		MetricsSource:  get("MC_METRICS_SOURCE", MetricsBuiltin),
		PrometheusURL:  os.Getenv("MC_PROMETHEUS_URL"),
		Links:          links(),
		Retention:      Retention,
	}

	switch cfg.MetricsSource {
	case MetricsBuiltin, MetricsPrometheus, MetricsCloudWatch:
	default:
		return Config{}, fmt.Errorf("config: MC_METRICS_SOURCE must be %s, %s or %s",
			MetricsBuiltin, MetricsPrometheus, MetricsCloudWatch)
	}
	if cfg.Simulation {
		if cfg.Env != "local" {
			return Config{}, errors.New("config: MC_SIMULATION=1 is only allowed with MC_ENV=local")
		}
		if !strings.HasPrefix(cfg.SandboxKey, SandboxKeyPrefix) {
			return Config{}, fmt.Errorf("config: MC_SANDBOX_KEY must start with %s when MC_SIMULATION=1", SandboxKeyPrefix)
		}
	}

	raw := mcconfig.DefaultThresholds
	if p := os.Getenv("MC_THRESHOLDS_FILE"); p != "" {
		b, err := os.ReadFile(p) //nolint:gosec // operator-supplied path by design
		if err != nil {
			return Config{}, fmt.Errorf("config: read thresholds: %w", err)
		}
		raw = b
	}
	if err := yaml.Unmarshal(raw, &cfg.Thresholds); err != nil {
		return Config{}, fmt.Errorf("config: parse thresholds: %w", err)
	}
	t := cfg.Thresholds
	if t.CourierStale <= 0 || t.CourierLost < t.CourierStale || t.Slow <= 0 {
		return Config{}, errors.New("config: thresholds: need 0 < courier_stale <= courier_lost and slow > 0")
	}
	return cfg, nil
}

func get(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func links() map[string]string {
	out := map[string]string{}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if name, ok := strings.CutPrefix(k, "MC_LINK_"); ok && v != "" && name != "" {
			out[strings.ToLower(name)] = v
		}
	}
	return out
}
