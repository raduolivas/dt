package httpapi_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raduolivas/dt/backend/pkg/apikey"
	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/pkg/logx"
	"github.com/raduolivas/dt/backend/pkg/viewerjwt"
	httpapi "github.com/raduolivas/dt/backend/services/gateway/internal/adapters/inbound/http"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app/apptest"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

// viewerNow is the viewer edge's clock (fixedClock).
var viewerNow = fixedClock{}.Now()

const snapJSON = `{"delivery_id":"` + delID + `","delivery_model":"direct","status":"confirmed","courier":null,` +
	`"pickup":{"location":{"lat":-23.5614,"lng":-46.6559},"name":"Pizzaria Central"},` +
	`"dropoff":{"location":{"lat":-23.5505,"lng":-46.6333},"label":null},` +
	`"route":{"to_pickup":null,"to_dropoff":[]},"eta_seconds":null,"eta_window":null,"eta_state":"calculating",` +
	`"eta_quality":"osrm","distance_remaining_m":null,"progress":null,` +
	`"timeline":[{"status":"confirmed","at":"2026-10-20T11:59:00Z"}],"updated_at":"2026-10-20T12:00:00Z","version":3}`

type fakeSnapshots struct {
	mu  sync.Mutex
	err error
}

func (f *fakeSnapshots) Snapshot(context.Context, domain.TrackingLink) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return json.RawMessage(snapJSON), nil
}

// burstLimiterMem allows Burst requests per key and then denies (no refill).
type burstLimiterMem struct {
	mu sync.Mutex
	n  map[string]int
}

func (b *burstLimiterMem) Allow(_ context.Context, key string, l app.Limit) (app.Decision, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.n[key]++
	if b.n[key] > l.Burst {
		return app.Decision{Limit: l.Burst, RetryAfter: 6 * time.Second, ResetAfter: time.Minute}, nil
	}
	return app.Decision{Allowed: true, Limit: l.Burst, Remaining: l.Burst - b.n[key]}, nil
}

type viewerEdge struct {
	t      *testing.T
	h      http.Handler
	links  *app.TrackingLinks
	snaps  *fakeSnapshots
	signer *viewerjwt.Signer
	logs   *bytes.Buffer
}

func testSigner(t *testing.T) *viewerjwt.Signer {
	t.Helper()
	seed := make([]byte, 32)
	_, _ = rand.Read(seed)
	s, err := viewerjwt.ParseKeys("v1:"+base64.StdEncoding.EncodeToString(seed), "v1")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// newViewerEdge mounts the partner API and the viewer routes on one mux
// behind the outer middleware main uses.
func newViewerEdge(t *testing.T, lim app.Limiter) *viewerEdge {
	t.Helper()
	links := &app.TrackingLinks{Store: apptest.NewMemLinks(), Hasher: apptest.Hasher{}, Revoker: &apptest.MemRevoker{}, Clock: fixedClock{}}
	s := testSigner(t)
	snaps := &fakeSnapshots{}
	logs := &bytes.Buffer{}
	log := logx.New(logs, slog.LevelDebug, "gateway")
	mux := http.NewServeMux()
	httpapi.Mount(mux, httpapi.Routes(httpapi.Deps{}), httpapi.Middleware{
		Auth: &app.Authenticator{Keys: &store{byLookup: map[string]domain.KeyRecord{}}, Hasher: hasher{}, Clock: fixedClock{}}, Log: log,
	})
	httpapi.MountViewer(mux, httpapi.ViewerDeps{
		Viewer:  &app.Viewer{Links: links, Tokens: app.SignerTokens{Signer: s, Verifier: s.Verifier()}, Snapshots: snaps, Limiter: lim},
		Limiter: lim, JWKS: s.JWKS(), Clock: fixedClock{}, Log: log,
	})
	h := httpx.RequestID(httpx.AccessLog(log)(httpx.Recover(log)(mux)))
	return &viewerEdge{t: t, h: h, links: links, snaps: snaps, signer: s, logs: logs}
}

func (v *viewerEdge) do(r *http.Request) *httptest.ResponseRecorder {
	if r.RemoteAddr == "192.0.2.1:1234" {
		r.RemoteAddr = peer
	}
	rec := httptest.NewRecorder()
	v.h.ServeHTTP(rec, r)
	return rec
}

func (v *viewerEdge) issue(t *testing.T) (domain.TrackingLink, string) {
	t.Helper()
	l, tok, err := v.links.Issue(context.Background(), domain.Principal{TenantID: tenantID, Env: domain.EnvTest}, delID)
	if err != nil {
		t.Fatal(err)
	}
	return l, tok
}

func sessionReq(tok string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/tracking/session", strings.NewReader(`{"token":"`+tok+`"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://shop.example")
	return r
}

