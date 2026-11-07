// Package config reads Mission Control's settings from the environment and
// the thresholds file. Errors never echo the sandbox key.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	mcconfig "github.com/raduolivas/dt/observability/mission-control/backend/config"
	linkkinds "github.com/raduolivas/dt/observability/mission-control/backend/internal/metrics/links"
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
	Detect       Detect         `yaml:"detect"`
}

// CountWindow is "more than/at least Count events inside Window".
type CountWindow struct {
	Count  int           `yaml:"count"`
	Window time.Duration `yaml:"window"`
}

// Detect holds the detector thresholds (spec §7); the keys are documented in
// config/thresholds.yaml.
type Detect struct {
	Window                time.Duration            `yaml:"window"`
	StatusLookback        time.Duration            `yaml:"status_lookback"`
	FixesDropped          time.Duration            `yaml:"fixes_dropped"`
	RejectedAtGateway     time.Duration            `yaml:"rejected_at_gateway"`
	ProcessedNotPublished time.Duration            `yaml:"processed_not_published"`
	ConsumerLagging       time.Duration            `yaml:"consumer_lagging"`
	SnapshotNotDelivered  time.Duration            `yaml:"snapshot_not_delivered"`
	Storm                 CountWindow              `yaml:"storm"`
	DependencyFailing     CountWindow              `yaml:"dependency_failing"`
	MCBehind              MCBehind                 `yaml:"mc_behind"`
	StuckStatus           map[string]time.Duration `yaml:"stuck_status"`
}

func (d Detect) validate() error {
	for name, v := range map[string]time.Duration{
		"window": d.Window, "status_lookback": d.StatusLookback, "fixes_dropped": d.FixesDropped,
		"rejected_at_gateway": d.RejectedAtGateway, "processed_not_published": d.ProcessedNotPublished,
		"consumer_lagging": d.ConsumerLagging, "snapshot_not_delivered": d.SnapshotNotDelivered,
		"storm.window": d.Storm.Window, "dependency_failing.window": d.DependencyFailing.Window,
		"mc_behind.lag": d.MCBehind.Lag, "mc_behind.drops_hold": d.MCBehind.DropsHold,
	} {
		if v <= 0 {
			return fmt.Errorf("config: thresholds: detect.%s must be > 0", name)
		}
	}
	for name, v := range map[string]time.Duration{
		"fixes_dropped": d.FixesDropped, "rejected_at_gateway": d.RejectedAtGateway,
		"processed_not_published": d.ProcessedNotPublished, "consumer_lagging": d.ConsumerLagging,
		"snapshot_not_delivered": d.SnapshotNotDelivered, "storm.window": d.Storm.Window,
		"dependency_failing.window": d.DependencyFailing.Window,
	} {
		if d.Window < v {
			return fmt.Errorf("config: thresholds: detect.window (%s) must be >= detect.%s (%s)", d.Window, name, v)
		}
	}
	if d.Storm.Count <= 0 || d.DependencyFailing.Count <= 0 {
		return errors.New("config: thresholds: detect storm and dependency_failing counts must be > 0")
	}
	for s, v := range d.StuckStatus {
		if v <= 0 {
			return fmt.Errorf("config: thresholds: detect.stuck_status.%s must be > 0", s)
		}
	}
	return nil
}

// MCBehind is the "Mission Control itself is behind" rule.
type MCBehind struct {
	Lag       time.Duration `yaml:"lag"`        // ingest lag above this
	DropsHold time.Duration `yaml:"drops_hold"` // a drop keeps the finding open this long
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
	// EdgeURL is the origin of the viewer API (simulation only); it defaults to GatewayURL.
	EdgeURL string

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
		EdgeURL:        os.Getenv("MC_EDGE_URL"),
		Env:            get("MC_ENV", "local"),
		Simulation:     os.Getenv("MC_SIMULATION") == "1",
		SandboxKey:     os.Getenv("MC_SANDBOX_KEY"),
		MetricsSource:  get("MC_METRICS_SOURCE", MetricsBuiltin),
		PrometheusURL:  os.Getenv("MC_PROMETHEUS_URL"),
		Links:          links(),
		Retention:      Retention,
	}

	if cfg.EdgeURL == "" {
		cfg.EdgeURL = cfg.GatewayURL
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
	// The detect section is merged key by key over the embedded defaults, so an
	// override file may set only the keys it changes.
	var def Thresholds
	if err := yaml.Unmarshal(mcconfig.DefaultThresholds, &def); err != nil {
		return Config{}, fmt.Errorf("config: parse default thresholds: %w", err)
	}
	merged := def.Detect
	if err := yaml.Unmarshal(raw, &struct {
		Detect *Detect `yaml:"detect"`
	}{&merged}); err != nil {
		return Config{}, fmt.Errorf("config: parse thresholds: %w", err)
	}
	cfg.Thresholds.Detect = merged
	if err := merged.validate(); err != nil {
		return Config{}, err
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
			// Only http(s) templates: never a javascript: or data: URL. The value is not logged.
			if l := strings.ToLower(v); !strings.HasPrefix(l, "http://") && !strings.HasPrefix(l, "https://") {
				slog.Warn("config: dropping link template that is not http(s)", "name", strings.ToLower(name))
				continue
			}
			if !slices.Contains(linkkinds.Kinds(), strings.ToLower(name)) {
				slog.Warn("config: unknown MC_LINK_ name, no link will be shown",
					"name", strings.ToLower(name), "valid", strings.Join(linkkinds.Kinds(), ","))
			}
			out[strings.ToLower(name)] = v
		}
	}
	return out
}
