package httpapi_test

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	trackingv1 "github.com/raduolivas/dt/backend/gen/dt/tracking/v1"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

const relayBody = `{"fixes":[{"t":"2026-10-22T14:15:03Z","lat":-23.5612,"lng":-46.6557,"accuracy_m":6.5,"speed_mps":4.8,"heading_deg":84},` +
	`{"t":"2026-10-22T14:15:06Z","lat":-23.5611,"lng":-46.6551,"accuracy_m":7}]}`

func relayTarget() string { return "/v1/couriers/" + couID + "/locations" }

// keyRecorder is a limiter that counts the calls per key and, per key, allows
// at most Burst of them (no refill): the first Burst calls of a key pass.
type keyRecorder struct {
	mu   sync.Mutex
	n    map[string]int
	deny func(key string) bool
}

func newKeyRecorder() *keyRecorder { return &keyRecorder{n: map[string]int{}} }

func (k *keyRecorder) Allow(_ context.Context, key string, l app.Limit) (app.Decision, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.n[key]++
	if k.n[key] > l.Burst || (k.deny != nil && k.deny(key)) {
		return app.Decision{Limit: l.Burst, RetryAfter: time.Second, ResetAfter: 5 * time.Second}, nil
	}
	return app.Decision{Allowed: true, Limit: l.Burst, Remaining: l.Burst - k.n[key]}, nil
}

func (k *keyRecorder) count(key string) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.n[key]
}

func (k *keyRecorder) keys() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	var out []string
	for key := range k.n {
		out = append(out, key)
	}
	return out
}

