package httpapi_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/raduolivas/dt/backend/pkg/apikey"
	"github.com/raduolivas/dt/backend/services/gateway/api"
	httpapi "github.com/raduolivas/dt/backend/services/gateway/internal/adapters/inbound/http"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

// The authorization matrix runs every gateway route against every credential
// case. Later milestones extend it: new routes join httpapi.Routes and are
// picked up automatically; new credential types and tenant-scoped resources
// add cases (see tenantResourceCases).

// credentialCase is one row of the matrix. Apply sets the credential (or its
// defect) on a request that is otherwise valid for the route. Want returns the
// expected status and problem code for the route; code "" means the request is
// allowed (status is then the route's success status). A status of 0 means
// the case does not apply to the route and is skipped.
type credentialCase struct {
	Name  string
	Apply func(*http.Request)
	Want  func(httpapi.Route) (status int, code string)
}

// matrixFixture holds the credentials one route's cases use, all in one
// in-memory key store.
type matrixFixture struct {
	test, live                        testKey
	revoked, expired, rolled, noScope testKey
	allowlisted, unknown              testKey
	other                             netip.Prefix // the allowlist of the allowlisted key
}

// tenantResourceCases is the hook where M2 adds cases for routes that take a
// tenant-owned resource id, e.g. "other tenant's resource" -> 404 not_found.
// It is a no-op in M1: no route has a resource yet.
func tenantResourceCases(httpapi.Route, *matrixFixture) []credentialCase { return nil }

func allScopesExcept(skip domain.Scope) []domain.Scope {
	var out []domain.Scope
	for _, s := range domain.AllScopes {
		if s != skip {
			out = append(out, s)
		}
	}
	return out
}

func newMatrixFixture(t *testing.T, rt httpapi.Route) (*matrixFixture, []testKey) {
	t.Helper()
	now := fixedClock{}.Now()
	f := &matrixFixture{other: netip.MustParsePrefix("203.0.113.0/24")}
	f.test = mint(t, apikey.Test, "ak_01j9z3k8m2q4r6s8t0v2w4x6y1", domain.AllScopes...)
	f.live = mint(t, apikey.Live, "ak_01j9z3k8m2q4r6s8t0v2w4x6y2", domain.AllScopes...)
	f.revoked = mint(t, apikey.Test, "ak_01j9z3k8m2q4r6s8t0v2w4x6y3", domain.AllScopes...)
	f.revoked.rec.RevokedAt = &now
	f.expired = mint(t, apikey.Test, "ak_01j9z3k8m2q4r6s8t0v2w4x6y4", domain.AllScopes...)
	past := now.Add(-time.Second)
	f.expired.rec.ExpiresAt = &past
	f.rolled = mint(t, apikey.Test, "ak_01j9z3k8m2q4r6s8t0v2w4x6y5", domain.AllScopes...)
	overlap := now.Add(time.Hour)
	f.rolled.rec.ExpiresAt = &overlap
	f.noScope = mint(t, apikey.Test, "ak_01j9z3k8m2q4r6s8t0v2w4x6y6", allScopesExcept(rt.Scope)...)
	f.allowlisted = mint(t, apikey.Test, "ak_01j9z3k8m2q4r6s8t0v2w4x6y7", domain.AllScopes...)
	f.allowlisted.rec.AllowedIPs = []netip.Prefix{f.other}
	f.unknown = mint(t, apikey.Test, "ak_01j9z3k8m2q4r6s8t0v2w4x6y8", domain.AllScopes...) // never stored
	return f, []testKey{f.test, f.live, f.revoked, f.expired, f.rolled, f.noScope, f.allowlisted}
}

func bearer(k testKey) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+k.raw) }
}

func allowed(rt httpapi.Route) (int, string) { return successStatus(rt), "" }

func unauth(httpapi.Route) (int, string) { return http.StatusUnauthorized, "unauthenticated" }

func forbidden(code string) func(httpapi.Route) (int, string) {
	return func(httpapi.Route) (int, string) { return http.StatusForbidden, code }
}

// successStatus is the status a fully authorized request to rt gets.
func successStatus(rt httpapi.Route) int {
	if rt.Method == http.MethodPost {
		return http.StatusCreated
	}
	return http.StatusOK
}

