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

	"connectrpc.com/connect/v2"
	"github.com/getkin/kin-openapi/openapi3"

	deliveriesv1 "github.com/raduolivas/dt/backend/gen/dt/deliveries/v1"
	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
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
	// Reached marks a refusal the deliveries service itself answers (404 for
	// a resource outside the caller's scope): the route handler runs and
	// makes exactly one RPC, unlike an authorization refusal.
	Reached bool
	// Limited makes the partner rate limiter deny the request (429).
	Limited bool
}

// matrixFixture holds the credentials one route's cases use, all in one
// in-memory key store.
type matrixFixture struct {
	test, live                        testKey
	revoked, expired, rolled, noScope testKey
	allowlisted, unknown              testKey
	other                             netip.Prefix // the allowlist of the allowlisted key
}

// Resources the matrix fake (scopedFake) owns. delMine and couMine belong to
// (fixtureTenant, test), delLive to (fixtureTenant, live), delOther to another
// tenant. The live key's happy path uses delLive, so that "live key on a test
// resource" can be the other-environment case.
const (
	delMine     = delID
	delLive     = "del_01j9z3k8m2q4r6s8t0v2w4x6z1"
	delOther    = "del_01j9z3k8m2q4r6s8t0v2w4x6z2"
	couMine     = couID
	extMine     = "c-mine"
	otherTenant = "ten_01j9z3k8m2q4r6s8t0v2w4x6z3"
)

// hasResourceID reports whether rt addresses a delivery by {id}.
// couriers.upsert is deliberately not one: external_id is tenant and
// environment scoped, so a PUT just creates the caller's own courier.
func hasResourceID(rt httpapi.Route) bool { return strings.Contains(rt.Pattern, "{id}") }

// onResource points a request at resource id.
func onResource(rt httpapi.Route, id string) func(*http.Request) {
	return func(r *http.Request) { r.URL.Path = resolvePath(rt, id, extMine) }
}

func resolvePath(rt httpapi.Route, id, ext string) string {
	return pathParam.ReplaceAllStringFunc(rt.Pattern, func(m string) string {
		if m == "{external_id}" {
			return ext
		}
		return id
	})
}

func both(fs ...func(*http.Request)) func(*http.Request) {
	return func(r *http.Request) {
		for _, f := range fs {
			f(r)
		}
	}
}

// tenantResourceCases are the cases for routes that take a tenant-owned
// resource id: another tenant's resource and the same tenant's other
// environment are both 404 not_found, never 403.
func tenantResourceCases(rt httpapi.Route, f *matrixFixture) []credentialCase {
	if !hasResourceID(rt) {
		return nil
	}
	notFound := func(httpapi.Route) (int, string) { return http.StatusNotFound, "not_found" }
	return []credentialCase{
		{Name: "other tenant's resource", Apply: both(bearer(f.test), onResource(rt, delOther)), Want: notFound, Reached: true},
		{Name: "same tenant, other environment", Apply: both(bearer(f.live), onResource(rt, delMine)), Want: notFound, Reached: true},
	}
}

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

