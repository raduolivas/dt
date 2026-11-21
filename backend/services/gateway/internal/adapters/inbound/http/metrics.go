package httpapi

import (
	"context"
	"strings"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/raduolivas/dt/backend/pkg/otelx"
)

// meterName is the instrumentation scope of this package's metrics.
const meterName = "github.com/raduolivas/dt/backend/services/gateway/internal/adapters/inbound/http"

// Rate-limit surfaces, the only values of the surface label.
const (
	surfaceCourier = "courier"
	surfaceViewer  = "viewer"
	surfacePartner = "partner"
)

// Refresh outcomes, the only values of the outcome label.
const (
	refreshOK      = "ok"
	refreshReuse   = "reuse"
	refreshExpired = "expired"
	refreshInvalid = "invalid"
)

// surfaceOf names the API surface a request path belongs to. It looks at the
// path only, never at ids or anything else in the request.
func surfaceOf(path string) string {
	switch {
	case strings.HasPrefix(path, "/v1/courier/"):
		return surfaceCourier
	case strings.HasPrefix(path, "/v1/tracking/"):
		return surfaceViewer
	default:
		return surfacePartner
	}
}

type gatewayMetrics struct {
	rejections metric.Int64Counter
	started    metric.Int64Counter
	refresh    metric.Int64Counter
}

var (
	metricsMu sync.Mutex
	cur       *gatewayMetrics
	// newMeter returns the meter for this package's metrics; tests replace it.
	newMeter = func() metric.Meter { return otelx.Meter(meterName) }
)

// instruments creates the counters once. With no provider installed they are
// no-ops, so recording never fails or blocks a request.
func instruments() *gatewayMetrics {
	metricsMu.Lock()
	defer metricsMu.Unlock()
	if cur != nil {
		return cur
	}
	m := newMeter()
	// Counter creation only fails on an invalid name; the nil-safe no-op
	// fallback below keeps a telemetry fault out of the request path.
	rej, _ := m.Int64Counter("dt.ratelimit.rejections",
		metric.WithDescription("Requests refused with 429, by API surface."))
	st, _ := m.Int64Counter("dt.courier.sessions.started",
		metric.WithDescription("Courier sessions started, by mode."))
	rf, _ := m.Int64Counter("dt.courier.refresh",
		metric.WithDescription("Courier token refresh attempts, by outcome."))
	cur = &gatewayMetrics{rejections: rej, started: st, refresh: rf}
	return cur
}

func countRejection(ctx context.Context, path string) {
	instruments().rejections.Add(ctx, 1, metric.WithAttributes(attribute.String("surface", surfaceOf(path))))
}

// countSessionStarted takes the mode of a started session ("sdk" or "link").
func countSessionStarted(ctx context.Context, mode string) {
	instruments().started.Add(ctx, 1, metric.WithAttributes(attribute.String("mode", mode)))
}

func countRefresh(ctx context.Context, outcome string) {
	instruments().refresh.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}
