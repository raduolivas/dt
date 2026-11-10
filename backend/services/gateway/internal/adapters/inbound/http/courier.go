package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"google.golang.org/protobuf/types/known/timestamppb"

	deliveriesv1 "github.com/raduolivas/dt/backend/gen/dt/deliveries/v1"
	"github.com/raduolivas/dt/backend/gen/dt/deliveries/v1/deliveriesv1connect"
	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	trackingv1 "github.com/raduolivas/dt/backend/gen/dt/tracking/v1"
	"github.com/raduolivas/dt/backend/gen/dt/tracking/v1/trackingv1connect"
	"github.com/raduolivas/dt/backend/pkg/courierjwt"
	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/services/gateway/api"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

// MaxCourierBodyBytes is the body limit of the courier routes other than the
// location batch.
const MaxCourierBodyBytes = 1 << 10

// CourierService is what the courier routes need from the sessions service;
// it is satisfied by *app.CourierSessions.
type CourierService interface {
	Exchange(ctx context.Context, code, ip string) (app.Tokens, error)
	Refresh(ctx context.Context, refresh string) (app.Tokens, error)
	RefreshSession(ctx context.Context, refresh string) (string, error)
	Authenticate(ctx context.Context, accessToken string) (domain.Principal, courierjwt.Claims, error)
	EndSession(ctx context.Context, sid, reason string) (domain.CourierSession, error)
	Touch(ctx context.Context, sid string, now time.Time) error
}

// CourierDeps configures the courier routes.
type CourierDeps struct {
	// Sessions exchanges, refreshes, authenticates and ends sessions. Required.
	Sessions CourierService
	// Deliveries lists the courier's deliveries and takes their events. Nil
	// answers those routes 503 service_unavailable.
	Deliveries deliveriesv1connect.DeliveriesServiceClient
	// Tracking takes the courier's fixes. Nil answers 503 tracking_unavailable.
	Tracking trackingv1connect.TrackingServiceClient
	// Limiter holds the rl:courier:* buckets (nil disables them; a failing
	// limiter lets requests through).
	Limiter app.Limiter
	// TrustedProxies are as for Middleware.
	TrustedProxies []netip.Prefix
	// Spec is the OpenAPI document; nil means the embedded contract.
	Spec *openapi3.T
	// Clock is the time of Touch; nil means now.
	Clock app.Clock
	// Log receives server-side failures (never tokens). Nil means
	// slog.Default().
	Log *slog.Logger
}

type courierCtxKey struct{}

// courierClaims returns the verified token claims MountCourier's
// authentication stored in ctx.
func courierClaims(ctx context.Context) (courierjwt.Claims, bool) {
	c, ok := ctx.Value(courierCtxKey{}).(courierjwt.Claims)
	return c, ok
}