// credentialCases is the table every route is run against.
func credentialCases(rt httpapi.Route, f *matrixFixture) []credentialCase {
	cs := []credentialCase{
		{"no credential", func(*http.Request) {}, unauth},
		{"malformed key", func(r *http.Request) { r.Header.Set("Authorization", "Bearer dt_sk_test_short") }, unauth},
		{"unknown key", bearer(f.unknown), unauth},
		{"revoked key", bearer(f.revoked), unauth},
		{"expired key", bearer(f.expired), unauth},
		{"rolled key inside the overlap", bearer(f.rolled), allowed},
		{"valid key with Origin", func(r *http.Request) {
			bearer(f.test)(r)
			r.Header.Set("Origin", "https://app.example")
		}, forbidden("secret_key_in_browser")},
		{"key allowlist, other IP", bearer(f.allowlisted), forbidden("ip_not_allowed")},
		{"key lacking the route scope", bearer(f.noScope), func(r httpapi.Route) (int, string) {
			if r.Scope == "" {
				return 0, ""
			}
			return http.StatusForbidden, "insufficient_scope"
		}},
		{"live key on a test-only route", bearer(f.live), func(r httpapi.Route) (int, string) {
			if r.Env != httpapi.TestOnly {
				return 0, ""
			}
			return http.StatusForbidden, "environment_not_allowed"
		}},
		{"valid test key", bearer(f.test), allowed},
		{"valid live key", bearer(f.live), func(r httpapi.Route) (int, string) {
			if r.Env == httpapi.TestOnly {
				return 0, "" // covered by the test-only case above
			}
			return allowed(r)
		}},
	}
	return append(cs, tenantResourceCases(rt, f)...)
}

var pathParam = regexp.MustCompile(`\{[^}]+\}`)

var idemSeq atomic.Int64

// baseRequest is a request that would succeed for rt with a good credential.
func baseRequest(rt httpapi.Route) *http.Request {
	target := pathParam.ReplaceAllString(rt.Pattern, "x")
	if rt.Method != http.MethodPost {
		return httptest.NewRequest(rt.Method, target, nil)
	}
	r := httptest.NewRequest(rt.Method, target, strings.NewReader(`{"name":"matrix"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "matrix-"+strconv.FormatInt(idemSeq.Add(1), 10))
	return r
}

// matrixStore is an in-memory app.IdempotencyStore (no replay needed here:
// every request uses a fresh key).
type matrixStore struct {
	mu   sync.Mutex
	seen map[app.IdempotencyScope][]byte
}

func (s *matrixStore) Begin(_ context.Context, sc app.IdempotencyScope, hash []byte, now time.Time) (app.IdempotencyOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.seen[sc]; ok {
		if !bytes.Equal(prev, hash) {
			return app.IdempotencyOutcome{Kind: app.OutcomeMismatch}, nil
		}
		return app.IdempotencyOutcome{Kind: app.OutcomeInProgress}, nil
	}
	s.seen[sc] = hash
	return app.IdempotencyOutcome{Kind: app.OutcomeStarted, Lease: now.Add(app.IdempotencyLockTTL)}, nil
}

func (s *matrixStore) Complete(context.Context, app.IdempotencyScope, time.Time, app.StoredResponse) error {
	return nil
}

func (s *matrixStore) Abandon(_ context.Context, sc app.IdempotencyScope, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.seen, sc)
	return nil
}

type allowAll struct{}

func (allowAll) Allow(context.Context, string, app.Limit) (app.Decision, error) {
	return app.Decision{Allowed: true, Limit: 100, Remaining: 99}, nil
}

// testRoutes are two test-only routes that exercise the rows /v1/me cannot
// (it has no scope and accepts both environments).
func testRoutes(calls *atomic.Int64) []httpapi.Route {
	h := func(status int) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{}`))
		})
	}
	creds := []domain.CredentialType{domain.CredentialSecretKey}
	return []httpapi.Route{
		{Name: "test.deliveries.create", Method: http.MethodPost, Pattern: "/v1/test/deliveries", Credentials: creds,
			Scope: domain.ScopeDeliveriesWrite, Env: httpapi.TestOnly, Handler: h(http.StatusCreated)},
		{Name: "test.webhooks.get", Method: http.MethodGet, Pattern: "/v1/test/webhooks", Credentials: creds,
			Scope: domain.ScopeWebhooksWrite, Env: httpapi.AnyEnv, Handler: h(http.StatusOK)},
	}
}