// liveResource points a delivery route at the live-environment delivery.
func liveResource(rt httpapi.Route) func(*http.Request) {
	if !hasResourceID(rt) {
		return func(*http.Request) {}
	}
	return onResource(rt, delLive)
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
func successStatus(rt httpapi.Route) int { return rt.SuccessStatus }

// credentialCases is the table every route is run against.
func credentialCases(rt httpapi.Route, f *matrixFixture) []credentialCase {
	cs := []credentialCase{
		{Name: "no credential", Apply: func(*http.Request) {}, Want: unauth},
		{Name: "malformed key", Apply: func(r *http.Request) { r.Header.Set("Authorization", "Bearer dt_sk_test_short") }, Want: unauth},
		{Name: "unknown key", Apply: bearer(f.unknown), Want: unauth},
		{Name: "revoked key", Apply: bearer(f.revoked), Want: unauth},
		{Name: "expired key", Apply: bearer(f.expired), Want: unauth},
		{Name: "rolled key inside the overlap", Apply: bearer(f.rolled), Want: allowed},
		{Name: "valid key with Origin", Apply: func(r *http.Request) {
			bearer(f.test)(r)
			r.Header.Set("Origin", "https://app.example")
		}, Want: forbidden("secret_key_in_browser")},
		{Name: "key allowlist, other IP", Apply: bearer(f.allowlisted), Want: forbidden("ip_not_allowed")},
		{Name: "key lacking the route scope", Apply: bearer(f.noScope), Want: func(r httpapi.Route) (int, string) {
			if r.Scope == "" {
				return 0, ""
			}
			return http.StatusForbidden, "insufficient_scope"
		}},
		{Name: "live key on a test-only route", Apply: bearer(f.live), Want: func(r httpapi.Route) (int, string) {
			if r.Env != httpapi.TestOnly {
				return 0, ""
			}
			return http.StatusForbidden, "environment_not_allowed"
		}},
		{Name: "valid test key", Apply: bearer(f.test), Want: allowed},
		{Name: "valid live key", Apply: both(bearer(f.live), liveResource(rt)), Want: func(r httpapi.Route) (int, string) {
			if r.Env == httpapi.TestOnly {
				return 0, "" // covered by the test-only case above
			}
			return allowed(r)
		}},
		{Name: "rate limited key", Apply: bearer(f.test), Want: func(httpapi.Route) (int, string) {
			return http.StatusTooManyRequests, "rate_limited"
		}, Limited: true},
	}
	return append(cs, tenantResourceCases(rt, f)...)
}

var pathParam = regexp.MustCompile(`\{[^}]+\}`)

var idemSeq atomic.Int64

// routeBodies is a valid example body per route name (the OpenAPI examples),
// so request validation never masks an authorization result.
var routeBodies = map[string]string{
	"test.deliveries.create":  `{"name":"matrix"}`,
	"deliveries.create":       create,
	"deliveries.update":       `{"dropoff":{"location":{"lat":-23.55,"lng":-46.64}}}`,
	"deliveries.assign":       `{"courier_id":"` + couMine + `"}`,
	"deliveries.report_event": `{"event_id":"evt-9001","type":"at_pickup","occurred_at":"2026-10-22T14:10:00Z"}`,
	"deliveries.cancel":       `{"reason":"Customer cancelled the order"}`,
	"couriers.upsert":         `{"display_name":"Carlos","vehicle":"e_bike","photo_url":"https://cdn.example.com/c/77.jpg"}`,
}

// baseRequest is a request that would succeed for rt with a good credential
// on the test environment's own resources.
func baseRequest(rt httpapi.Route) *http.Request {
	target := resolvePath(rt, delMine, extMine)
	body, hasBody := routeBodies[rt.Name]
	if !hasBody && rt.Method == http.MethodPost {
		body, hasBody = `{"name":"matrix"}`, true
	}
	var r *http.Request
	if hasBody {
		r = httptest.NewRequest(rt.Method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(rt.Method, target, nil)
	}
	if rt.Method == http.MethodPost {
		r.Header.Set("Idempotency-Key", "matrix-"+strconv.FormatInt(idemSeq.Add(1), 10))
	}
	return r
}

// scopedFake is a deliveries client that owns delMine and couMine in
// (fixture tenant, test), delLive in (fixture tenant, live) and delOther in
// another tenant. Anything outside the scope the gateway sends is NotFound,
// like the service behind row-level security. Upserting couriers answers
// "updated", so an authorized PUT is 200.
type scopedFake struct{ *fakeDeliveries }

type owner struct {
	tenant string
	env    eventsv1.Environment
}

var owners = map[string]owner{
	delMine:  {tenantID, eventsv1.Environment_ENVIRONMENT_TEST},
	delLive:  {tenantID, eventsv1.Environment_ENVIRONMENT_LIVE},
	delOther: {otherTenant, eventsv1.Environment_ENVIRONMENT_TEST},
}

// visible records the call and reports whether sc can see delivery id.
func (s scopedFake) visible(ctx context.Context, name string, req any, sc *deliveriesv1.Scope, id string) error {
	if err := s.rec(ctx, name, req); err != nil {
		return err
	}
	if o, ok := owners[id]; !ok || o.tenant != sc.GetTenantId() || o.env != sc.GetEnvironment() {
		return rpcFail(connect.CodeNotFound, "not_found")(ctx)
	}
	return nil
}

func (s scopedFake) GetDelivery(ctx context.Context, r *deliveriesv1.GetDeliveryRequest) (*deliveriesv1.GetDeliveryResponse, error) {
	if err := s.visible(ctx, "get", r, r.GetScope(), r.GetId()); err != nil {
		return nil, err
	}
	return &deliveriesv1.GetDeliveryResponse{Delivery: s.del}, nil
}

func (s scopedFake) UpdateDelivery(ctx context.Context, r *deliveriesv1.UpdateDeliveryRequest) (*deliveriesv1.UpdateDeliveryResponse, error) {
	if err := s.visible(ctx, "update", r, r.GetScope(), r.GetId()); err != nil {
		return nil, err
	}
	return &deliveriesv1.UpdateDeliveryResponse{Delivery: s.del}, nil
}

func (s scopedFake) AssignCourier(ctx context.Context, r *deliveriesv1.AssignCourierRequest) (*deliveriesv1.AssignCourierResponse, error) {
	if err := s.visible(ctx, "assign", r, r.GetScope(), r.GetId()); err != nil {
		return nil, err
	}
	return &deliveriesv1.AssignCourierResponse{Delivery: s.del}, nil
}

func (s scopedFake) ReportEvent(ctx context.Context, r *deliveriesv1.ReportEventRequest) (*deliveriesv1.ReportEventResponse, error) {
	if err := s.visible(ctx, "event", r, r.GetScope(), r.GetId()); err != nil {
		return nil, err
	}
	return &deliveriesv1.ReportEventResponse{Delivery: s.del, Outcome: "applied"}, nil
}

func (s scopedFake) CancelDelivery(ctx context.Context, r *deliveriesv1.CancelDeliveryRequest) (*deliveriesv1.CancelDeliveryResponse, error) {
	if err := s.visible(ctx, "cancel", r, r.GetScope(), r.GetId()); err != nil {
		return nil, err
	}
	return &deliveriesv1.CancelDeliveryResponse{Delivery: s.del}, nil
}

func (s scopedFake) ErasePersonalData(ctx context.Context, r *deliveriesv1.ErasePersonalDataRequest) (*deliveriesv1.ErasePersonalDataResponse, error) {
	if err := s.visible(ctx, "erase", r, r.GetScope(), r.GetId()); err != nil {
		return nil, err
	}
	return &deliveriesv1.ErasePersonalDataResponse{Delivery: s.del}, nil
}

// pathTarget is the value of the route's first {wildcard} in req's path, ""
// when the pattern has none.
func pathTarget(rt httpapi.Route, req *http.Request) string {
	pat := strings.Split(rt.Pattern, "/")
	got := strings.Split(req.URL.Path, "/")
	for i, seg := range pat {
		if strings.HasPrefix(seg, "{") && i < len(got) {
			return got[i]
		}
	}
	return ""
}

func denyAll() app.Decision {
	return app.Decision{Limit: 100, RetryAfter: time.Second, ResetAfter: time.Second}
}

type limiter struct{ deny bool }

func (l limiter) Allow(context.Context, string, app.Limit) (app.Decision, error) {
	if l.deny {
		return denyAll(), nil
	}
	return app.Decision{Allowed: true, Limit: 100, Remaining: 99}, nil
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
			Scope: domain.ScopeDeliveriesWrite, Env: httpapi.TestOnly, SuccessStatus: http.StatusCreated, Handler: h(http.StatusCreated)},
		{Name: "test.webhooks.get", Method: http.MethodGet, Pattern: "/v1/test/webhooks", Credentials: creds,
			Scope: domain.ScopeWebhooksWrite, Env: httpapi.AnyEnv, SuccessStatus: http.StatusOK, Handler: h(http.StatusOK)},
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
				// Production routes get a fresh fake per cell; the two
				// test-only routes carry their own handlers.
				fake := scopedFake{newFake()}
				counted := rt
				for _, pr := range httpapi.Routes(httpapi.Deps{Deliveries: fake}) {
					if pr.Name == rt.Name {
						counted = pr
					}
				}
				rt := counted
				inner := rt.Handler
				counted.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					handled.Add(1)
					inner.ServeHTTP(w, r)
				})
				audit := &memAudit{}
				e := newEdge(t, keys, withRoutes(counted), withMiddleware(func(m *httpapi.Middleware) {
					m.Spec = matrixSpec(t)
					m.FailedAuth = httpapi.FailedAuthLimit(allowAll{}, m.Log)
					m.RateLimit = httpapi.PartnerRateLimit(limiter{deny: cell.Limited}, m.Log)
					m.Idempotency = httpapi.Idempotency(&matrixStore{seen: map[app.IdempotencyScope][]byte{}}, fixedClock{}, m.Log)
					m.Audit = httpapi.Audit(audit, fixedClock{}, m.Log)
				}))

				req := baseRequest(rt)
				cell.Apply(req)
				rec := e.do(req)

				status, code := cell.Want(rt)
				rows := audit.all()
				// M1 audits every authenticated write at its final status,
				// refusals included (insufficient_scope, 404s). Requests
				// refused before a principal exists (401, the Origin and IP
				// 403s of Authenticate) have no actor, and 429s are skipped
				// on purpose, so neither leaves a row.
				audited := rt.Method != http.MethodGet && status != http.StatusUnauthorized && status != http.StatusTooManyRequests &&
					code != "secret_key_in_browser" && code != "ip_not_allowed"
				if audited {
					if len(rows) != 1 {
						t.Fatalf("audit rows = %d, want exactly 1: %+v", len(rows), rows)
					}
					got, wantTarget := rows[0], pathTarget(rt, req)
					if rt.Name == "deliveries.create" && code == "" {
						wantTarget = delID // the id the service just created
					}
					if got.Action != rt.Name || got.Status != status || got.Target != wantTarget {
						t.Fatalf("audit row = {action %q status %d target %q}, want {%q %d %q}",
							got.Action, got.Status, got.Target, rt.Name, status, wantTarget)
					}
				} else if len(rows) != 0 {
					t.Fatalf("audit rows = %d for status %d on %s, want none: %+v", len(rows), status, rt.Method, rows)
				}

				if code != "" {
					wantProblem(t, rec, status, code)
					wantHandled, wantRPC := int64(0), 0
					if cell.Reached {
						wantHandled, wantRPC = 1, 1
					}
					if n := handled.Load(); n != wantHandled {
						t.Fatalf("handler ran %d times, want %d", n, wantHandled)
					}
					if n := fake.count(); n != wantRPC {
						t.Fatalf("deliveries calls = %d, want %d", n, wantRPC)
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
