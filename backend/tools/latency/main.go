// Command latency measures how long a courier fix takes to reach a viewer.
//
// It creates end_to_end deliveries (each with its own courier), opens viewers
// on some of them over SSE, and drives fixes through the courier relay at 1 Hz
// per courier. Each fix's t is recorded; the latency of a fix is the time the
// viewer received the snapshot whose courier.position_at equals that t, minus
// the time the relay answered the request that carried the fix. It prints the
// p50, p95 and p99 and exits non-zero when the p95 is 1000 ms or more.
//
// Usage:
//
//	latency -api http://127.0.0.1:8080 -stream http://127.0.0.1:8082 -key dt_sk_test_...
//	        [-deliveries 20] [-viewers 5] [-duration 2m]
//
// The key may also come from DT_API_KEY. It must be a test key of a tenant
// that allows the end_to_end model, so the courier is visible from assignment.
// Amsterdam coordinates are used, so the stack's OSRM data covers them.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// maxP95 is the budget: the exit status is non-zero at or above it.
const maxP95 = time.Second

type percentiles struct {
	N             int
	P50, P95, P99 time.Duration
}

func (p percentiles) ok() bool { return p.P95 < maxP95 }

// summarize computes nearest-rank percentiles of the sample without
// reordering it.
func summarize(sample []time.Duration) (percentiles, error) {
	if len(sample) == 0 {
		return percentiles{}, errors.New("no latency samples")
	}
	s := append([]time.Duration(nil), sample...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	rank := func(p float64) time.Duration {
		i := int(math.Ceil(p/100*float64(len(s)))) - 1
		return s[max(0, min(i, len(s)-1))]
	}
	return percentiles{N: len(s), P50: rank(50), P95: rank(95), P99: rank(99)}, nil
}

type client struct {
	api  string
	key  string
	run  string
	http *http.Client
}

// do sends a JSON request to the API and decodes a 2xx answer into out.
func (c *client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.api+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method == http.MethodPost {
		// Fresh per call: the gateway needs the header on POSTs (the relay ignores it).
		req.Header.Set("Idempotency-Key", fmt.Sprintf("lat-%s-%d", c.run, time.Now().UnixNano()))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(raw)))
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

type target struct {
	idx       int
	courierID string
	delivery  string
	linkToken string
}

// setup creates a courier and an end_to_end delivery, and assigns the courier.
func (c *client) setup(ctx context.Context, i int) (target, error) {
	t := target{idx: i}
	var cou struct{ ID string }
	if err := c.do(ctx, http.MethodPut, fmt.Sprintf("/v1/couriers/lat-%s-%d", c.run, i),
		map[string]any{"display_name": fmt.Sprintf("Latency %d", i), "vehicle": "e_bike"}, &cou); err != nil {
		return t, err
	}
	t.courierID = cou.ID
	var del struct {
		ID       string
		Tracking *struct{ Token string }
	}
	if err := c.do(ctx, http.MethodPost, "/v1/deliveries", map[string]any{
		"external_id":    fmt.Sprintf("lat-%s-%d", c.run, i),
		"delivery_model": "end_to_end",
		"pickup":         map[string]any{"location": map[string]any{"lat": 52.3731, "lng": 4.8922}, "name": "Latency pickup"},
		"dropoff":        map[string]any{"location": map[string]any{"lat": 52.3600, "lng": 4.8852}, "label": "Latency dropoff"},
	}, &del); err != nil {
		return t, err
	}
	t.delivery = del.ID
	if del.Tracking != nil {
		t.linkToken = del.Tracking.Token
	}
	err := c.do(ctx, http.MethodPost, "/v1/deliveries/"+del.ID+"/assignment", map[string]any{"courier_id": cou.ID}, nil)
	return t, err
}

// clock records, per delivery and fix time, when the relay answered and when a
// viewer first saw the fix.
type clock struct {
	mu       sync.Mutex
	answered map[string]time.Time // key: delivery|t
	seen     map[string]time.Time
}

func newClock() *clock {
	return &clock{answered: map[string]time.Time{}, seen: map[string]time.Time{}}
}

func fixKey(delivery string, t time.Time) string {
	return fmt.Sprintf("%s|%d", delivery, t.UnixMilli())
}

func (k *clock) answer(delivery string, t, at time.Time) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.answered[fixKey(delivery, t)] = at
}