func refreshReq(jwt string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/tracking/session/refresh", nil)
	r.Header.Set("Authorization", "Bearer "+jwt)
	r.Header.Set("Origin", "https://shop.example")
	return r
}

type sessionBody struct {
	ViewerToken string          `json:"viewer_token"`
	ExpiresAt   time.Time       `json:"expires_at"`
	Snapshot    json.RawMessage `json:"snapshot"`
	Branding    *string         `json:"branding"`
}

func TestSessionExchangesLinkTokenForViewerToken(t *testing.T) {
	t.Parallel()
	v := newViewerEdge(t, nil)
	l, tok := v.issue(t)
	req := sessionReq(tok)
	rec := v.do(req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	checkResponse(t, req, rec)
	var b sessionBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if !b.ExpiresAt.Equal(viewerNow.Add(15*time.Minute)) || b.Branding != nil || !bytes.Equal(b.Snapshot, []byte(snapJSON)) {
		t.Fatalf("body = %s", rec.Body.String())
	}
	c, err := v.signer.Verifier().Verify(context.Background(), b.ViewerToken, viewerNow)
	if err != nil || c.LinkID != l.ID || c.DeliveryID != delID || c.TenantID != tenantID || c.Env != "test" {
		t.Fatalf("claims = %+v, %v", c, err)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" || rec.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("CORS headers = %v", rec.Header())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
	// Neither token is ever logged.
	if strings.Contains(v.logs.String(), tok) || strings.Contains(v.logs.String(), b.ViewerToken) {
		t.Fatalf("token in logs: %s", v.logs.String())
	}
}

func TestSessionErrors(t *testing.T) {
	t.Parallel()
	v := newViewerEdge(t, nil)
	_, tok := v.issue(t)
	unknown, _ := domain.NewLinkToken()

	rec := v.do(sessionReq(unknown))
	wantProblem(t, rec, http.StatusUnauthorized, "link_invalid")
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("problem without CORS header: the browser could not read it")
	}
	wantProblem(t, v.do(sessionReq("short")), http.StatusBadRequest, "validation_failed")

	big := httptest.NewRequest(http.MethodPost, "/v1/tracking/session",
		strings.NewReader(`{"token":"`+tok+`","pad":"`+strings.Repeat("x", 1100)+`"}`))
	big.Header.Set("Content-Type", "application/json")
	wantProblem(t, v.do(big), http.StatusRequestEntityTooLarge, "payload_too_large")

	v.snaps.err = errors.New("tracking down")
	wantProblem(t, v.do(sessionReq(tok)), http.StatusServiceUnavailable, "tracking_unavailable")
	v.snaps.err = nil

	// A secret key authenticates nothing on viewer routes.
	k := mint(t, apikey.Test, actor, domain.AllScopes...)
	r := refreshReq(k.raw)
	r.Header.Del("Origin")
	wantProblem(t, v.do(r), http.StatusUnauthorized, "viewer_token_invalid")
	r = httptest.NewRequest(http.MethodPost, "/v1/tracking/session/refresh", nil)
	wantProblem(t, v.do(r), http.StatusUnauthorized, "viewer_token_invalid")
	if strings.Contains(v.logs.String(), k.raw) {
		t.Fatal("secret key in logs")
	}
}

func TestViewerCORSPreflight(t *testing.T) {
	t.Parallel()
	v := newViewerEdge(t, nil)
	for _, path := range []string{"/v1/tracking/session", "/v1/tracking/session/refresh"} {
		r := httptest.NewRequest(http.MethodOptions, path, nil)
		r.Header.Set("Origin", "https://partner-app.example")
		r.Header.Set("Access-Control-Request-Method", "POST")
		r.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
		rec := v.do(r)
		h := rec.Header()
		if rec.Code != http.StatusNoContent || h.Get("Access-Control-Allow-Origin") != "*" ||
			h.Get("Access-Control-Allow-Methods") != "POST, GET, OPTIONS" ||
			h.Get("Access-Control-Allow-Headers") != "Authorization, Content-Type" ||
			h.Get("Access-Control-Allow-Credentials") != "" || h.Get("Access-Control-Max-Age") == "" {
			t.Fatalf("%s preflight = %d %v", path, rec.Code, h)
		}
	}
	// The partner API still refuses browsers.
	r := httptest.NewRequest(http.MethodOptions, "/v1/deliveries", nil)
	r.Header.Set("Origin", "https://partner-app.example")
	rec := v.do(r)
	wantProblem(t, rec, http.StatusForbidden, "cors_not_allowed")
	assertNoCORSHeaders(t, rec)
}

func TestSessionRateLimitedPerIPAndLink(t *testing.T) {
	t.Parallel()
	v := newViewerEdge(t, &burstLimiterMem{n: map[string]int{}})
	_, tok := v.issue(t)

	// 10 per IP per minute, whatever the token.
	unknown, _ := domain.NewLinkToken()
	for i := range 10 {
		r := sessionReq(unknown)
		r.RemoteAddr = "203.0.113.5:1000"
		if rec := v.do(r); rec.Code != http.StatusUnauthorized {
			t.Fatalf("call %d = %d", i, rec.Code)
		}
	}
	r := sessionReq(tok)
	r.RemoteAddr = "203.0.113.5:1000"
	rec := v.do(r)
	wantProblem(t, rec, http.StatusTooManyRequests, "rate_limited")
	if rec.Header().Get("Retry-After") != "6" || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("429 headers = %v", rec.Header())
	}

	// 30 per link per minute, across IPs (refresh shares the link bucket).
	var jwt string
	for i := range 30 {
		r := sessionReq(tok)
		r.RemoteAddr = "198.51.100." + strconv.Itoa(i+1) + ":1000"
		rec := v.do(r)
		if rec.Code != http.StatusOK {
			t.Fatalf("link call %d = %d: %s", i, rec.Code, rec.Body.String())
		}
		var b sessionBody
		_ = json.Unmarshal(rec.Body.Bytes(), &b)
		jwt = b.ViewerToken
	}
	r = sessionReq(tok)
	r.RemoteAddr = "198.51.100.200:1000"
	wantProblem(t, v.do(r), http.StatusTooManyRequests, "rate_limited")
	r = refreshReq(jwt)
	r.RemoteAddr = "198.51.100.201:1000"
	wantProblem(t, v.do(r), http.StatusTooManyRequests, "rate_limited")
}

