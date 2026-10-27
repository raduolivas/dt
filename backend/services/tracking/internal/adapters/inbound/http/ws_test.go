package httpapi_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/raduolivas/dt/backend/pkg/viewerjwt"
	httpapi "github.com/raduolivas/dt/backend/services/tracking/internal/adapters/inbound/http"
	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

func TestStreamRequiresAuthFirstMessage(t *testing.T) {
	e := newEnv(t)

	// Silence: closed 4401 after the auth timeout.
	start := time.Now()
	mustClose(t, e.dial(), 4401, 3*time.Second)
	if d := time.Since(start); d < 400*time.Millisecond {
		t.Fatalf("closed after %v, before the auth timeout", d)
	}

	// Anything but auth first.
	c := e.dial()
	send(t, c, map[string]string{"type": "hello"})
	mustClose(t, c, 4401, 3*time.Second)

	// A bad token, an expired token, a token of another signer.
	for name, tok := range map[string]string{
		"garbage": "not.a.jwt",
		"expired": func() string { s, _, _ := e.signer.Sign(claimsA, time.Now().Add(-time.Hour)); return s }(),
		"foreign": func() string { o := newSigner(t); s, _, _ := o.Sign(claimsA, time.Now()); return s }(),
	} {
		c := e.dial()
		auth(t, c, tok)
		m, code := read(t, c, 3*time.Second)
		if code != 4401 {
			t.Fatalf("%s token: message %s close %d, want 4401", name, m.Type, code)
		}
	}

	// A token whose tenant is not the delivery's: refused, nothing leaks.
	other := claimsA
	other.TenantID = "ten_b"
	c = e.dial()
	auth(t, c, e.token(other))
	mustClose(t, c, 4401, 3*time.Second)

	// Oversized messages are refused.
	c = e.dial()
	send(t, c, map[string]string{"type": "auth", "token": strings.Repeat("x", 8<<10)})
	if _, code := read(t, c, 3*time.Second); code != websocket.StatusMessageTooBig {
		t.Fatalf("oversized: close %d", code)
	}

	// The right token: the current snapshot.
	c = e.dial()
	auth(t, c, e.token(claimsA))
	if s := mustRead(t, c).snapshot(t); s.Version != 10 || s.DeliveryID != delA {
		t.Fatalf("first snapshot = %+v", s)
	}
}

func TestStreamGetsSnapshotsInVersionOrder(t *testing.T) {
	e := newEnv(t)
	c := e.dial()
	auth(t, c, e.token(claimsA))
	if v := mustRead(t, c).snapshot(t).Version; v != 10 {
		t.Fatalf("initial version %d", v)
	}
	e.publish(snapshot(delA, 9, domain.StatusPickedUp, true)) // older than the initial one
	e.publish(snapshot(delA, 12, domain.StatusPickedUp, true))
	e.publish(snapshot(delA, 11, domain.StatusPickedUp, true)) // late
	e.publish(snapshot(delA, 13, domain.StatusArriving, true))
	if v := mustRead(t, c).snapshot(t).Version; v != 12 {
		t.Fatalf("got version %d, want 12", v)
	}
	if v := mustRead(t, c).snapshot(t).Version; v != 13 {
		t.Fatalf("got version %d, want 13", v)
	}
	m := mustRead(t, c)
	var st struct {
		Status domain.Status `json:"status"`
	}
	if m.Type != "status" || json.Unmarshal(m.Data, &st) != nil || st.Status != domain.StatusArriving {
		t.Fatalf("status message = %s %s", m.Type, m.Data)
	}
	// Another delivery's snapshots never reach this stream.
	e.publish(snapshot(delB, 99, domain.StatusPickedUp, true))
	e.publish(snapshot(delA, 14, domain.StatusArriving, true))
	if s := mustRead(t, c).snapshot(t); s.DeliveryID != delA || s.Version != 14 {
		t.Fatalf("got %+v", s)
	}
}