func TestRelayForwardsFixesToTracking(t *testing.T) {
	t.Parallel()
	g := newTrackGW(t, nil)
	req := g.req(http.MethodPost, relayTarget(), relayBody)
	rec := g.do(req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	checkResponse(t, req, rec)
	if rec.Body.String() != `{"accepted":2,"dropped":1}`+"\n" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	in, ok := g.track.last().(*trackingv1.IngestRequest)
	if !ok {
		t.Fatalf("request = %T", g.track.last())
	}
	sc := in.GetScope()
	if sc.GetTenantId() != tenantID || sc.GetEnvironment() != eventsv1.Environment_ENVIRONMENT_TEST || sc.GetActorId() != actor {
		t.Fatalf("scope = %v", sc)
	}
	if in.GetCourierId() != couID || in.GetSource() != "relay" || len(in.GetFixes()) != 2 {
		t.Fatalf("request = %v", in)
	}
	f0, f1 := in.GetFixes()[0], in.GetFixes()[1]
	if !f0.GetT().AsTime().Equal(time.Date(2026, 10, 22, 14, 15, 3, 0, time.UTC)) || f0.GetLat() != -23.5612 ||
		f0.GetLng() != -46.6557 || f0.GetAccuracyM() != 6.5 || f0.GetSpeedMps() != 4.8 || f0.GetHeadingDeg() != 84 {
		t.Fatalf("fix 0 = %v", f0)
	}
	if f1.SpeedMps != nil || f1.HeadingDeg != nil {
		t.Fatalf("fix 1 carries speed or heading it did not send: %v", f1)
	}
}

// TestRelayIsNotAudited: location fixes are tracking data with the trail's
// own retention, not account or configuration writes, so the relay writes no
// audit row, whatever its outcome; other writes on the same edge still do.
func TestRelayIsNotAudited(t *testing.T) {
	t.Parallel()
	g := newTrackGW(t, nil)
	if rec := g.do(g.req(http.MethodPost, relayTarget(), relayBody)); rec.Code != http.StatusOK {
		t.Fatalf("relay = %d: %s", rec.Code, rec.Body.String())
	}
	g.track.failing = rpcFail(connect.CodeNotFound, "not_found")
	wantProblem(t, g.do(g.req(http.MethodPost, relayTarget(), relayBody)), http.StatusNotFound, "not_found")
	k := newTrackGW(t, nil, domain.ScopeDeliveriesRead)
	wantProblem(t, k.do(k.req(http.MethodPost, relayTarget(), relayBody)), http.StatusForbidden, "insufficient_scope")
	if rows := append(g.audit.all(), k.audit.all()...); len(rows) != 0 {
		t.Fatalf("relay audited: %+v", rows)
	}
	// The hook is mounted: another write is audited.
	g.do(g.req(http.MethodPost, "/v1/deliveries/"+delID+"/cancel", `{"reason":"Customer cancelled the order"}`))
	if rows := g.audit.all(); len(rows) != 1 || rows[0].Action != "deliveries.cancel" {
		t.Fatalf("audit = %+v", rows)
	}
}

func TestRelayErrors(t *testing.T) {
	t.Parallel()
	g := newTrackGW(t, nil)

	// Unlike other POSTs the relay takes no Idempotency-Key (its contract
	// declares none): without one it passes, and a repeated one is never
	// replayed from a stored record.
	r := g.req(http.MethodPost, relayTarget(), relayBody)
	r.Header.Del("Idempotency-Key")
	if rec := g.do(r); rec.Code != http.StatusOK {
		t.Fatalf("relay without Idempotency-Key = %d: %s", rec.Code, rec.Body.String())
	}
	for range 2 {
		r = g.req(http.MethodPost, relayTarget(), relayBody)
		r.Header.Set("Idempotency-Key", "same")
		if rec := g.do(r); rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "" {
			t.Fatalf("relay with a repeated key = %d %v", rec.Code, rec.Header())
		}
	}
	if g.track.count() != 3 {
		t.Fatalf("tracking calls = %d, want 3", g.track.count())
	}
	g = newTrackGW(t, nil)

	// Contract violations never reach tracking.
	for name, body := range map[string]string{
		"no fixes":     `{"fixes":[]}`,
		"too many":     `{"fixes":[` + strings.TrimSuffix(strings.Repeat(`{"t":"2026-10-22T14:15:03Z","lat":1,"lng":1,"accuracy_m":1},`, 51), ",") + `]}`,
		"bad latitude": `{"fixes":[{"t":"2026-10-22T14:15:03Z","lat":91,"lng":1,"accuracy_m":1}]}`,
		"no time":      `{"fixes":[{"lat":1,"lng":1,"accuracy_m":1}]}`,
		"extra field":  `{"fixes":[{"t":"2026-10-22T14:15:03Z","lat":1,"lng":1,"accuracy_m":1}],"x":1}`,
	} {
		wantProblem(t, g.do(g.req(http.MethodPost, relayTarget(), body)), http.StatusBadRequest, "validation_failed")
		if g.track.count() != 0 {
			t.Fatalf("%s: tracking called", name)
		}
	}
	// {id} must be a courier id.
	wantProblem(t, g.do(g.req(http.MethodPost, "/v1/couriers/"+delID+"/locations", relayBody)), http.StatusBadRequest, "validation_failed")

	// The scope: couriers:write.
	k := newTrackGW(t, nil, domain.ScopeDeliveriesRead)
	wantProblem(t, k.do(k.req(http.MethodPost, relayTarget(), relayBody)), http.StatusForbidden, "insufficient_scope")

	// Tracking's answers.
	g.track.failing = rpcFail(connect.CodeNotFound, "not_found")
	wantProblem(t, g.do(g.req(http.MethodPost, relayTarget(), relayBody)), http.StatusNotFound, "not_found")
	g.track.failing = rpcFail(connect.CodeUnavailable, "tracking_unavailable")
	wantProblem(t, g.do(g.req(http.MethodPost, relayTarget(), relayBody)), http.StatusServiceUnavailable, "tracking_unavailable")
	g.track.failing = rpcFail(connect.CodeUnavailable, "")
	wantProblem(t, g.do(g.req(http.MethodPost, relayTarget(), relayBody)), http.StatusServiceUnavailable, "service_unavailable")
}

func TestRelayUsesOwnBucket(t *testing.T) {
	t.Parallel()
	lim := newKeyRecorder()
	g := newTrackGW(t, lim)

	// 5 calls pass (burst 5 per courier), the 6th within the second is 429.
	for i := range 5 {
		rec := g.do(g.req(http.MethodPost, relayTarget(), relayBody))
		if rec.Code != http.StatusOK {
			t.Fatalf("relay call %d = %d: %s", i+1, rec.Code, rec.Body.String())
		}
		// The headers describe the courier's bucket, the one a client hits.
		if rec.Header().Get("RateLimit-Limit") != "5" || rec.Header().Get("RateLimit-Remaining") != strconv.Itoa(4-i) {
			t.Fatalf("relay call %d headers = %v", i+1, rec.Header())
		}
	}
	rec := g.do(g.req(http.MethodPost, relayTarget(), relayBody))
	wantProblem(t, rec, http.StatusTooManyRequests, "rate_limited")
	if rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	if g.track.count() != 5 {
		t.Fatalf("tracking calls = %d, want 5 (the 429 never reaches it)", g.track.count())
	}

	// The relay never touches the partner key bucket...
	for _, key := range lim.keys() {
		if strings.HasPrefix(key, "rl:sk:") {
			t.Fatalf("relay charged %s", key)
		}
	}
	if lim.count("rl:relay:cou:"+couID) != 6 || lim.count("rl:relay:ten:"+tenantID+":test") != 5 {
		t.Fatalf("buckets = %v", lim.n)
	}
	// ...so the key's own API calls are unaffected.
	if rec := g.do(g.req(http.MethodGet, "/v1/me", "")); rec.Code != http.StatusOK {
		t.Fatalf("/v1/me = %d", rec.Code)
	}
	if lim.count("rl:sk:"+actor) != 1 {
		t.Fatalf("keys = %v", lim.keys())
	}
	// Another courier has its own bucket.
	other := "/v1/couriers/cou_01j9z3k8m2q4r6s8t0v2w4x6z9/locations"
	if rec := g.do(g.req(http.MethodPost, other, relayBody)); rec.Code != http.StatusOK {
		t.Fatalf("other courier = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRelayTenantCeiling(t *testing.T) {
	t.Parallel()
	lim := newKeyRecorder()
	lim.deny = func(key string) bool { return strings.HasPrefix(key, "rl:relay:ten:") }
	g := newTrackGW(t, lim)
	wantProblem(t, g.do(g.req(http.MethodPost, relayTarget(), relayBody)), http.StatusTooManyRequests, "rate_limited")
	if g.track.count() != 0 {
		t.Fatal("tracking called after the tenant ceiling")
	}
	// A path that is no courier id cannot mint bucket keys.
	g.do(g.req(http.MethodPost, "/v1/couriers/not-an-id/locations", relayBody))
	for _, key := range lim.keys() {
		if strings.Contains(key, "not-an-id") {
			t.Fatalf("bucket key from an unvalidated path: %s", key)
		}
	}
}

func TestRelayBodyLimit16KB(t *testing.T) {
	t.Parallel()
	g := newTrackGW(t, nil)
	pad := func(n int) string { // a body of exactly n bytes
		head, tail := `{"fixes":[{"t":"2026-10-22T14:15:03Z","lat":1,"lng":1,"accuracy_m":1}],"pad":"`, `"}`
		return head + strings.Repeat("x", n-len(head)-len(tail)) + tail
	}
	// 17 KB is under the general 64 KiB limit but over the relay's 16 KB.
	for _, n := range []int{16<<10 + 1, 17 << 10, 60 << 10} {
		wantProblem(t, g.do(g.req(http.MethodPost, relayTarget(), pad(n))), http.StatusRequestEntityTooLarge, "payload_too_large")
	}
	// Exactly 16 KB passes the size check (the extra field is then refused
	// by the contract, not by the size limit).
	wantProblem(t, g.do(g.req(http.MethodPost, relayTarget(), pad(16<<10))), http.StatusBadRequest, "validation_failed")
	if g.track.count() != 0 {
		t.Fatal("tracking called")
	}
	// The other routes keep the 64 KiB limit.
	big := `{"external_id":"o","notes":"` + strings.Repeat("x", 17<<10) + `"}`
	if rec := g.do(g.req(http.MethodPost, "/v1/deliveries", big)); rec.Code == http.StatusRequestEntityTooLarge {
		t.Fatalf("deliveries.create has the relay limit: %s", rec.Body.String())
	}
}
