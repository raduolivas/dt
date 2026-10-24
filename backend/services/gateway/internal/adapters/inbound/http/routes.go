package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/raduolivas/dt/backend/gen/dt/deliveries/v1/deliveriesv1connect"
	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/services/gateway/api"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

// EnvRule says which environments may call a route.
type EnvRule int

const (
	// AnyEnv accepts test and live credentials.
	AnyEnv EnvRule = iota
	// TestOnly refuses live credentials with 403 environment_not_allowed.
	TestOnly
)

// Route is one public endpoint. The route table is the single source of truth
// for registration and for the authorization-matrix test.
type Route struct {
	Name        string // audit action, e.g. "me.get"
	Method      string
	Pattern     string // stdlib mux pattern under /v1, e.g. "/v1/me"
	Credentials []domain.CredentialType
	Scope       domain.Scope // "" = any valid credential
	Env         EnvRule
	// SuccessStatus is the status a fully authorized, valid request gets (the
	// authorization matrix expects it). Upsert routes that can answer two
	// statuses name the usual one.
	SuccessStatus int
	Handler       http.Handler
}

// Deps are what the route handlers need.
type Deps struct {
	// Deliveries is the deliveries internal RPC client.
	Deliveries deliveriesv1connect.DeliveriesServiceClient
	// Idempotency deletes a delivery's stored idempotency responses once its
	// personal data is erased. Nil skips the purge.
	Idempotency app.IdempotencyPurger
	// Log receives failures of the deliveries calls. Nil means slog.Default().
	Log *slog.Logger
}

// Routes is the public route table.
func Routes(deps Deps) []Route {
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}
	d := &deliveriesAPI{c: deps.Deliveries, purger: deps.Idempotency, log: log}
	secret := []domain.CredentialType{domain.CredentialSecretKey}
	route := func(name, method, pattern string, scope domain.Scope, status int, h http.HandlerFunc) Route {
		return Route{Name: name, Method: method, Pattern: pattern, Credentials: secret, Scope: scope,
			Env: AnyEnv, SuccessStatus: status, Handler: h}
	}
	const (
		read     = domain.ScopeDeliveriesRead
		write    = domain.ScopeDeliveriesWrite
		couriers = domain.ScopeCouriersWrite
	)
	return []Route{
		route("me.get", http.MethodGet, "/v1/me", "", http.StatusOK, getMe),
		route("deliveries.create", http.MethodPost, "/v1/deliveries", write, http.StatusCreated, d.create),
		route("deliveries.list", http.MethodGet, "/v1/deliveries", read, http.StatusOK, d.list),
		route("deliveries.get", http.MethodGet, "/v1/deliveries/{id}", read, http.StatusOK, d.get),
		route("deliveries.update", http.MethodPatch, "/v1/deliveries/{id}", write, http.StatusOK, d.update),
		route("deliveries.assign", http.MethodPost, "/v1/deliveries/{id}/assignment", write, http.StatusOK, d.assign),
		route("deliveries.report_event", http.MethodPost, "/v1/deliveries/{id}/events", write, http.StatusOK, d.reportEvent),
		route("deliveries.cancel", http.MethodPost, "/v1/deliveries/{id}/cancel", write, http.StatusOK, d.cancel),
		route("deliveries.erase_personal_data", http.MethodDelete, "/v1/deliveries/{id}/personal-data", write, http.StatusOK, d.erasePersonalData),
		route("couriers.upsert", http.MethodPut, "/v1/couriers/{external_id}", couriers, http.StatusOK, d.upsertCourier),
	}
}

// Authenticator resolves a raw secret key used from ip to a principal; it is
// satisfied by *app.Authenticator.
type Authenticator interface {
	Authenticate(ctx context.Context, raw string, ip netip.Addr) (domain.Principal, error)
}

// Hook wraps a route's handler at a fixed point of the chain. A nil Hook is a
// no-op.
type Hook func(next http.Handler, rt Route) http.Handler