// MountCourier registers the courier routes on mux, apart from the partner
// API chain: no secret key authenticates anything here.
//
//	POST /v1/courier/token                      link code or refresh token → tokens
//	GET  /v1/courier/deliveries                 the courier's open deliveries
//	POST /v1/courier/locations                  a batch of fixes
//	POST /v1/courier/deliveries/{id}/events     a status tap
//	POST /v1/courier/session/end                the courier ends the session
//	OPTIONS on each                             CORS preflight (204)
//
// Each route runs: ViewerCORS (any origin, no credentials) → MaxBytes (16 KiB
// for locations, 1 KiB otherwise) → ClientIP(trusted) → [courier token
// authentication, every route but the token one] → its rate buckets →
// Validate (OpenAPI) → handler. The buckets are rl:courier:loc:<sid> (1/s,
// burst 5), rl:courier:evt:<sid> (10/min), rl:courier:read:<sid> (30/min, shared by the
// delivery listing and the session end) and, for a refresh token,
// rl:courier:tok:<sid> (10/min); a link code is charged to
// rl:courier:ip:<ip> (20/min) by the handler, which sees the grant type, and
// failed codes count. Responses carry Cache-Control: no-store. Call it once
// per mux, after or before Mount.
func MountCourier(mux *http.ServeMux, d CourierDeps) {
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
	a := &courierAPI{svc: d.Sessions, deliveries: d.Deliveries, tracking: d.Tracking, limiter: d.Limiter,
		clock: clock, log: log}
	validate := Validate(spec, log)
	type route struct {
		method, path string
		limit        int64
		h            http.HandlerFunc
		scope        domain.Scope // "" = any courier token; unused on the token route
		bucket       string       // key prefix of the session bucket, "" = none
		rate         app.Limit
	}
	routes := []route{
		{method: "POST", path: "/v1/courier/token", limit: MaxCourierBodyBytes, h: a.token},
		{method: "GET", path: "/v1/courier/deliveries", limit: MaxCourierBodyBytes, h: a.listDeliveries,
			bucket: "rl:courier:read:", rate: app.CourierRead},
		{method: "POST", path: "/v1/courier/locations", limit: MaxRelayBodyBytes, h: a.reportLocations,
			scope: domain.ScopeLocationWrite, bucket: "rl:courier:loc:", rate: app.CourierLocations},
		{method: "POST", path: "/v1/courier/deliveries/{id}/events", limit: MaxCourierBodyBytes, h: a.reportEvent,
			scope: domain.ScopeStatusWrite, bucket: "rl:courier:evt:", rate: app.CourierEvents},
		{method: "POST", path: "/v1/courier/session/end", limit: MaxCourierBodyBytes, h: a.endSession,
			bucket: "rl:courier:read:", rate: app.CourierRead},
	}
	for _, rt := range routes {
		var h http.Handler = rt.h
		h = validate(h)
		if rt.bucket != "" {
			h = a.sessionLimit(rt.bucket, rt.rate)(h)
		}
		if rt.path != "/v1/courier/token" {
			h = a.authenticate(rt.scope)(h)
		}
		h = ClientIP(d.TrustedProxies)(h)
		h = MaxBytes(rt.limit)(h)
		h = noStore(h)
		mux.Handle(rt.method+" "+rt.path, ViewerCORS(h))
		mux.Handle("OPTIONS "+rt.path, http.HandlerFunc(viewerPreflight))
	}
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

type courierAPI struct {
	svc        CourierService
	deliveries deliveriesv1connect.DeliveriesServiceClient
	tracking   trackingv1connect.TrackingServiceClient
	limiter    app.Limiter
	clock      app.Clock
	log        *slog.Logger
}

// authenticate verifies the courier access token and stores the principal
// and the claims. Anything but a live courier token (a secret key, a viewer
// token, an expired or revoked one) is the one indistinguishable 401; a
// deny-list that cannot be read fails closed with 503.
func (a *courierAPI) authenticate(scope domain.Scope) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := bearerToken(r)
			if !ok {
				unauthenticated(w, r)
				return
			}
			p, claims, err := a.svc.Authenticate(r.Context(), token)
			switch {
			case errors.Is(err, app.ErrUnauthenticated):
				unauthenticated(w, r)
				return
			case err != nil:
				a.log.ErrorContext(r.Context(), "courier_authenticate_failed",
					"error", err.Error(), "request_id", httpx.RequestIDFrom(r.Context()))
				httpx.WriteProblem(w, r, unavailableProblem())
				return
			}
			if scope != "" && !p.Has(scope) {
				httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusForbidden, Code: "insufficient_scope"})
				return
			}
			ctx := context.WithValue(r.Context(), principalKey{}, p)
			ctx = context.WithValue(ctx, courierCtxKey{}, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// charge counts one request against bucket key; false means the 429 has
// been written. A failing limiter lets the request through.
func (a *courierAPI) charge(w http.ResponseWriter, r *http.Request, key string, l app.Limit) bool {
	if a.limiter == nil {
		return true
	}
	d, err := a.limiter.Allow(r.Context(), key, l)
	switch {
	case err != nil:
		warnUnavailable(a.log, r, err)
	case !d.Allowed:
		rateLimited(w, r, d)
		return false
	}
	return true
}

// sessionLimit charges <prefix><session id> after authentication.
func (a *courierAPI) sessionLimit(prefix string, l app.Limit) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if c, ok := courierClaims(r.Context()); ok && !a.charge(w, r, prefix+c.SessionID, l) {
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// courierUnauthorized answers a refused token grant: 401 with the route's
// own code.
func courierUnauthorized(w http.ResponseWriter, r *http.Request, code string) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusUnauthorized, Code: code})
}

func grantInvalid(w http.ResponseWriter, r *http.Request, field, detail string) {
	httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusBadRequest, Code: "validation_failed",
		Detail: "The request does not match the API contract.",
		Errors: []httpx.FieldError{{Field: field, Code: "invalid", Message: detail}}})
}

// courierIPKey is the client IP ("" when unknown) and its token-route
// bucket, rl:courier:ip:<ip>, shared by link codes and unknown refresh tokens.
func courierIPKey(r *http.Request) (string, string) {
	if addr, ok := ClientIPFrom(r.Context()); ok && addr.IsValid() {
		return addr.String(), "rl:courier:ip:" + addr.String()
	}
	return "", "rl:courier:ip:unknown"
}