func TestTokenSwapKeepsStream(t *testing.T) {
	e := newEnv(t)
	c := e.dial()
	auth(t, c, e.token(claimsA))
	mustRead(t, c)
	time.Sleep(150 * time.Millisecond)
	auth(t, c, e.token(claimsA)) // a refreshed token for the same link
	time.Sleep(150 * time.Millisecond)
	e.publish(snapshot(delA, 20, domain.StatusPickedUp, true))
	if v := mustRead(t, c).snapshot(t).Version; v != 20 {
		t.Fatalf("after swap: version %d", v)
	}

	// A token for another link of the same delivery: closed.
	time.Sleep(150 * time.Millisecond)
	otherLink := claimsA
	otherLink.LinkID = linkB
	auth(t, c, e.token(otherLink))
	mustClose(t, c, 4401, 3*time.Second)

	// A token for another delivery: closed.
	e.set(delB, scA, snapshot(delB, 1, domain.StatusPickedUp, false))
	c = e.dial()
	auth(t, c, e.token(claimsA))
	mustRead(t, c)
	time.Sleep(150 * time.Millisecond)
	auth(t, c, e.token(viewerjwt.Claims{DeliveryID: delB, TenantID: scA.TenantID, Env: scA.Env, LinkID: linkA}))
	mustClose(t, c, 4401, 3*time.Second)

	// An invalid token in a swap: closed.
	c = e.dial()
	auth(t, c, e.token(claimsA))
	mustRead(t, c)
	time.Sleep(150 * time.Millisecond)
	auth(t, c, "garbage")
	mustClose(t, c, 4401, 3*time.Second)

	// Swaps faster than the swap interval: a policy violation.
	c = e.dial()
	auth(t, c, e.token(claimsA))
	mustRead(t, c)
	auth(t, c, e.token(claimsA))
	auth(t, c, e.token(claimsA))
	mustClose(t, c, websocket.StatusPolicyViolation, 3*time.Second)
}

func TestRotationClosesOpenStreams(t *testing.T) {
	e := newEnv(t)
	a1, a2, b := e.dial(), e.dial(), e.dial()
	auth(t, a1, e.token(claimsA))
	auth(t, a2, e.token(claimsA))
	newLink := claimsA
	newLink.LinkID = linkB
	auth(t, b, e.token(newLink))
	for _, c := range []*websocket.Conn{a1, a2, b} {
		mustRead(t, c)
	}

	e.revoke(linkA)
	start := time.Now()
	for _, c := range []*websocket.Conn{a1, a2} {
		if m := mustRead(t, c); m.Type != "revoked" {
			t.Fatalf("last message %s, want revoked", m.Type)
		}
		mustClose(t, c, 4401, time.Second)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("closed after %v, want within 1 s", d)
	}
	// The new link's stream is untouched.
	e.publish(snapshot(delA, 30, domain.StatusPickedUp, true))
	if v := mustRead(t, b).snapshot(t).Version; v != 30 {
		t.Fatalf("new link stream: version %d", v)
	}
	// The old link's token is deny-listed for new streams too.
	c := e.dial()
	auth(t, c, e.token(claimsA))
	if m := mustRead(t, c); m.Type != "revoked" {
		t.Fatalf("revoked token: %s", m.Type)
	}
	mustClose(t, c, 4401, time.Second)
	waitStreams(t, e.hub, 1)
}