// matrixSpec is the production contract plus the two test-only paths, so
// request validation does not fail them closed.
func matrixSpec(t *testing.T) *openapi3.T {
	t.Helper()
	doc, err := httpapi.LoadSpec(api.Spec)
	if err != nil {
		t.Fatal(err)
	}
	obj := openapi3.NewObjectSchema().WithProperty("name", openapi3.NewStringSchema())
	obj.Required = []string{"name"}
	resp := func(status string) *openapi3.Responses {
		r := openapi3.NewResponses()
		r.Set(status, &openapi3.ResponseRef{Value: openapi3.NewResponse().WithDescription("ok")})
		return r
	}
	doc.Paths.Set("/test/deliveries", &openapi3.PathItem{Post: &openapi3.Operation{
		OperationID: "testCreateDelivery",
		RequestBody: &openapi3.RequestBodyRef{Value: openapi3.NewRequestBody().WithRequired(true).WithJSONSchema(obj)},
		Responses:   resp("201"),
	}})
	doc.Paths.Set("/test/webhooks", &openapi3.PathItem{Get: &openapi3.Operation{
		OperationID: "testGetWebhooks", Responses: resp("200"),
	}})
	return doc
}

func TestAuthorizationMatrix(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	routes := append(httpapi.Routes(httpapi.Deps{}), testRoutes(&calls)...)

	for _, rt := range routes {
		listing, _ := newMatrixFixture(t, rt) // only to enumerate the cases
		for _, c := range credentialCases(rt, listing) {
			wantStatus, _ := c.Want(rt)
			if wantStatus == 0 {
				continue
			}
			t.Run(rt.Name+"/"+c.Name, func(t *testing.T) {
				t.Parallel()
				// Fresh credentials and edge per cell: nothing is shared.
				f, keys := newMatrixFixture(t, rt)
				var cell credentialCase
				for _, cc := range credentialCases(rt, f) {
					if cc.Name == c.Name {
						cell = cc
					}
				}
				var handled atomic.Int64
				counted := rt
				inner := rt.Handler
				counted.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					handled.Add(1)
					inner.ServeHTTP(w, r)
				})
				e := newEdge(t, keys, withRoutes(counted), withMiddleware(func(m *httpapi.Middleware) {
					m.Spec = matrixSpec(t)
					m.FailedAuth = httpapi.FailedAuthLimit(allowAll{}, m.Log)
					m.RateLimit = httpapi.PartnerRateLimit(allowAll{}, m.Log)
					m.Idempotency = httpapi.Idempotency(&matrixStore{seen: map[app.IdempotencyScope][]byte{}}, fixedClock{}, m.Log)
					m.Audit = httpapi.Audit(&memAudit{}, fixedClock{}, m.Log)
				}))

				req := baseRequest(rt)
				cell.Apply(req)
				rec := e.do(req)

				status, code := cell.Want(rt)
				if code != "" {
					wantProblem(t, rec, status, code)
					if n := handled.Load(); n != 0 {
						t.Fatalf("handler ran %d times for a refused request", n)
					}
					return
				}
				if rec.Code != status {
					t.Fatalf("status = %d, want %d: %s", rec.Code, status, rec.Body.String())
				}
				if n := handled.Load(); n != 1 {
					t.Fatalf("handler ran %d times, want 1", n)
				}
			})
		}
	}
}

// TestEveryRouteIsInTheMatrix keeps a route from being added without being
// exercised: the matrix runs over httpapi.Routes, so every production route
// must declare who may call it.
func TestEveryRouteIsInTheMatrix(t *testing.T) {
	t.Parallel()
	routes := httpapi.Routes(httpapi.Deps{})
	if len(routes) == 0 {
		t.Fatal("Routes is empty: the matrix would test nothing")
	}
	seen := map[string]bool{}
	for _, rt := range routes {
		if rt.Name == "" || rt.Handler == nil {
			t.Errorf("route %q %s %s has no name or handler", rt.Name, rt.Method, rt.Pattern)
		}
		if len(rt.Credentials) == 0 {
			t.Errorf("route %s %s has no Credentials: it would accept no caller (or be added without thought)", rt.Method, rt.Pattern)
		}
		if seen[rt.Name] {
			t.Errorf("duplicate route name %q", rt.Name)
		}
		seen[rt.Name] = true
	}
}
