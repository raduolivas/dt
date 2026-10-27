package httpapi_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/gorillamux"

	"github.com/raduolivas/dt/backend/pkg/apikey"
	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/pkg/logx"
	"github.com/raduolivas/dt/backend/services/gateway/api"
	httpapi "github.com/raduolivas/dt/backend/services/gateway/internal/adapters/inbound/http"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

const (
	tenantID = "ten_01j9z3k8m2q4r6s8t0v2w4x6y8"
	peer     = "198.51.100.9:41000"
)

var pepper = []byte("test-only-pepper-0123456789abcdef")

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC) }

// store is an in-memory KeyStore that counts lookups.
type store struct {
	mu       sync.Mutex
	byLookup map[string]domain.KeyRecord
	err      error
	lookups  int
}

func (s *store) FindByLookup(_ context.Context, lookup string) (domain.KeyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lookups++
	if s.err != nil {
		return domain.KeyRecord{}, s.err
	}
	r, ok := s.byLookup[lookup]
	if !ok {
		return domain.KeyRecord{}, app.ErrNotFound
	}
	return r, nil
}

func (s *store) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lookups
}

type hasher struct{}

func (hasher) Verify(plaintext string, hash []byte) bool {
	return apikey.Verify(pepper, plaintext, hash)
}

type testKey struct {
	raw, lookup string
	rec         domain.KeyRecord
}

// mint creates a key with the given env, id and scopes.
func mint(t *testing.T, env apikey.Environment, id string, scopes ...domain.Scope) testKey {
	t.Helper()
	k, err := apikey.Generate(env)
	if err != nil {
		t.Fatal(err)
	}
	return testKey{raw: k.Plaintext, lookup: k.Lookup, rec: domain.KeyRecord{
		ID: id, TenantID: tenantID, Lookup: k.Lookup, Env: domain.Environment(env),
		Hash: apikey.Hash(pepper, k.Plaintext), Scopes: scopes,
	}}
}

type edge struct {
	t     *testing.T
	store *store
	logs  *bytes.Buffer
	h     http.Handler
}

type option func(*httpapi.Middleware, *[]httpapi.Route)

func trusted(cidrs string) option {
	return func(m *httpapi.Middleware, _ *[]httpapi.Route) {
		p, err := httpapi.ParseTrustedProxies(cidrs)
		if err != nil {
			panic(err)
		}
		m.TrustedProxies = p
	}
}

func withRoutes(rs ...httpapi.Route) option {
	return func(_ *httpapi.Middleware, routes *[]httpapi.Route) { *routes = rs }
}

func withSpec(spec string) option {
	return func(m *httpapi.Middleware, _ *[]httpapi.Route) {
		doc, err := httpapi.LoadSpec([]byte(spec))
		if err != nil {
			panic(err)
		}
		m.Spec = doc
	}
}

func withMiddleware(f func(*httpapi.Middleware)) option {
	return func(m *httpapi.Middleware, _ *[]httpapi.Route) { f(m) }
}

// newEdge mounts the routes (default: production Routes) on a mux behind the
// same outer middleware main uses, logging to a buffer.
func newEdge(t *testing.T, keys []testKey, opts ...option) *edge {
	t.Helper()
	s := &store{byLookup: map[string]domain.KeyRecord{}}
	for _, k := range keys {
		s.byLookup[k.lookup] = k.rec
	}
	logs := &bytes.Buffer{}
	log := logx.New(logs, slog.LevelDebug, "gateway")
	mw := httpapi.Middleware{
		Auth: &app.Authenticator{Keys: s, Hasher: hasher{}, Clock: fixedClock{}},
		Log:  log,
	}
	routes := httpapi.Routes(httpapi.Deps{})
	for _, o := range opts {
		o(&mw, &routes)
	}
	mux := http.NewServeMux()
	httpapi.Mount(mux, routes, mw)
	h := httpx.RequestID(httpx.AccessLog(log)(httpx.Recover(log)(mux)))
	return &edge{t: t, store: s, logs: logs, h: h}
}