func (k *clock) see(delivery string, t, at time.Time) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, ok := k.seen[fixKey(delivery, t)]; !ok {
		k.seen[fixKey(delivery, t)] = at
	}
}

// latencies pairs every fix a viewer saw with the relay's answer for it.
func (k *clock) latencies() []time.Duration {
	k.mu.Lock()
	defer k.mu.Unlock()
	var out []time.Duration
	for key, seen := range k.seen {
		if ans, ok := k.answered[key]; ok {
			out = append(out, seen.Sub(ans))
		}
	}
	return out
}

// drive posts one fix per second for the courier until ctx ends.
func (c *client) drive(ctx context.Context, t target, k *clock, fails *counter) {
	const stepDeg = 0.000072 // about 8 m north per fix
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var last time.Time
	for n := 0; ; n++ {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		fixT := time.Now().UTC().Truncate(time.Second)
		if fixT.Equal(last) {
			continue
		}
		last = fixT
		// A triangle wave along a street-sized line, so a long run stays plausible.
		off := math.Abs(float64(n%2000) - 1000)
		fix := map[string]any{
			"t": fixT.Format(time.RFC3339), "lat": 52.3676 + off*stepDeg, "lng": 4.9000 + float64(t.idx)*0.0015,
			"accuracy_m": 5, "speed_mps": 8, "heading_deg": 0,
		}
		var res struct{ Accepted, Dropped int }
		err := c.do(ctx, http.MethodPost, "/v1/couriers/"+t.courierID+"/locations", map[string]any{"fixes": []any{fix}}, &res)
		answered := time.Now()
		if err != nil || res.Accepted != 1 {
			if ctx.Err() == nil {
				fails.add(fmt.Sprintf("relay %s: accepted=%d err=%v", t.delivery, res.Accepted, err))
			}
			continue
		}
		k.answer(t.delivery, fixT, answered)
	}
}

type counter struct {
	mu    sync.Mutex
	n     int
	first string
}

func (c *counter) add(msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n == 0 {
		c.first = msg
	}
	c.n++
}

// watch opens a viewer session for the delivery and records each snapshot's
// courier.position_at as it arrives over SSE.
func (c *client) watch(ctx context.Context, stream string, t target, k *clock, ready chan<- error) {
	viewerToken, err := c.session(ctx, t.linkToken)
	if err != nil {
		ready <- fmt.Errorf("viewer session: %w", err)
		return
	}
	sreq, _ := http.NewRequestWithContext(ctx, http.MethodGet, stream+"/v1/tracking/stream/sse", nil)
	sreq.Header.Set("Authorization", "Bearer "+viewerToken)
	sreq.Header.Set("Accept", "text/event-stream")
	// No overall timeout: the stream lives for the whole run.
	sresp, err := (&http.Client{}).Do(sreq)
	if err != nil {
		ready <- fmt.Errorf("viewer stream: %w", err)
		return
	}
	defer func() { _ = sresp.Body.Close() }()
	if sresp.StatusCode != http.StatusOK {
		ready <- fmt.Errorf("viewer stream: %s", sresp.Status)
		return
	}
	ready <- nil
	sc := bufio.NewScanner(sresp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var event string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			at := time.Now()
			if event != "snapshot" {
				continue
			}
			var m struct {
				Data struct {
					Courier *struct {
						PositionAt *time.Time `json:"position_at"`
					} `json:"courier"`
				} `json:"data"`
			}
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &m) == nil &&
				m.Data.Courier != nil && m.Data.Courier.PositionAt != nil {
				k.see(t.delivery, *m.Data.Courier.PositionAt, at)
			}
		case line == "":
			event = ""
		}
	}
}

