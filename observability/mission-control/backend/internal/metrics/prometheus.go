package metrics

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/config"
)

//go:embed promql.yaml
var promqlRaw []byte

// promTimeout keeps a dead Prometheus from stalling /api/system.
const promTimeout = 2 * time.Second

type promQueries struct {
	Rate      string `yaml:"rate"`
	ErrorRate string `yaml:"error_rate"`
	P95ms     string `yaml:"p95_ms"`
}

type prometheus struct {
	base string
	hc   *http.Client
	q    promQueries
}

func newPrometheus(base string) Source {
	p := &prometheus{base: strings.TrimRight(base, "/"), hc: &http.Client{Timeout: promTimeout}}
	if err := yaml.Unmarshal(promqlRaw, &p.q); err != nil {
		panic("metrics: promql.yaml: " + err.Error()) // embedded file, covered by tests
	}
	return p
}

func (*prometheus) Name() string { return config.MetricsPrometheus }

func (p *prometheus) WireTraffic(ctx context.Context, window time.Duration) ([]WireStat, error) {
	if p.base == "" {
		return nil, errors.New("metrics: MC_PROMETHEUS_URL not set")
	}
	win := strconv.Itoa(int(clampWindow(window).Seconds())) + "s"
	// One deadline for the whole call; the three queries run concurrently.
	ctx, cancel := context.WithTimeout(ctx, promTimeout)
	defer cancel()
	var errs, p95 map[string]float64
	var wg sync.WaitGroup
	wg.Add(2)
	// Error rate and p95 are optional per wire: a failed query leaves 0.
	go func() { defer wg.Done(); errs, _ = p.vector(ctx, p.q.ErrorRate, win) }()
	go func() { defer wg.Done(); p95, _ = p.vector(ctx, p.q.P95ms, win) }()
	rate, err := p.vector(ctx, p.q.Rate, win)
	wg.Wait()
	if err != nil {
		return nil, err
	}
	if len(rate) == 0 {
		return nil, ErrNoData
	}
	out := make([]WireStat, 0, len(rate))
	for id, r := range rate {
		out = append(out, WireStat{WireID: id, Rate: r, ErrorRate: errs[id], P95ms: p95[id]})
	}
	return out, nil
}

// vector runs one instant query and returns the value per `wire` label.
func (p *prometheus) vector(ctx context.Context, tpl, win string) (map[string]float64, error) {
	q := strings.ReplaceAll(tpl, "{{window}}", win)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"/api/v1/query?"+url.Values{"query": {q}}.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("metrics: prometheus request: %w", err)
	}
	resp, err := p.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("metrics: prometheus: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metrics: prometheus status %d", resp.StatusCode)
	}
	var body struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]any            `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("metrics: prometheus decode: %w", err)
	}
	if body.Status != "success" {
		return nil, fmt.Errorf("metrics: prometheus status %q", body.Status)
	}
	out := map[string]float64{}
	for _, r := range body.Data.Result {
		id := r.Metric["wire"]
		s, _ := r.Value[1].(string)
		v, err := strconv.ParseFloat(s, 64)
		if id == "" || err != nil || v != v { // skip unlabeled, unparsable, NaN
			continue
		}
		out[id] = v
	}
	return out, nil
}