func TestFinalStateHidesCourierThenClosesAtExpiry(t *testing.T) {
	e := newEnv(t)
	c := e.dial()
	auth(t, c, e.token(claimsA))
	if s := mustRead(t, c).snapshot(t); s.Courier == nil {
		t.Fatal("picked_up direct: courier hidden")
	}
	// A final snapshot: no courier, even if one were published by mistake.
	final := snapshot(delA, 40, domain.StatusDelivered, true)
	final.Route.ToPickup = []domain.Pair{{52.36, 4.90}}
	e.publish(final)
	s := mustRead(t, c).snapshot(t)
	if s.Status != domain.StatusDelivered || s.Courier != nil || s.Route.ToPickup != nil {
		t.Fatalf("final snapshot leaks the courier: %+v", s)
	}
	if m := mustRead(t, c); m.Type != "status" {
		t.Fatalf("want status, got %s", m.Type)
	}

	// The link expires (30 min after final; here, in about two seconds).
	at := time.Now().Add(2 * time.Second).Truncate(time.Second)
	e.expire(linkA, at)
	m, code := read(t, c, 5*time.Second)
	if code != -1 || m.Type != "revoked" {
		t.Fatalf("at expiry: message %s close %d, want revoked", m.Type, code)
	}
	if now := time.Now(); now.Before(at) {
		t.Fatalf("closed at %v, before the expiry %v", now, at)
	}
	mustClose(t, c, 4401, time.Second)

	// After the expiry a new stream is refused at once.
	c = e.dial()
	auth(t, c, e.token(claimsA))
	if m := mustRead(t, c); m.Type != "revoked" {
		t.Fatalf("expired link: %s", m.Type)
	}
	mustClose(t, c, 4401, time.Second)

	// A stream opened before the announcement reads nothing, but one opened
	// after reads the expiry key and closes at it.
	e.set(delB, scA, snapshot(delB, 1, domain.StatusDelivered, false))
	cb := viewerjwt.Claims{DeliveryID: delB, TenantID: scA.TenantID, Env: scA.Env, LinkID: linkB}
	at2 := time.Now().Add(2 * time.Second).Truncate(time.Second)
	e.rdb.Set(t.Context(), viewerjwt.ExpiryKey(linkB), at2.Unix(), time.Minute)
	c = e.dial()
	auth(t, c, e.token(cb))
	mustRead(t, c)
	if m := mustRead(t, c); m.Type != "revoked" || time.Now().Before(at2) {
		t.Fatalf("stream after the announcement: %s at %v (expiry %v)", m.Type, time.Now(), at2)
	}
}

func TestSSEMatchesWS(t *testing.T) {
	e := newEnv(t)
	tok := e.token(claimsA)
	ws := e.dial()
	auth(t, ws, tok)
	events, res := e.sse(tok)
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "text/event-stream" ||
		res.Header.Get("Access-Control-Allow-Origin") != "*" || res.Header.Get("Access-Control-Allow-Credentials") != "" ||
		res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("sse response: %d %v", res.StatusCode, res.Header)
	}
	var fromWS, fromSSE []msg
	fromWS = append(fromWS, mustRead(t, ws))
	m, _ := next(t, events)
	fromSSE = append(fromSSE, m)

	e.publish(snapshot(delA, 11, domain.StatusPickedUp, true))
	e.publish(snapshot(delA, 12, domain.StatusArriving, true))
	for range 3 {
		fromWS = append(fromWS, mustRead(t, ws))
		m, _ := next(t, events)
		fromSSE = append(fromSSE, m)
	}
	e.revoke(linkA)
	fromWS = append(fromWS, mustRead(t, ws))
	m, _ = next(t, events)
	fromSSE = append(fromSSE, m)
	if _, open := next(t, events); open {
		t.Fatal("SSE stream still open after revoked")
	}
	mustClose(t, ws, 4401, time.Second)

	if len(fromWS) != len(fromSSE) {
		t.Fatalf("ws %d messages, sse %d", len(fromWS), len(fromSSE))
	}
	for i := range fromWS {
		if fromWS[i].Type != fromSSE[i].Type || string(fromWS[i].Data) != string(fromSSE[i].Data) {
			t.Fatalf("message %d: ws %s %s, sse %s %s", i, fromWS[i].Type, fromWS[i].Data, fromSSE[i].Type, fromSSE[i].Data)
		}
	}
	want := []string{"snapshot", "snapshot", "snapshot", "status", "revoked"}
	for i, w := range want {
		if fromWS[i].Type != w {
			t.Fatalf("message %d = %s, want %s", i, fromWS[i].Type, w)
		}
	}

	// Without a valid bearer token: 401 problem, CORS still readable.
	_, res = e.sse("")
	if res.StatusCode != 401 || res.Header.Get("Content-Type") != "application/problem+json" ||
		res.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("sse without token: %d %v", res.StatusCode, res.Header)
	}
	_, res = e.sse(tok) // revoked
	if res.StatusCode != 401 {
		t.Fatalf("sse with a revoked token: %d", res.StatusCode)
	}
}