func (e *edge) do(r *http.Request) *httptest.ResponseRecorder {
	e.t.Helper()
	if r.RemoteAddr == "192.0.2.1:1234" { // httptest default
		r.RemoteAddr = peer
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

func get(target, auth string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	return r
}

func problem(t *testing.T, rec *httptest.ResponseRecorder) httpx.Problem {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type = %q, body %s", ct, rec.Body.String())
	}
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode problem: %v: %s", err, rec.Body.String())
	}
	return p
}

func wantProblem(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) httpx.Problem {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d: %s", rec.Code, status, rec.Body.String())
	}
	p := problem(t, rec)
	if p.Code != code || p.Status != status {
		t.Fatalf("problem = %+v, want %d %s", p, status, code)
	}
	if status == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("WWW-Authenticate = %q on a 401", rec.Header().Get("WWW-Authenticate"))
	}
	return p
}

func TestAuthHeaderVariants(t *testing.T) {
	t.Parallel()
	k := mint(t, apikey.Test, "ak_01j9z3k8m2q4r6s8t0v2w4x6y8", domain.ScopeDeliveriesRead)
	e := newEdge(t, []testKey{k})

	cases := []struct {
		name   string
		req    *http.Request
		status int
	}{
		{"canonical", get("/v1/me", "Bearer "+k.raw), http.StatusOK},
		{"lowercase scheme", get("/v1/me", "bearer "+k.raw), http.StatusOK},
		{"extra whitespace", get("/v1/me", "Bearer   "+k.raw+"  "), http.StatusOK},
		{"query string", get("/v1/me?api_key="+k.raw, ""), http.StatusUnauthorized},
		{"query string with header", get("/v1/me?api_key="+k.raw, "Bearer "+k.raw), http.StatusUnauthorized},
		{"basic auth", get("/v1/me", "Basic "+base64.StdEncoding.EncodeToString([]byte(k.raw+":"))), http.StatusUnauthorized},
		{"no header", get("/v1/me", ""), http.StatusUnauthorized},
		{"scheme only", get("/v1/me", "Bearer"), http.StatusUnauthorized},
		{"two tokens", get("/v1/me", "Bearer "+k.raw+" "+k.raw), http.StatusUnauthorized},
		{"unknown key", get("/v1/me", "Bearer "+mint(t, apikey.Test, "ak_x").raw), http.StatusUnauthorized},
	}
	for _, c := range cases {
		rec := e.do(c.req)
		if rec.Code != c.status {
			t.Errorf("%s: status = %d, want %d: %s", c.name, rec.Code, c.status, rec.Body.String())
			continue
		}
		if c.status == http.StatusUnauthorized {
			wantProblem(t, rec, http.StatusUnauthorized, "unauthenticated")
		}
	}
	// Two Authorization headers are ambiguous and refused.
	r := get("/v1/me", "Bearer "+k.raw)
	r.Header.Add("Authorization", "Bearer "+k.raw)
	wantProblem(t, e.do(r), http.StatusUnauthorized, "unauthenticated")

	logs := e.logs.String()
	if !strings.Contains(logs, `"http_request"`) {
		t.Fatalf("access log not captured: %s", logs)
	}
	if strings.Contains(logs, k.raw) || strings.Contains(logs, k.lookup) {
		t.Fatalf("log contains the key or its lookup: %s", logs)
	}
}

