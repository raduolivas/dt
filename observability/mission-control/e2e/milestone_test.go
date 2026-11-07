//go:build mce2e

// Package e2e proves the Mission Control milestone against the real local stack: for each break scenario,
// Mission Control names the right hop and reason within 40 s of the break.
//
// It is a dev tool, run by `make mc-e2e` after `make dev`, `make mc` and `make mc-sim` (the sandbox key in
// observability/mission-control/.env, MC_SIMULATION=1). It is not part of `make ci` and not part of mc-backend
// (scenarios 5 and 6 call scripts/mc-break.sh, which uses Docker; the backend never does).
//
// Env: MC_URL (default http://127.0.0.1:8095), MC_E2E_ONLY (comma list of scenarios, default 1,2,3,4,5,6; the
// trace check always runs).
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// detectBudget is how long after the break a finding must appear (spec 9; scenario 1 takes ~35 s live: the
// 30 s courier-stale threshold plus the 5 s detector tick).
const detectBudget = 40 * time.Second

// clearBudget is how long a healed finding may stay active (detectors re-evaluate every 5 s over a window).
const clearBudget = 120 * time.Second

// scenario4Reason is what DT emits when a reassigned courier keeps sending fixes (ruling R36): the old courier's
// active set is empty, so tracking drops them as no_active_delivery. DT never reports delivery_reassigned.
const scenario4Reason = "no_active_delivery"

type event struct {
	ID         int64          `json:"id"`
	ObservedAt time.Time      `json:"observedAt"`
	Kind       string         `json:"kind"`
	Source     string         `json:"source"`
	Target     string         `json:"target"`
	Outcome    string         `json:"outcome"`
	Reason     string         `json:"reason"`
	DeliveryID string         `json:"deliveryId"`
	TraceID    string         `json:"traceId"`
	Attrs      map[string]any `json:"attrs"`
}

func (e event) rule() string { s, _ := e.Attrs["rule"].(string); return s }
func (e event) span() string { s, _ := e.Attrs["span_name"].(string); return s }
func (e event) body() string { s, _ := e.Attrs["body"].(string); return s }

type run struct {
	DeliveryID string `json:"delivery_id"`
	OrderRef   string `json:"order_ref"`
	PickedUp   bool   `json:"picked_up"`
}

type env struct {
	t    *testing.T
	base string
	root string
}

func (e *env) do(method, path string, body any, out any) int {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.base+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode < 300 && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			e.t.Fatalf("%s %s: decode: %v: %.200s", method, path, err, b)
		}
	}
	return resp.StatusCode
}

// until polls f every 1 s until it returns true or d passes; it returns how long it took.
func until(d time.Duration, f func() bool) (time.Duration, bool) {
	start := time.Now()
	for {
		if f() {
			return time.Since(start), true
		}
		if time.Since(start) >= d {
			return time.Since(start), false
		}
		time.Sleep(time.Second)
	}
}

func (e *env) newRun(query string) run {
	e.t.Helper()
	var r run
	if code := e.do("POST", "/api/sim/runs"+query, nil, &r); code != 201 {
		e.t.Fatalf("create sim run: status %d (is MC_SIMULATION=1 on? run make mc-sim)", code)
	}
	return r
}

func (e *env) orderFindings(id string) []event {
	var o struct {
		Detectors []event `json:"detectors"`
	}
	e.do("GET", "/api/orders/"+id, nil, &o)
	return o.Detectors
}

func (e *env) systemWarnings() []event {
	var s struct {
		Warnings []event `json:"warnings"`
	}
	e.do("GET", "/api/system", nil, &s)
	return s.Warnings
}

// find returns the newest finding of the rule and reason at one of the hops, stored at or after since, when it
// is still active. The order panel lists cleared findings too (a clear is a newer row with outcome "cleared").
func find(list []event, since time.Time, rule, reason string, hops ...string) (event, bool) {
	var newest event
	for _, f := range list {
		if f.rule() != rule || f.Reason != reason || f.ObservedAt.Before(since) {
			continue
		}
		for _, h := range hops {
			if f.Target == h && f.ID >= newest.ID {
				newest = f
			}
		}
	}
	return newest, newest.ID != 0 && newest.Outcome != "cleared"
}