// Middleware configures the per-route chain Mount builds.
type Middleware struct {
	// Auth authenticates secret keys. Required.
	Auth Authenticator
	// TrustedProxies are the proxies whose X-Forwarded-For hops count (see
	// ClientIP). Empty means the TCP peer is the client.
	TrustedProxies []netip.Prefix
	// Spec is the OpenAPI document requests are validated against. Nil means
	// the embedded production contract (api.Spec).
	Spec *openapi3.T
	// Log receives server-side failures (never credentials). Nil means
	// slog.Default().
	Log *slog.Logger

	// FailedAuth (Task 10) wraps Authenticate: it sees the client IP but no
	// principal, and observes the 401s Authenticate writes.
	FailedAuth Hook
	// RateLimit (Task 10) runs right after Authenticate, with the
	// principal set and before Audit and Authorize, so every authenticated
	// request is counted, including those Authorize refuses.
	RateLimit Hook
	// Idempotency (Task 11) runs on POST routes only, after Authorize.
	Idempotency Hook
	// Audit (Task 12) runs on non-GET routes only, after RateLimit and
	// before Authorize, so it sees every authenticated write's final status
	// except a 429.
	Audit Hook
}

// MaxBodyBytes is the partner request body limit (spec §11.1).
const MaxBodyBytes = 64 << 10

// Mount registers every route on mux under its "METHOD /v1/..." pattern,
// wrapped in this chain, outermost first:
//
//	MaxBytes(64 KiB) → ClientIP(trusted) → [FailedAuth] → Authenticate →
//	[RateLimit] → [Audit, non-GET only] → Authorize(route) →
//	[Idempotency, POST only] → Validate (OpenAPI, security off) → handler
//
// FailedAuth wraps Authenticate so it can count the 401s Authenticate writes.
// RateLimit sits right inside Authenticate, so every authenticated request
// is counted, including those Authorize refuses with 403, and a 429 never
// reaches Audit (a key hammering the API cannot multiply audit writes). Audit
// sits outside Authorize, so every other authenticated write is recorded with
// its final status, including 403s, idempotency conflicts and replays (spec
// §11.1).
//
// Bracketed steps are Middleware hooks and are skipped when nil. Mount also
// registers the /v1/ fallbacks: OPTIONS answers 403 cors_not_allowed (the
// partner API never allows browser origins, so no Access-Control-* header is
// ever sent) and any other unmatched path answers 404 not_found. Call Mount
// once per mux. It panics if the spec cannot be loaded, a programming error.
func Mount(mux *http.ServeMux, routes []Route, mw Middleware) {
	log := mw.Log
	if log == nil {
		log = slog.Default()
	}
	spec := mw.Spec
	if spec == nil {
		var err error
		if spec, err = LoadSpec(api.Spec); err != nil {
			panic("httpapi: embedded openapi.yaml: " + err.Error())
		}
	}
	validate := Validate(spec, log)

	for _, rt := range routes {
		h := validate(rt.Handler)
		if mw.Idempotency != nil && rt.Method == http.MethodPost {
			h = mw.Idempotency(h, rt)
		}
		h = Authorize(rt)(h)
		if mw.Audit != nil && rt.Method != http.MethodGet {
			h = mw.Audit(h, rt)
		}
		if mw.RateLimit != nil {
			h = mw.RateLimit(h, rt)
		}
		h = Authenticate(mw.Auth, log)(h)
		if mw.FailedAuth != nil {
			h = mw.FailedAuth(h, rt)
		}
		h = ClientIP(mw.TrustedProxies)(h)
		h = MaxBytes(MaxBodyBytes)(h)
		mux.Handle(rt.Method+" "+rt.Pattern, h)
	}

	mux.Handle("OPTIONS /v1/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteProblem(w, r, httpx.Problem{
			Status: http.StatusForbidden, Code: "cors_not_allowed",
			Detail: "The API does not accept browser requests; call it from your server.",
		})
	}))
	mux.Handle("/v1/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusNotFound, Code: "not_found"})
	}))
}

// internalError answers 500 without detail; the caller logs the cause.
func internalError(w http.ResponseWriter, r *http.Request) {
	httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusInternalServerError, Code: "internal_error"})
}
