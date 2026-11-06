// Package metrics reads aggregate wire traffic from a configurable source
// (spec §4.6): the builtin mc_event aggregate, Prometheus, or CloudWatch.
package metrics

import (
	"context"
	"errors"
	"time"
)

// ErrNotImplemented is returned by adapters that are staging work (CloudWatch).
var ErrNotImplemented = errors.New("metrics: source not implemented")

// ErrNoData means the source answered but had no per-wire series.
var ErrNoData = errors.New("metrics: no data")

// MaxWindow bounds every query window.
const MaxWindow = time.Hour

// WireStat is aggregate traffic on one wire over a window.
type WireStat struct {
	WireID    string
	Rate      float64 // events per second
	ErrorRate float64 // failed / total, 0..1
	P95ms     float64
}

// Source supplies aggregate numbers per wire.
type Source interface {
	WireTraffic(ctx context.Context, window time.Duration) ([]WireStat, error)
	Name() string
}

// Flag receives the metrics_source_unavailable self-health flag.
type Flag interface{ MetricsSourceUnavailable(bool) }

func clampWindow(w time.Duration) time.Duration {
	if w <= 0 || w > MaxWindow {
		return MaxWindow
	}
	return w
}

type fallback struct {
	primary, builtin Source
	flag             Flag
}

// WithFallback returns primary's answer, or builtin's when primary fails; the
// flag (may be nil) is set while primary is failing.
func WithFallback(primary, builtin Source, flag Flag) Source {
	return fallback{primary, builtin, flag}
}

func (f fallback) WireTraffic(ctx context.Context, w time.Duration) ([]WireStat, error) {
	out, err := f.primary.WireTraffic(ctx, w)
	if err == nil {
		if f.flag != nil {
			f.flag.MetricsSourceUnavailable(false)
		}
		return out, nil
	}
	if f.flag != nil {
		f.flag.MetricsSourceUnavailable(true)
	}
	return f.builtin.WireTraffic(ctx, w)
}

func (f fallback) Name() string { return f.primary.Name() }