// token serves POST /v1/courier/token. The contract's schema is a flat
// object, so the pairing of grant_type and its field is checked here.
func (a *courierAPI) token(w http.ResponseWriter, r *http.Request) {
	var in struct {
		GrantType    string  `json:"grant_type"`
		Code         *string `json:"code"`
		RefreshToken *string `json:"refresh_token"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	var tokens app.Tokens
	var err error
	refreshing := false
	switch {
	case in.GrantType == "link_code" && in.Code != nil && in.RefreshToken == nil:
		ip, key := courierIPKey(r)
		if !a.charge(w, r, key, app.CourierLinkIP) {
			return
		}
		tokens, err = a.svc.Exchange(r.Context(), *in.Code, ip)
	case in.GrantType == "refresh_token" && in.RefreshToken != nil && in.Code == nil:
		refreshing = true
		var sid string
		if sid, err = a.svc.RefreshSession(r.Context(), *in.RefreshToken); err == nil {
			if !a.charge(w, r, "rl:courier:tok:"+sid, app.CourierRefresh) {
				return
			}
			tokens, err = a.svc.Refresh(r.Context(), *in.RefreshToken)
		} else if errors.Is(err, app.ErrRefreshInvalid) {
			// An unknown token has no session bucket: charge the failure to
			// the client IP, like a failed link code. Only failures count.
			if _, key := courierIPKey(r); !a.charge(w, r, key, app.CourierLinkIP) {
				return
			}
		}
	case in.GrantType == "link_code":
		grantInvalid(w, r, "code", "is required with grant_type link_code, and refresh_token must be absent")
		return
	default:
		grantInvalid(w, r, "refresh_token", "is required with grant_type refresh_token, and code must be absent")
		return
	}
	if refreshing {
		countRefreshOutcome(r.Context(), err)
	}
	switch {
	case err == nil:
	case errors.Is(err, app.ErrLinkCodeInvalid):
		courierUnauthorized(w, r, "link_code_invalid")
		return
	case errors.Is(err, app.ErrLinkCodeUsed):
		courierUnauthorized(w, r, "link_code_used")
		return
	case errors.Is(err, app.ErrRefreshInvalid), errors.Is(err, app.ErrRefreshReuse):
		courierUnauthorized(w, r, "refresh_token_invalid")
		return
	case errors.Is(err, app.ErrSessionEnded):
		courierUnauthorized(w, r, "session_ended")
		return
	default:
		a.log.ErrorContext(r.Context(), "courier_token_failed",
			"error", err.Error(), "request_id", httpx.RequestIDFrom(r.Context()))
		internalError(w, r)
		return
	}
	// expires_in is relative, so a client whose clock is off can still time
	// its refresh (M5-R18).
	expiresIn := max(0, int64(tokens.AccessExpiresAt.Sub(a.clock.Now())/time.Second))
	writeJSON(w, http.StatusOK, struct {
		AccessToken      string    `json:"access_token"`
		AccessExpiresAt  time.Time `json:"access_expires_at"`
		ExpiresIn        int64     `json:"expires_in"`
		RefreshToken     string    `json:"refresh_token"`
		SessionExpiresAt time.Time `json:"session_expires_at"`
	}{tokens.AccessToken, tokens.AccessExpiresAt.UTC(), expiresIn, tokens.RefreshToken, tokens.SessionExpiresAt.UTC()})
}

// deliveriesScope is the RPC scope of the authenticated courier session.
func (a *courierAPI) deliveriesScope(w http.ResponseWriter, r *http.Request) (*deliveriesv1.Scope, courierjwt.Claims, bool) {
	claims, ok := courierClaims(r.Context())
	if !ok {
		internalError(w, r) // authenticate always runs first
		return nil, claims, false
	}
	if a.deliveries == nil {
		httpx.WriteProblem(w, r, unavailableProblem())
		return nil, claims, false
	}
	sc, ok := scopeFor(w, r)
	return sc, claims, ok
}

type courierPickupJSON struct {
	Lat   float64 `json:"lat"`
	Lng   float64 `json:"lng"`
	Label string  `json:"label"`
}

type courierDropoffJSON struct {
	Lat   float64 `json:"lat"`
	Lng   float64 `json:"lng"`
	Label *string `json:"label"`
	Notes *string `json:"notes"`
}

// courierDeliveryJSON is the public CourierDelivery: only what the courier
// needs to do the job.
type courierDeliveryJSON struct {
	ID                  string                `json:"id"`
	Reference           string                `json:"reference"`
	Model               string                `json:"model"`
	Status              string                `json:"status"`
	ReadyAt             *time.Time            `json:"ready_at"`
	Pickup              courierPickupJSON     `json:"pickup"`
	Dropoff             courierDropoffJSON    `json:"dropoff"`
	ItemsSummary        *string               `json:"items_summary"`
	ItemCount           *int32                `json:"item_count"`
	PickupConfirmations []pickupConfirmationJ `json:"pickup_confirmations"`
	NextActions         []string              `json:"next_actions"`
}

func confirmations(in []*deliveriesv1.PickupConfirmation) []pickupConfirmationJ {
	out := make([]pickupConfirmationJ, 0, len(in))
	for _, pc := range in {
		out = append(out, pickupConfirmationJ{By: pc.GetBy(), At: pc.GetAt().AsTime()})
	}
	return out
}

func toCourierDeliveryJSON(d *deliveriesv1.CourierDelivery) courierDeliveryJSON {
	out := courierDeliveryJSON{ID: d.GetId(), Reference: d.GetReference(), Model: d.GetModel(), Status: d.GetStatus(),
		Pickup: courierPickupJSON{Lat: d.GetPickup().GetLocation().GetLat(), Lng: d.GetPickup().GetLocation().GetLng(),
			Label: d.GetPickup().GetLabel()},
		Dropoff: courierDropoffJSON{Lat: d.GetDropoff().GetLocation().GetLat(), Lng: d.GetDropoff().GetLocation().GetLng(),
			Label: d.GetDropoff().Label, Notes: d.GetDropoff().Notes},
		ItemsSummary: d.ItemsSummary, ItemCount: d.ItemCount,
		PickupConfirmations: confirmations(d.GetPickupConfirmations()), NextActions: nonNil(d.GetNextActions())}
	out.ReadyAt = optTime(d.GetReadyAt())
	return out
}

// courierViewOf is the courier's view of a full delivery, as returned by an
// event report: the same fields as the listing, nothing else.
func courierViewOf(d *deliveriesv1.Delivery, next []string) courierDeliveryJSON {
	return toCourierDeliveryJSON(&deliveriesv1.CourierDelivery{
		Id: d.GetId(), Reference: d.GetExternalId(), Model: d.GetDeliveryModel(), Status: d.GetStatus(),
		ReadyAt:      d.GetReadyAt(),
		Pickup:       &deliveriesv1.CourierPickup{Location: d.GetPickup().GetLocation(), Label: d.GetPickup().GetName()},
		Dropoff:      &deliveriesv1.CourierDropoff{Location: d.GetDropoff().GetLocation(), Label: d.GetDropoff().Label, Notes: d.Notes},
		ItemsSummary: d.ItemsSummary, ItemCount: d.ItemCount,
		PickupConfirmations: d.GetPickupConfirmations(), NextActions: next,
	})
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// listCourier asks deliveries for the session's courier's open deliveries.
func (a *courierAPI) listCourier(r *http.Request, sc *deliveriesv1.Scope, courierID string) (*deliveriesv1.ListCourierDeliveriesResponse, error) {
	return callRPC(r.Context(), func(ctx context.Context) (*deliveriesv1.ListCourierDeliveriesResponse, error) {
		return a.deliveries.ListCourierDeliveries(ctx, &deliveriesv1.ListCourierDeliveriesRequest{Scope: sc, CourierId: courierID})
	})
}

// listDeliveries serves GET /v1/courier/deliveries.
func (a *courierAPI) listDeliveries(w http.ResponseWriter, r *http.Request) {
	sc, claims, ok := a.deliveriesScope(w, r)
	if !ok {
		return
	}
	res, err := a.listCourier(r, sc, claims.CourierID)
	if err != nil {
		writeRPCError(w, r, a.log, err)
		return
	}
	out := make([]courierDeliveryJSON, 0, len(res.GetDeliveries()))
	for _, d := range res.GetDeliveries() {
		out = append(out, toCourierDeliveryJSON(d))
	}
	writeJSON(w, http.StatusOK, struct {
		Deliveries []courierDeliveryJSON `json:"deliveries"`
	}{out})
}

// reportEvent serves POST /v1/courier/deliveries/{id}/events. The token's
// courier is the ownership guard, checked by deliveries under the row lock
// (M5-R16): a delivery not assigned to it, whoever owns it, is 404, and the
// owner's retry of a final tap is still duplicate.
func (a *courierAPI) reportEvent(w http.ResponseWriter, r *http.Request) {
	sc, claims, ok := a.deliveriesScope(w, r)
	if !ok {
		return
	}
	var in struct {
		EventID    string     `json:"event_id"`
		Type       string     `json:"type"`
		OccurredAt *time.Time `json:"occurred_at"`
		Reason     string     `json:"reason"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	req := &deliveriesv1.ReportEventRequest{Scope: sc, Id: r.PathValue("id"), EventId: in.EventID, Type: in.Type,
		Reason: in.Reason, Actor: "courier", CourierId: claims.CourierID}
	if in.OccurredAt != nil {
		req.OccurredAt = timestamppb.New(*in.OccurredAt)
	}
	res, err := callRPC(r.Context(), func(ctx context.Context) (*deliveriesv1.ReportEventResponse, error) {
		return a.deliveries.ReportEvent(ctx, req)
	})
	if err != nil {
		writeRPCError(w, r, a.log, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Outcome  string              `json:"outcome"`
		Delivery courierDeliveryJSON `json:"delivery"`
	}{res.GetOutcome(), courierViewOf(res.GetDelivery(), res.GetNextActions())})
}

// reportLocations serves POST /v1/courier/locations: the relay's rules, for
// the token's courier, with the token's source (sdk or link).
func (a *courierAPI) reportLocations(w http.ResponseWriter, r *http.Request) {
	claims, ok := courierClaims(r.Context())
	if !ok {
		internalError(w, r) // authenticate always runs first
		return
	}
	if a.tracking == nil {
		trackingUnavailable(w, r)
		return
	}
	var in struct {
		Fixes []struct {
			T          time.Time `json:"t"`
			Lat        float64   `json:"lat"`
			Lng        float64   `json:"lng"`
			AccuracyM  float64   `json:"accuracy_m"`
			SpeedMps   *float64  `json:"speed_mps"`
			HeadingDeg *float64  `json:"heading_deg"`
		} `json:"fixes"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	fixes := make([]*trackingv1.Fix, len(in.Fixes))
	for i, f := range in.Fixes {
		fixes[i] = &trackingv1.Fix{T: timestamppb.New(f.T), Lat: f.Lat, Lng: f.Lng, AccuracyM: f.AccuracyM,
			SpeedMps: f.SpeedMps, HeadingDeg: f.HeadingDeg}
	}
	env := eventsv1.Environment_ENVIRONMENT_TEST
	if domain.Environment(claims.Env) == domain.EnvLive {
		env = eventsv1.Environment_ENVIRONMENT_LIVE
	}
	sc := &trackingv1.Scope{TenantId: claims.TenantID, Environment: env, ActorId: claims.SessionID}
	res, err := callRPC(r.Context(), func(ctx context.Context) (*trackingv1.IngestResponse, error) {
		return a.tracking.IngestLocations(ctx, &trackingv1.IngestRequest{Scope: sc, CourierId: claims.CourierID,
			Fixes: fixes, Source: claims.Source})
	})
	if err != nil {
		writeRPCError(w, r, a.log, err)
		return
	}
	// last_seen_at is a convenience for the partner: a failed write must not
	// fail the batch.
	if err := a.svc.Touch(r.Context(), claims.SessionID, a.clock.Now()); err != nil {
		a.log.WarnContext(r.Context(), "courier_touch_failed",
			"error", err.Error(), "request_id", httpx.RequestIDFrom(r.Context()))
	}
	writeJSON(w, http.StatusOK, struct {
		Accepted int32 `json:"accepted"`
		Dropped  int32 `json:"dropped"`
	}{res.GetAccepted(), res.GetDropped()})
}

// endSession serves POST /v1/courier/session/end. The app service audits the
// end; the reason is always "courier".
func (a *courierAPI) endSession(w http.ResponseWriter, r *http.Request) {
	claims, ok := courierClaims(r.Context())
	if !ok {
		internalError(w, r) // authenticate always runs first
		return
	}
	_, err := a.svc.EndSession(r.Context(), claims.SessionID, string(domain.EndCourier))
	switch {
	case errors.Is(err, app.ErrCourierSessionNotFound):
		unauthenticated(w, r)
		return
	case err != nil:
		a.log.ErrorContext(r.Context(), "courier_session_end_failed",
			"error", err.Error(), "request_id", httpx.RequestIDFrom(r.Context()))
		internalError(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// countRefreshOutcome records how a refresh_token grant ended. A rate-limited
// attempt returns before this runs (dt.ratelimit.rejections counts it), and
// an internal error fits no outcome, so neither is counted here.
func countRefreshOutcome(ctx context.Context, err error) {
	switch {
	case err == nil:
		countRefresh(ctx, refreshOK)
	case errors.Is(err, app.ErrRefreshReuse):
		countRefresh(ctx, refreshReuse)
	case errors.Is(err, app.ErrSessionEnded):
		countRefresh(ctx, refreshExpired)
	case errors.Is(err, app.ErrRefreshInvalid):
		countRefresh(ctx, refreshInvalid)
	}
}
