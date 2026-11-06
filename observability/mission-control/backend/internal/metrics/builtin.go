package metrics

import (
	"context"
	"time"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/config"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/topology"
)

// New returns the Source cfg.MetricsSource names. st backs builtin.
func New(cfg config.Config, st *store.Store, topo topology.Topology) Source {
	switch cfg.MetricsSource {
	case config.MetricsPrometheus:
		return newPrometheus(cfg.PrometheusURL)
	case config.MetricsCloudWatch:
		return cloudwatch{}
	default:
		return builtin{st: st, topo: topo}
	}
}

type cloudwatch struct{}

func (cloudwatch) Name() string { return config.MetricsCloudWatch }
func (cloudwatch) WireTraffic(context.Context, time.Duration) ([]WireStat, error) {
	return nil, ErrNotImplemented
}

// builtin aggregates mc_event per topology wire.
type builtin struct {
	st   *store.Store
	topo topology.Topology
}

func (builtin) Name() string { return config.MetricsBuiltin }

func (b builtin) WireTraffic(ctx context.Context, window time.Duration) ([]WireStat, error) {
	window = clampWindow(window)
	aggs, err := b.st.WireAggregates(ctx, time.Now().Add(-window))
	if err != nil {
		return nil, err
	}
	wires := map[string]bool{}
	for _, w := range b.topo.Wires {
		wires[w.ID] = true
	}
	var out []WireStat
	for _, a := range aggs {
		id := a.Source + ">" + a.Target
		if !wires[id] || a.Count == 0 {
			continue
		}
		out = append(out, WireStat{WireID: id, Rate: float64(a.Count) / window.Seconds(),
			ErrorRate: float64(a.Failed) / float64(a.Count), P95ms: a.P95ms})
	}
	return out, nil
}