// session exchanges a tracking link token for a viewer token.
func (c *client) session(ctx context.Context, link string) (string, error) {
	b, _ := json.Marshal(map[string]string{"token": link})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.api+"/v1/tracking/session", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		ViewerToken string `json:"viewer_token"`
	}
	err = decodeOK(resp, &out)
	return out.ViewerToken, err
}

func decodeOK(resp *http.Response, out any) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	return json.Unmarshal(raw, out)
}

func main() {
	api := flag.String("api", "http://127.0.0.1:8080", "gateway base URL")
	stream := flag.String("stream", "http://127.0.0.1:8082", "tracking service base URL (viewer streams)")
	key := flag.String("key", os.Getenv("DT_API_KEY"), "test secret key (default $DT_API_KEY)")
	deliveries := flag.Int("deliveries", 20, "deliveries (one courier each), all driven at 1 Hz")
	viewers := flag.Int("viewers", 5, "viewers, on the first deliveries")
	duration := flag.Duration("duration", 2*time.Minute, "how long to drive fixes")
	flag.Parse()
	if *key == "" || *deliveries < 1 || *viewers < 1 || *viewers > *deliveries || *duration < 5*time.Second {
		fmt.Fprintln(os.Stderr, "usage: latency -key <test key> [-api URL] [-stream URL] [-deliveries N] [-viewers N<=deliveries] [-duration 2m]")
		os.Exit(2)
	}
	if err := run(*api, *stream, *key, *deliveries, *viewers, *duration); err != nil {
		fmt.Fprintln(os.Stderr, "latency:", err)
		os.Exit(1)
	}
}

func run(api, stream, key string, deliveries, viewers int, duration time.Duration) error {
	c := &client{api: strings.TrimRight(api, "/"), key: key, run: time.Now().UTC().Format("20060102T150405"),
		http: &http.Client{Timeout: 10 * time.Second}}
	stream = strings.TrimRight(stream, "/")
	ctx := context.Background()

	targets := make([]target, deliveries)
	for i := range targets {
		t, err := c.setup(ctx, i)
		if err != nil {
			return fmt.Errorf("setup %d: %w", i, err)
		}
		targets[i] = t
	}
	fmt.Fprintf(os.Stderr, "created %d deliveries\n", deliveries)

	k := newClock()
	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	ready := make(chan error, viewers)
	var watchers sync.WaitGroup
	for i := 0; i < viewers; i++ {
		if targets[i].linkToken == "" {
			return fmt.Errorf("delivery %s has no tracking link", targets[i].delivery)
		}
		watchers.Add(1)
		go func() { defer watchers.Done(); c.watch(watchCtx, stream, targets[i], k, ready) }()
	}
	for i := 0; i < viewers; i++ {
		if err := <-ready; err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "%d viewers connected; driving fixes for %s\n", viewers, duration)

	driveCtx, stopDrive := context.WithTimeout(ctx, duration)
	defer stopDrive()
	var fails counter
	var drivers sync.WaitGroup
	for _, t := range targets {
		drivers.Add(1)
		go func() { defer drivers.Done(); c.drive(driveCtx, t, k, &fails) }()
	}
	drivers.Wait()
	time.Sleep(3 * time.Second) // the last snapshots
	stopWatch()
	watchers.Wait()

	if fails.n > 0 {
		fmt.Fprintf(os.Stderr, "%d relay failures, first: %s\n", fails.n, fails.first)
	}
	p, err := summarize(k.latencies())
	if err != nil {
		return err
	}
	fmt.Printf("samples %d\np50 %d ms\np95 %d ms\np99 %d ms\n", p.N, p.P50.Milliseconds(), p.P95.Milliseconds(), p.P99.Milliseconds())
	if !p.ok() {
		return fmt.Errorf("p95 %d ms is not under %d ms", p.P95.Milliseconds(), maxP95.Milliseconds())
	}
	if fails.n > 0 {
		return fmt.Errorf("%d relay failures", fails.n)
	}
	return nil
}