func TestSecretKeyWithOriginRejected(t *testing.T) {
	t.Parallel()
	k := mint(t, apikey.Live, "ak_01j9z3k8m2q4r6s8t0v2w4x6y8", domain.ScopeDeliveriesRead)
	e := newEdge(t, []testKey{k})

	r := get("/v1/me", "Bearer "+k.raw)
	r.Header.Set("Origin", "https://evil.example")
	wantProblem(t, e.do(r), http.StatusForbidden, "secret_key_in_browser")
	if n := e.store.count(); n != 0 {
		t.Fatalf("key store queried %d times, want 0", n)
	}
	// Even a malformed dt_sk_ token is refused before any lookup.
	r = get("/v1/me", "Bearer dt_sk_live_short")
	r.Header.Set("Origin", "https://evil.example")
	wantProblem(t, e.do(r), http.StatusForbidden, "secret_key_in_browser")
	if n := e.store.count(); n != 0 {
		t.Fatalf("key store queried %d times, want 0", n)
	}
}

func TestClientIPIgnoresUntrustedForwardedFor(t *testing.T) {
	t.Parallel()
	k := mint(t, apikey.Test, "ak_01j9z3k8m2q4r6s8t0v2w4x6y8", domain.ScopeDeliveriesRead)
	k.rec.AllowedIPs = []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}

	req := func(xff ...string) *http.Request {
		r := get("/v1/me", "Bearer "+k.raw)
		for _, v := range xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		return r
	}

	// No trusted proxies: the TCP peer is the client, XFF is ignored.
	e := newEdge(t, []testKey{k})
	wantProblem(t, e.do(req("203.0.113.7")), http.StatusForbidden, "ip_not_allowed")

	// The peer is a trusted proxy: the hop it reports is the client.
	e = newEdge(t, []testKey{k}, trusted("198.51.100.0/24"))
	if rec := e.do(req("203.0.113.7")); rec.Code != http.StatusOK {
		t.Fatalf("trusted proxy: status = %d: %s", rec.Code, rec.Body.String())
	}
	// A spoofed left hop does not count: the first untrusted hop from the right wins.
	wantProblem(t, e.do(req("203.0.113.7, 10.9.9.9")), http.StatusForbidden, "ip_not_allowed")
	// Split across header lines, the order is the same.
	wantProblem(t, e.do(req("203.0.113.7", "10.9.9.9")), http.StatusForbidden, "ip_not_allowed")
	// A garbage hop never resolves to an allowed address.
	wantProblem(t, e.do(req("203.0.113.7, not-an-ip")), http.StatusForbidden, "ip_not_allowed")
}

// echoRoute is a POST route used by the tests that need a body.
func echoRoute(scope domain.Scope, env httpapi.EnvRule) httpapi.Route {
	return httpapi.Route{
		Name: "echo.create", Method: http.MethodPost, Pattern: "/v1/echo",
		Credentials: []domain.CredentialType{domain.CredentialSecretKey}, Scope: scope, Env: env,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(b)
		}),
	}
}

const echoSpec = `
openapi: 3.1.0
info: {title: test, version: 1.0.0}
paths:
  /me:
    get:
      operationId: getMe
      security: [{SecretKey: []}]
      responses:
        '200': {description: ok}
  /echo:
    post:
      operationId: echo
      security: [{SecretKey: []}]
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [name, pickup]
              properties:
                name: {type: string, minLength: 1}
                note: {type: string}
                pickup:
                  type: object
                  required: [lat]
                  properties:
                    lat: {type: number, minimum: -90, maximum: 90}
      responses:
        '201': {description: created}
components:
  securitySchemes:
    SecretKey: {type: http, scheme: bearer}
`

func post(target, auth, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	return r
}