func (e *env) script(args ...string) string {
	e.t.Helper()
	cmd := exec.Command(filepath.Join(e.root, "observability/mission-control/scripts/mc-break.sh"), args...)
	cmd.Dir = e.root
	cmd.Env = append(os.Environ(), "MC_URL="+e.base)
	out, err := cmd.CombinedOutput()
	if err != nil {
		e.t.Fatalf("mc-break.sh %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func selected(n int) bool {
	only := os.Getenv("MC_E2E_ONLY")
	if only == "" {
		return true
	}
	for _, s := range strings.Split(only, ",") {
		if strings.TrimSpace(s) == fmt.Sprint(n) {
			return true
		}
	}
	return false
}

func TestMilestone(t *testing.T) {
	e := &env{t: t, base: strings.TrimRight(os.Getenv("MC_URL"), "/"), root: "../../.."}
	if e.base == "" {
		e.base = "http://127.0.0.1:8095"
	}
	if abs, err := filepath.Abs(e.root); err == nil {
		e.root = abs
	}
	var st struct {
		Enabled bool  `json:"enabled"`
		Runs    []run `json:"runs"`
	}
	if code := e.do("GET", "/api/sim/status", nil, &st); code != 200 || !st.Enabled {
		t.Fatalf("simulation is not enabled at %s (status %d): run make dev, make mc, make mc-sim first", e.base, code)
	}

	// A full pass starts three runs (base, scenario 4's held run, scenario 6's routing run) and mc-backend allows 8
	// live ones; make mc-sim restarts it and clears them.
	if len(st.Runs) > 5 {
		t.Fatalf("%d simulated runs are live (cap 8, this test needs 3 more): run make mc-sim to restart mc-backend", len(st.Runs))
	}

	// A sim run is found by its order number, and its timeline holds the whole chain on one trace.
	r := e.newRun("")
	t.Logf("run %s order %s", r.DeliveryID, r.OrderRef)
	took, ok := until(30*time.Second, func() bool {
		var hits []struct {
			DeliveryID string `json:"deliveryId"`
			Origin     string `json:"origin"`
		}
		e.do("GET", "/api/orders?q="+r.OrderRef, nil, &hits)
		return len(hits) == 1 && hits[0].DeliveryID == r.DeliveryID && hits[0].Origin == "simulated"
	})
	if !ok {
		t.Fatalf("order %s not found (as simulated) after %s", r.OrderRef, took)
	}
	t.Logf("order found after %s", took.Round(time.Second))
	checkTrace(t, e, r)

	if selected(1) {
		t.Run("1_phone_stops", func(t *testing.T) {
			e.t = t
			quiet(t, func() bool {
				_, still := find(e.orderFindings(r.DeliveryID), time.Time{}, "courier_stale", "no_accepted_fix", "courier")
				return !still
			})
			start := time.Now()
			defer e.do("POST", "/api/sim/runs/"+r.DeliveryID+"/heal", nil, nil) // never leave the run broken
			e.do("POST", "/api/sim/runs/"+r.DeliveryID+"/break", map[string]int{"scenario": 1}, nil)
			took, ok := until(detectBudget, func() bool {
				_, ok := find(e.orderFindings(r.DeliveryID), start, "courier_stale", "no_accepted_fix", "courier")
				return ok
			})
			t.Logf("scenario 1: courier_stale at courier after %s (budget %s)", took.Round(time.Second), detectBudget)
			if !ok {
				t.Errorf("no courier_stale finding at hop courier on the order within %s", detectBudget)
			}
			e.do("POST", "/api/sim/runs/"+r.DeliveryID+"/heal", nil, nil)
			cleared(t, func() bool {
				_, still := find(e.orderFindings(r.DeliveryID), start, "courier_stale", "no_accepted_fix", "courier")
				return !still
			})
		})
	}
	for _, c := range []struct {
		n      int
		name   string
		reason string
	}{{2, "2_invalid_token", "unauthorized"}, {3, "3_rate_limit", "rate_limited"}} {
		if !selected(c.n) {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			e.t = t
			// 401/429 carry no delivery id (ruling R37): the signal is a system warning at the gateway.
			quiet(t, func() bool {
				_, still := find(e.systemWarnings(), time.Time{}, "rejected_at_gateway", c.reason, "gateway.courier_api")
				return !still
			})
			start := time.Now()
			defer e.do("POST", "/api/sim/runs/"+r.DeliveryID+"/heal", nil, nil)
			e.do("POST", "/api/sim/runs/"+r.DeliveryID+"/break", map[string]int{"scenario": c.n}, nil)
			took, ok := until(detectBudget, func() bool {
				_, ok := find(e.systemWarnings(), start, "rejected_at_gateway", c.reason, "gateway.courier_api")
				return ok
			})
			t.Logf("scenario %d: rejected_at_gateway %s after %s", c.n, c.reason, took.Round(time.Second))
			if !ok {
				t.Errorf("no rejected_at_gateway/%s warning at gateway.courier_api within %s", c.reason, detectBudget)
			}
			e.do("POST", "/api/sim/runs/"+r.DeliveryID+"/heal", nil, nil)
			cleared(t, func() bool {
				_, still := find(e.systemWarnings(), start, "rejected_at_gateway", c.reason, "gateway.courier_api")
				return !still
			})
		})
	}
	if selected(4) {
		t.Run("4_courier_reassigned", func(t *testing.T) {
			e.t = t
			// The script makes its own held run (DT reassigns only before pickup), then reassigns its courier.
			quiet(t, func() bool {
				_, still := find(e.systemWarnings(), time.Time{}, "fixes_dropped", scenario4Reason, "tracking.ingest")
				return !still
			})
			start := time.Now() // counted from before the script: it makes the held run, waits 6 s, then reassigns
			defer e.script("4", "heal")
			t.Log(e.script("4"))
			took, ok := until(detectBudget, func() bool {
				_, ok := find(e.systemWarnings(), start, "fixes_dropped", scenario4Reason, "tracking.ingest")
				return ok
			})
			t.Logf("scenario 4: fixes_dropped %s at tracking.ingest after %s", scenario4Reason, took.Round(time.Second))
			if !ok {
				t.Errorf("no fixes_dropped/%s warning at tracking.ingest within %s", scenario4Reason, detectBudget)
			}
			t.Log(e.script("4", "heal"))
			cleared(t, func() bool {
				_, still := find(e.systemWarnings(), start, "fixes_dropped", scenario4Reason, "tracking.ingest")
				return !still
			})
		})
	}
	if selected(5) {
		t.Run("5_redis_paused", func(t *testing.T) {
			e.t = t
			isRedisAny := func() bool {
				_, ok := find(e.systemWarnings(), time.Time{}, "dependency_failing", "calls_failing", "tracking.state", "redis", "tracking.state>redis")
				return !ok
			}
			quiet(t, isRedisAny)
			start := time.Now()
			defer e.script("5", "heal") // never leave DT broken, whatever the assertions say
			t.Log(e.script("5"))
			isRedis := func(list []event) bool {
				_, ok := find(list, start, "dependency_failing", "calls_failing", "tracking.state", "redis", "tracking.state>redis")
				return ok
			}
			took, ok := until(detectBudget, func() bool { return isRedis(e.systemWarnings()) })
			t.Logf("scenario 5: dependency_failing at tracking.state/redis after %s", took.Round(time.Second))
			if !ok {
				t.Errorf("no dependency_failing finding at tracking.state/redis within %s; warnings: %s", detectBudget, dump(e.systemWarnings()))
			}
			t.Log(e.script("5", "heal"))
			cleared(t, func() bool { return !isRedis(e.systemWarnings()) })
		})
	}
	if selected(6) {
		t.Run("6_osrm_stopped", func(t *testing.T) {
			e.t = t
			quiet(t, func() bool {
				_, ok := find(e.systemWarnings(), time.Time{}, "dependency_failing", "route_fallback", "osrm")
				return !ok
			})
			start := time.Now()
			// The script stops OSRM and forces a routing call with a fresh run (ruling R28).
			defer e.script("6", "heal") // OSRM must come back even when the break fails half way
			t.Log(e.script("6"))
			isOSRM := func(list []event) bool {
				_, ok := find(list, start, "dependency_failing", "route_fallback", "osrm")
				return ok
			}
			took, ok := until(detectBudget, func() bool { return isOSRM(e.systemWarnings()) })
			t.Logf("scenario 6: dependency_failing route_fallback at osrm after %s", took.Round(time.Second))
			if !ok {
				t.Errorf("no dependency_failing/route_fallback finding at osrm within %s; warnings: %s", detectBudget, dump(e.systemWarnings()))
			}
			t.Log(e.script("6", "heal"))
			cleared(t, func() bool { return !isOSRM(e.systemWarnings()) })
		})
	}
}

// quiet waits until no such finding is active, so a finding left over from an earlier scenario (or run) cannot
// pass for this break: an ongoing finding keeps its first timestamp.
func quiet(t *testing.T, gone func() bool) {
	t.Helper()
	if _, ok := until(clearBudget, gone); !ok {
		t.Fatalf("a finding from before this scenario is still active after %s", clearBudget)
	}
}

func cleared(t *testing.T, gone func() bool) {
	t.Helper()
	took, ok := until(clearBudget, gone)
	if !ok {
		t.Errorf("finding still active %s after healing", clearBudget)
		return
	}
	t.Logf("healed: finding cleared after %s", took.Round(time.Second))
}

func dump(evs []event) string {
	var parts []string
	for _, f := range evs {
		parts = append(parts, f.rule()+"/"+f.Reason+"@"+f.Target)
	}
	return strings.Join(parts, ", ")
}

// checkTrace waits for the viewer fan-out (it follows pickup, which the sim confirms early) and asserts the
// timeline holds the creation, a status change and one trace shared by the gateway courier span, tracking.ingest
// and tracking.viewer_fanout.
func checkTrace(t *testing.T, e *env, r run) {
	t.Helper()
	type seen struct{ gateway, ingest, fanout bool }
	var problem string
	_, ok := until(45*time.Second, func() bool {
		var tl struct {
			Events []event `json:"events"`
		}
		e.do("GET", "/api/orders/"+r.DeliveryID+"/timeline?limit=1000", nil, &tl)
		created, status := false, false
		traces := map[string]*seen{}
		for _, ev := range tl.Events {
			if ev.Kind == "log" && ev.body() == "delivery_created" {
				created = true
			}
			if ev.Kind == "state_change" && ev.span() == "deliveries.assign" {
				status = true
			}
			if ev.TraceID == "" {
				continue
			}
			s := traces[ev.TraceID]
			if s == nil {
				s = &seen{}
				traces[ev.TraceID] = s
			}
			switch {
			case ev.Source == "gateway.courier_api" && ev.span() == "POST /v1/courier/locations":
				s.gateway = true
			case ev.span() == "tracking.ingest":
				s.ingest = true
			case ev.span() == "tracking.viewer_fanout":
				s.fanout = true
			}
		}
		for _, s := range traces {
			if s.gateway && s.ingest && s.fanout && created && status {
				return true
			}
		}
		problem = fmt.Sprintf("created=%v status=%v events=%d traces=%d", created, status, len(tl.Events), len(traces))
		return false
	})
	if !ok {
		t.Fatalf("timeline lacks DeliveryCreated, a status change and one trace holding the gateway courier span, tracking.ingest and tracking.viewer_fanout (%s)", problem)
	}
}
