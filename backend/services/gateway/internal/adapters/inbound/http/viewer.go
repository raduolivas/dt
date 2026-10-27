package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/services/gateway/api"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
)

// MaxViewerBodyBytes is the viewer request body limit.
const MaxViewerBodyBytes = 1 << 10

// ViewerDeps configures the customer viewer routes.
type ViewerDeps struct {
	// Viewer runs the session exchange and refresh. Required.
	Viewer *app.Viewer
	// Limiter applies app.ViewerIP per client IP (nil disables it; a
	// failing limiter lets requests through).
	Limiter app.Limiter
	// JWKS is the public key set served at JWKSPath.
	JWKS []byte
	// TrustedProxies are as for Middleware.
	TrustedProxies []netip.Prefix
	// Spec is the OpenAPI document; nil means the embedded contract.
	Spec *openapi3.T
	// Clock is the time tokens are checked and issued at; nil means now.
	Clock app.Clock
	// Log receives server-side failures (never tokens). Nil means
	// slog.Default().
	Log *slog.Logger
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

// MountViewer registers the customer viewer routes on mux, apart from the
// partner API chain: no secret key authenticates anything here.
//
//	POST /v1/tracking/session          link token → viewer token + snapshot
//	POST /v1/tracking/session/refresh  viewer token → new viewer token
//	OPTIONS on both                    CORS preflight (204)
//	GET  /.well-known/jwks.json        the viewer token keys
//
// Each POST runs: ViewerCORS → MaxBytes(1 KiB) → ClientIP(trusted) →
// rl:viewer:ip:<ip> (10/min) → Validate (OpenAPI) → handler; the handlers
// charge rl:viewer:tl:<link id> (30/min) through the Viewer. Responses carry
// Cache-Control: no-store. Call it once per mux, after or before Mount.
func MountViewer(mux *http.ServeMux, d ViewerDeps) {
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	clock := d.Clock
	if clock == nil {
		clock = wallClock{}
	}
	spec := d.Spec
	if spec == nil {
		var err error
		if spec, err = LoadSpec(api.Spec); err != nil {
			panic("httpapi: embedded openapi.yaml: " + err.Error())
		}
	}
	validate := Validate(spec, log)
	v := &viewerAPI{v: d.Viewer, clock: clock, log: log}
	chain := func(h http.HandlerFunc) http.Handler {
		out := validate(h)
		out = viewerIPLimit(d.Limiter, log)(out)
		out = ClientIP(d.TrustedProxies)(out)
		out = MaxBytes(MaxViewerBodyBytes)(out)
		return ViewerCORS(out)
	}
	for path, h := range map[string]http.HandlerFunc{
		"/v1/tracking/session":         v.session,
		"/v1/tracking/session/refresh": v.refresh,
	} {
		mux.Handle("POST "+path, chain(h))
		mux.Handle("OPTIONS "+path, http.HandlerFunc(viewerPreflight))
	}
	mux.Handle("GET "+JWKSPath, jwksHandler(d.JWKS))
}

// viewerIPLimit charges rl:viewer:ip:<client ip> (rl:viewer:ip:unknown when
// it cannot be told) and answers 429 when the bucket is empty.
func viewerIPLimit(l app.Limiter, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if l == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := "rl:viewer:ip:unknown"
			if ip, ok := ClientIPFrom(r.Context()); ok && ip.IsValid() {
				key = "rl:viewer:ip:" + ip.String()
			}
			d, err := l.Allow(r.Context(), key, app.ViewerIP)
			switch {
			case err != nil:
				warnUnavailable(log, r, err)
			case !d.Allowed:
				rateLimited(w, r, d)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type viewerAPI struct {
	v     *app.Viewer
	clock app.Clock
	log   *slog.Logger
}

func (a *viewerAPI) session(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	jwt, exp, snap, err := a.v.Start(r.Context(), in.Token, a.clock.Now())
	if err != nil {
		a.fail(w, r, err, "link_invalid")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, struct {
		ViewerToken string          `json:"viewer_token"`
		ExpiresAt   time.Time       `json:"expires_at"`
		Snapshot    json.RawMessage `json:"snapshot"`
		Branding    *struct{}       `json:"branding"`
	}{ViewerToken: jwt, ExpiresAt: exp.UTC(), Snapshot: snap})
}

func (a *viewerAPI) refresh(w http.ResponseWriter, r *http.Request) {
	token, ok := bearerToken(r)
	if !ok {
		a.fail(w, r, app.ErrViewerTokenInvalid, "viewer_token_invalid")
		return
	}
	jwt, exp, err := a.v.Refresh(r.Context(), token, a.clock.Now())
	if err != nil {
		a.fail(w, r, err, "viewer_token_invalid")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, struct {
		ViewerToken string    `json:"viewer_token"`
		ExpiresAt   time.Time `json:"expires_at"`
	}{jwt, exp.UTC()})
}

// fail maps a viewer error; invalidCode is the route's 401 code. Logs carry
// the error only: no token, link id or IP beyond the access log.
func (a *viewerAPI) fail(w http.ResponseWriter, r *http.Request, err error, invalidCode string) {
	var rl *app.RateLimitedError
	switch {
	case errors.Is(err, app.ErrLinkInvalid), errors.Is(err, app.ErrViewerTokenInvalid):
		w.Header().Set("WWW-Authenticate", "Bearer")
		httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusUnauthorized, Code: invalidCode})
	case errors.As(err, &rl):
		rateLimited(w, r, rl.Decision)
	case errors.Is(err, app.ErrTrackingUnavailable):
		a.log.WarnContext(r.Context(), "viewer_tracking_unavailable",
			"error", err.Error(), "request_id", httpx.RequestIDFrom(r.Context()))
		trackingUnavailable(w, r)
	default:
		a.log.ErrorContext(r.Context(), "viewer_session_failed",
			"error", err.Error(), "request_id", httpx.RequestIDFrom(r.Context()))
		internalError(w, r)
	}
}
