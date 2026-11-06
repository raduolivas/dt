package metrics

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/api"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/metrics/links"
)

// TrafficWindow is the window /api/system reports traffic over.
const TrafficWindow = 5 * time.Minute

// Traffic adapts a Source to api.TrafficSource.
func Traffic(s Source) api.TrafficSource { return traffic{s} }

type traffic struct{ s Source }

func (t traffic) Traffic(ctx context.Context) ([]api.WireTraffic, error) {
	stats, err := t.s.WireTraffic(ctx, TrafficWindow)
	if err != nil {
		return nil, err
	}
	out := make([]api.WireTraffic, 0, len(stats))
	for _, w := range stats {
		n := math.Round(w.Rate * TrafficWindow.Seconds())
		out = append(out, api.WireTraffic{Wire: w.WireID, Count: int64(n), Failed: int64(math.Round(n * w.ErrorRate)),
			Rate: w.Rate, ErrorRate: w.ErrorRate, P95Ms: w.P95ms})
	}
	return out, nil
}

// Links adapts link templates to api.LinkSource.
func Links(templates map[string]string) api.LinkSource { return linkSource(templates) }

type linkSource map[string]string

func (l linkSource) Links(componentID, traceID string) []api.Link {
	if len(l) == 0 {
		return nil
	}
	service, _, _ := strings.Cut(componentID, ".")
	vars := map[string]string{"trace_id": traceID, "component": componentID, "service": service}
	var out []api.Link
	for _, k := range links.Kinds() {
		for _, x := range links.Render(l, k, vars) {
			out = append(out, api.Link{Label: x.Label, URL: x.URL})
		}
	}
	return out
}