func TestRefreshFailsAfterRotation(t *testing.T) {
	t.Parallel()
	v := newViewerEdge(t, nil)
	_, tok := v.issue(t)
	rec := v.do(sessionReq(tok))
	var b sessionBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}

	req := refreshReq(b.ViewerToken)
	rec = v.do(req)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh = %d: %s", rec.Code, rec.Body.String())
	}
	checkResponse(t, req, rec)
	var nb struct {
		ViewerToken string    `json:"viewer_token"`
		ExpiresAt   time.Time `json:"expires_at"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &nb)
	if nb.ViewerToken == "" || !nb.ExpiresAt.Equal(viewerNow.Add(15*time.Minute)) {
		t.Fatalf("refresh body = %s", rec.Body.String())
	}

	if _, _, err := v.links.Rotate(context.Background(), domain.Principal{TenantID: tenantID, Env: domain.EnvTest}, delID); err != nil {
		t.Fatal(err)
	}
	for _, jwt := range []string{b.ViewerToken, nb.ViewerToken} {
		wantProblem(t, v.do(refreshReq(jwt)), http.StatusUnauthorized, "viewer_token_invalid")
	}
	wantProblem(t, v.do(sessionReq(tok)), http.StatusUnauthorized, "link_invalid")
}

func TestJWKSRoute(t *testing.T) {
	t.Parallel()
	v := newViewerEdge(t, nil)
	rec := v.do(httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "public, max-age=300" ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") || rec.Body.String() != string(v.signer.JWKS()) {
		t.Fatalf("jwks = %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
}