func TestStreamCapPerLink(t *testing.T) {
	e := newEnv(t)
	tok := e.token(claimsA)
	var open []*websocket.Conn
	for range 5 {
		c := e.dial()
		auth(t, c, tok)
		mustRead(t, c)
		open = append(open, c)
	}
	c := e.dial()
	auth(t, c, tok)
	mustClose(t, c, websocket.StatusTryAgainLater, 3*time.Second)
	// SSE counts too.
	if _, res := e.sse(tok); res.StatusCode != 429 {
		t.Fatalf("sse over the link cap: %d", res.StatusCode)
	}
	// Another link is not affected.
	other := claimsA
	other.LinkID = linkB
	c = e.dial()
	auth(t, c, e.token(other))
	mustRead(t, c)
	_ = c.Close(websocket.StatusNormalClosure, "")

	// Closing one frees its slot (no leak on close).
	_ = open[0].Close(websocket.StatusNormalClosure, "")
	waitStreams(t, e.hub, 4)
	c = e.dial()
	auth(t, c, tok)
	mustRead(t, c)
}

func TestStreamCapPerInstance(t *testing.T) {
	e := newEnv(t, func(d *httpapi.Deps) { d.MaxStreams = 2 })
	// A failed auth releases its slot.
	for range 3 {
		c := e.dial()
		auth(t, c, "garbage")
		mustClose(t, c, 4401, 3*time.Second)
	}
	waitStreams(t, e.hub, 0)
	a, b := e.dial(), e.dial()
	auth(t, a, e.token(claimsA))
	auth(t, b, e.token(claimsA))
	mustRead(t, a)
	mustRead(t, b)
	c := e.dial()
	mustClose(t, c, websocket.StatusTryAgainLater, 3*time.Second)
	if _, res := e.sse(e.token(claimsA)); res.StatusCode != 503 {
		t.Fatalf("sse over the instance cap: %d", res.StatusCode)
	}
	_ = a.CloseNow()
	_ = b.CloseNow()
	waitStreams(t, e.hub, 0)
}

func TestShutdownClosesWithReconnectHint(t *testing.T) {
	e := newEnv(t)
	c := e.dial()
	auth(t, c, e.token(claimsA))
	mustRead(t, c)
	events, _ := e.sse(e.token(claimsA))
	next(t, events)
	e.stop()
	mustClose(t, c, websocket.StatusServiceRestart, 3*time.Second)
	for {
		if _, open := next(t, events); !open {
			break
		}
	}
	if err := <-e.done; err != nil {
		t.Fatalf("Run = %v", err)
	}
	e.done <- nil // for the cleanup
	waitStreams(t, e.hub, 0)
}

func TestSlowClientDropsOldestSnapshots(t *testing.T) {
	e := newEnv(t)
	c := e.dial()
	auth(t, c, e.token(claimsA))
	mustRead(t, c)
	// The client reads nothing while 200 snapshots go out; the hub never
	// blocks (the other stream keeps up) and the slow one gets the latest.
	fast := e.dial()
	auth(t, fast, e.token(claimsA))
	mustRead(t, fast)
	for v := int64(100); v < 300; v++ {
		e.publish(snapshot(delA, v, domain.StatusPickedUp, true))
	}
	var last int64
	for last != 299 {
		last = mustRead(t, fast).snapshot(t).Version
	}
	last = 0
	for last != 299 {
		v := mustRead(t, c).snapshot(t).Version
		if v <= last {
			t.Fatalf("out of order: %d after %d", v, last)
		}
		last = v
	}
}

func waitStreams(t *testing.T, h *httpapi.Hub, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for h.Streams() != want {
		if time.Now().After(deadline) {
			t.Fatalf("streams = %d, want %d", h.Streams(), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func newSigner(t *testing.T) *viewerjwt.Signer {
	t.Helper()
	s, err := viewerjwt.ParseKeys("k1:"+strings.Repeat("A", 43)+"=", "k1")
	if err != nil {
		t.Fatal(err)
	}
	return s
}