func TestPayloadTooLarge(t *testing.T) {
	t.Parallel()
	k := mint(t, apikey.Test, "ak_01j9z3k8m2q4r6s8t0v2w4x6y8")
	e := newEdge(t, []testKey{k}, withRoutes(echoRoute("", httpapi.AnyEnv)), withSpec(echoSpec))

	big := `{"name":"` + strings.Repeat("a", httpapi.MaxBodyBytes) + `"}`
	body := big[:httpapi.MaxBodyBytes+1]
	wantProblem(t, e.do(post("/v1/echo", "Bearer "+k.raw, body)), http.StatusRequestEntityTooLarge, "payload_too_large")

	// Without a Content-Length (chunked), the limit still holds.
	r := post("/v1/echo", "Bearer "+k.raw, "")
	r.Body = io.NopCloser(strings.NewReader(body))
	r.ContentLength = -1
	wantProblem(t, e.do(r), http.StatusRequestEntityTooLarge, "payload_too_large")

	// Exactly the limit passes the size check (and reaches validation).
	ok := `{"name":"x","pickup":{"lat":1},"note":"` + strings.Repeat("a", httpapi.MaxBodyBytes) + `"}`
	pad := len(ok) - httpapi.MaxBodyBytes
	ok = `{"name":"x","pickup":{"lat":1},"note":"` + strings.Repeat("a", httpapi.MaxBodyBytes-pad) + `"}`
	if len(ok) != httpapi.MaxBodyBytes {
		t.Fatalf("body is %d bytes", len(ok))
	}
	if rec := e.do(post("/v1/echo", "Bearer "+k.raw, ok)); rec.Code != http.StatusCreated {
		t.Fatalf("64 KiB body: status = %d: %.200s", rec.Code, rec.Body.String())
	}
}

func TestNoCORS(t *testing.T) {
	t.Parallel()
	k := mint(t, apikey.Test, "ak_01j9z3k8m2q4r6s8t0v2w4x6y8")
	e := newEdge(t, []testKey{k})

	for _, path := range []string{"/v1/me", "/v1/does-not-exist"} {
		r := httptest.NewRequest(http.MethodOptions, path, nil)
		r.Header.Set("Origin", "https://app.example")
		r.Header.Set("Access-Control-Request-Method", "GET")
		rec := e.do(r)
		wantProblem(t, rec, http.StatusForbidden, "cors_not_allowed")
		assertNoCORSHeaders(t, rec)
	}
	// A normal response carries no CORS headers either.
	rec := e.do(get("/v1/me", "Bearer "+k.raw))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	assertNoCORSHeaders(t, rec)
}

func assertNoCORSHeaders(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	for h := range rec.Header() {
		if strings.HasPrefix(strings.ToLower(h), "access-control-") {
			t.Fatalf("CORS header %s set", h)
		}
	}
}

func TestGetMeMatchesContract(t *testing.T) {
	t.Parallel()
	k := mint(t, apikey.Live, "ak_01j9z3k8m2q4r6s8t0v2w4x6y8", domain.ScopeDeliveriesRead, domain.ScopeWebhooksWrite)
	e := newEdge(t, []testKey{k})

	req := get("/v1/me", "Bearer "+k.raw)
	rec := e.do(req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	doc, err := httpapi.LoadSpec(api.Spec)
	if err != nil {
		t.Fatal(err)
	}
	doc.Servers = nil
	router, err := gorillamux.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	specReq := httptest.NewRequest(http.MethodGet, "/me", nil)
	route, params, err := router.FindRoute(specReq)
	if err != nil {
		t.Fatal(err)
	}
	err = openapi3filter.ValidateResponse(context.Background(), &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{
			Request: specReq, PathParams: params, Route: route,
			Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
		},
		Status: rec.Code,
		Header: rec.Header(),
		Body:   io.NopCloser(bytes.NewReader(rec.Body.Bytes())),
	})
	if err != nil {
		t.Fatalf("response does not match the contract: %v\n%s", err, rec.Body.String())
	}

	var me api.Me
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	if me.TenantId != tenantID || me.Environment != api.EnvironmentLive ||
		me.Credential.Type != api.SecretKey || me.Credential.Id != k.rec.ID {
		t.Fatalf("me = %+v", me)
	}
	if !slices.Equal(me.Scopes, []api.Scope{api.DeliveriesRead, api.WebhooksWrite}) {
		t.Fatalf("scopes = %v", me.Scopes)
	}

	// HEAD is served by the GET route and validated as GET.
	if rec := e.do(httptest.NewRequest(http.MethodHead, "/v1/me", nil)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("HEAD without key: status = %d", rec.Code)
	}
	head := httptest.NewRequest(http.MethodHead, "/v1/me", nil)
	head.Header.Set("Authorization", "Bearer "+k.raw)
	if rec := e.do(head); rec.Code != http.StatusOK {
		t.Fatalf("HEAD: status = %d: %s", rec.Code, rec.Body.String())
	}

	// A key with no scopes answers an empty list, not null.
	k2 := mint(t, apikey.Test, "ak_01j9z3k8m2q4r6s8t0v2w4x6y9")
	e2 := newEdge(t, []testKey{k2})
	rec = e2.do(get("/v1/me", "Bearer "+k2.raw))
	if !strings.Contains(rec.Body.String(), `"scopes":[]`) || !strings.Contains(rec.Body.String(), `"environment":"test"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestValidationFailedProblem(t *testing.T) {
	t.Parallel()
	k := mint(t, apikey.Test, "ak_01j9z3k8m2q4r6s8t0v2w4x6y8")
	e := newEdge(t, []testKey{k}, withRoutes(echoRoute("", httpapi.AnyEnv)), withSpec(echoSpec))

	rec := e.do(post("/v1/echo", "Bearer "+k.raw, `{"name":"","note":42,"pickup":{"lat":"north"}}`))
	p := wantProblem(t, rec, http.StatusBadRequest, "validation_failed")
	fields := map[string]bool{}
	for _, fe := range p.Errors {
		if fe.Field == "" || fe.Code == "" || fe.Message == "" {
			t.Errorf("incomplete field error %+v", fe)
		}
		fields[fe.Field] = true
	}
	for _, f := range []string{"name", "note", "pickup.lat"} {
		if !fields[f] {
			t.Errorf("no error for field %q in %+v", f, p.Errors)
		}
	}
	// Values from the request are never echoed back.
	if strings.Contains(rec.Body.String(), "north") {
		t.Errorf("problem echoes a request value: %s", rec.Body.String())
	}

	// A missing required property names that property.
	p = wantProblem(t, e.do(post("/v1/echo", "Bearer "+k.raw, `{"pickup":{}}`)), http.StatusBadRequest, "validation_failed")
	fields = map[string]bool{}
	for _, fe := range p.Errors {
		fields[fe.Field] = true
	}
	if !fields["name"] || !fields["pickup.lat"] {
		t.Errorf("missing properties not named: %+v", p.Errors)
	}

	// Not JSON at all.
	p = wantProblem(t, e.do(post("/v1/echo", "Bearer "+k.raw, `{`)), http.StatusBadRequest, "validation_failed")
	if len(p.Errors) == 0 || p.Errors[0].Field != "body" {
		t.Errorf("errors = %+v", p.Errors)
	}

	// Authentication runs first: a bad body without a key is a 401.
	wantProblem(t, e.do(post("/v1/echo", "", `{`)), http.StatusUnauthorized, "unauthenticated")
}

func TestAuthorize(t *testing.T) {
	t.Parallel()
	live := mint(t, apikey.Live, "ak_01j9z3k8m2q4r6s8t0v2w4x6y8", domain.ScopeDeliveriesRead)
	test := mint(t, apikey.Test, "ak_01j9z3k8m2q4r6s8t0v2w4x6y9", domain.ScopeDeliveriesWrite)
	body := `{"name":"x","pickup":{"lat":1}}`

	scoped := newEdge(t, []testKey{live, test}, withRoutes(echoRoute(domain.ScopeDeliveriesWrite, httpapi.AnyEnv)), withSpec(echoSpec))
	wantProblem(t, scoped.do(post("/v1/echo", "Bearer "+live.raw, body)), http.StatusForbidden, "insufficient_scope")
	if rec := scoped.do(post("/v1/echo", "Bearer "+test.raw, body)); rec.Code != http.StatusCreated {
		t.Fatalf("scoped: status = %d: %s", rec.Code, rec.Body.String())
	}

	testOnly := newEdge(t, []testKey{live, test}, withRoutes(echoRoute("", httpapi.TestOnly)), withSpec(echoSpec))
	wantProblem(t, testOnly.do(post("/v1/echo", "Bearer "+live.raw, body)), http.StatusForbidden, "environment_not_allowed")
	if rec := testOnly.do(post("/v1/echo", "Bearer "+test.raw, body)); rec.Code != http.StatusCreated {
		t.Fatalf("test only: status = %d: %s", rec.Code, rec.Body.String())
	}

	// A route that accepts no secret keys refuses one as unauthenticated.
	rt := echoRoute("", httpapi.AnyEnv)
	rt.Credentials = []domain.CredentialType{"courier_token"}
	courier := newEdge(t, []testKey{test}, withRoutes(rt), withSpec(echoSpec))
	wantProblem(t, courier.do(post("/v1/echo", "Bearer "+test.raw, body)), http.StatusUnauthorized, "unauthenticated")
}

func TestUnknownPathIsNotFound(t *testing.T) {
	t.Parallel()
	k := mint(t, apikey.Test, "ak_01j9z3k8m2q4r6s8t0v2w4x6y8")
	e := newEdge(t, []testKey{k})
	wantProblem(t, e.do(get("/v1/nope", "Bearer "+k.raw)), http.StatusNotFound, "not_found")
	wantProblem(t, e.do(get("/v1/nope", "")), http.StatusNotFound, "not_found")
	wantProblem(t, e.do(post("/v1/me", "Bearer "+k.raw, `{}`)), http.StatusNotFound, "not_found")
}

func TestKeyStoreFailureFailsClosed(t *testing.T) {
	t.Parallel()
	k := mint(t, apikey.Test, "ak_01j9z3k8m2q4r6s8t0v2w4x6y8")
	e := newEdge(t, []testKey{k})
	e.store.err = errors.New("connection refused")

	rec := e.do(get("/v1/me", "Bearer "+k.raw))
	wantProblem(t, rec, http.StatusInternalServerError, "internal_error")
	logs := e.logs.String()
	if !strings.Contains(logs, `"level":"ERROR"`) || !strings.Contains(logs, "connection refused") {
		t.Fatalf("store failure not logged at error: %s", logs)
	}
	if strings.Contains(logs, k.raw) || strings.Contains(logs, k.lookup) {
		t.Fatalf("log contains the key or its lookup: %s", logs)
	}
}

func TestRouteMissingFromContractFailsClosed(t *testing.T) {
	t.Parallel()
	k := mint(t, apikey.Test, "ak_01j9z3k8m2q4r6s8t0v2w4x6y8")
	// The echo route is mounted, but the production spec does not describe it.
	e := newEdge(t, []testKey{k}, withRoutes(echoRoute("", httpapi.AnyEnv)))
	wantProblem(t, e.do(post("/v1/echo", "Bearer "+k.raw, `{}`)), http.StatusInternalServerError, "internal_error")
}

func TestMiddlewareHookOrder(t *testing.T) {
	t.Parallel()
	k := mint(t, apikey.Test, "ak_01j9z3k8m2q4r6s8t0v2w4x6y8")
	var mu sync.Mutex
	var calls []string
	hook := func(name string) httpapi.Hook {
		return func(next http.Handler, rt httpapi.Route) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, authed := httpapi.PrincipalFrom(r.Context())
				_, hasIP := httpapi.ClientIPFrom(r.Context())
				mu.Lock()
				calls = append(calls, name+":"+rt.Name+":"+map[bool]string{true: "authed", false: "anon"}[authed]+":"+map[bool]string{true: "ip", false: "noip"}[hasIP])
				mu.Unlock()
				next.ServeHTTP(w, r)
			})
		}
	}
	hooks := withMiddleware(func(m *httpapi.Middleware) {
		m.FailedAuth = hook("failed_auth")
		m.RateLimit = hook("rate_limit")
		m.Idempotency = hook("idempotency")
		m.Audit = hook("audit")
	})
	reset := func() []string {
		mu.Lock()
		defer mu.Unlock()
		c := calls
		calls = nil
		return c
	}

	e := newEdge(t, []testKey{k}, withRoutes(echoRoute("", httpapi.AnyEnv), httpapi.Routes(httpapi.Deps{})[0]), withSpec(echoSpec), hooks)
	if rec := e.do(post("/v1/echo", "Bearer "+k.raw, `{"name":"x","pickup":{"lat":1}}`)); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	want := []string{
		"failed_auth:echo.create:anon:ip",
		"rate_limit:echo.create:authed:ip",
		"audit:echo.create:authed:ip",
		"idempotency:echo.create:authed:ip",
	}
	if got := reset(); !slices.Equal(got, want) {
		t.Fatalf("POST hooks = %v, want %v", got, want)
	}
	if rec := e.do(get("/v1/me", "Bearer "+k.raw)); rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	want = []string{"failed_auth:me.get:anon:ip", "rate_limit:me.get:authed:ip"}
	if got := reset(); !slices.Equal(got, want) {
		t.Fatalf("GET hooks = %v, want %v", got, want)
	}
	// A failed authentication reaches only the failed-auth hook.
	e.do(get("/v1/me", ""))
	if got := reset(); !slices.Equal(got, []string{"failed_auth:me.get:anon:ip"}) {
		t.Fatalf("401 hooks = %v", got)
	}
}

func TestRoutesTable(t *testing.T) {
	t.Parallel()
	rs := httpapi.Routes(httpapi.Deps{})
	if len(rs) != 15 { // me.get, the ten delivery and courier routes, the four tracking routes
		t.Fatalf("routes = %d, want 15", len(rs))
	}
	type props struct {
		method, pattern string
		scope           domain.Scope
		bucket          string
		auditReads      bool
		maxBody         int64
		noIdempotency   bool
	}
	want := map[string]props{
		"deliveries.get_tracking":      {http.MethodGet, "/v1/deliveries/{id}/tracking", domain.ScopeDeliveriesRead, "", false, 0, false},
		"deliveries.list_trail":        {http.MethodGet, "/v1/deliveries/{id}/trail", domain.ScopeDeliveriesRead, "", true, 0, false},
		"deliveries.list_proof_points": {http.MethodGet, "/v1/deliveries/{id}/proof-points", domain.ScopeDeliveriesRead, "", true, 0, false},
		"couriers.report_locations": {http.MethodPost, "/v1/couriers/{id}/locations", domain.ScopeCouriersWrite,
			httpapi.RateBucketRelay, false, 16 << 10, true},
	}
	for _, r := range rs {
		w, ok := want[r.Name]
		if !ok {
			if r.RateBucket != "" || r.AuditReads || r.MaxBody != 0 || r.NoIdempotency {
				t.Errorf("route %s has tracking-only settings: %+v", r.Name, r)
			}
			continue
		}
		delete(want, r.Name)
		got := props{r.Method, r.Pattern, r.Scope, r.RateBucket, r.AuditReads, r.MaxBody, r.NoIdempotency}
		if got != w || r.Env != httpapi.AnyEnv || r.SuccessStatus != http.StatusOK {
			t.Errorf("route %s = %+v, want %+v", r.Name, got, w)
		}
	}
	if len(want) != 0 {
		t.Errorf("missing routes: %v", want)
	}
	r := rs[0]
	if r.Name != "me.get" || r.Method != http.MethodGet || r.Pattern != "/v1/me" || r.Scope != "" ||
		r.Env != httpapi.AnyEnv || !slices.Equal(r.Credentials, []domain.CredentialType{domain.CredentialSecretKey}) || r.Handler == nil {
		t.Fatalf("route = %+v", r)
	}
}
