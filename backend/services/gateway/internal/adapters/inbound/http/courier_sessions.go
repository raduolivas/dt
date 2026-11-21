package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

// courierSessionsAPI serves the partner's courier-session routes. Every call
// is scoped to the authenticated principal's tenant and environment.
type courierSessionsAPI struct {
	svc     CourierSessionService // nil: service_unavailable
	urlBase string                // no trailing slash
	log     *slog.Logger
}

// courierSessionJSON is the public CourierSession.
type courierSessionJSON struct {
	ID         string     `json:"id"`
	CourierID  string     `json:"courier_id"`
	Mode       string     `json:"mode"`
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"started_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	EndedAt    *time.Time `json:"ended_at"`
	EndReason  *string    `json:"end_reason"`
	LastSeenAt *time.Time `json:"last_seen_at"`
}

// toCourierSessionJSON renders s as seen now (s is a View). A session still
// waiting for its link code to be exchanged has not started: the contract's
// started_at is then when it was created, and expires_at is null.
func toCourierSessionJSON(s domain.CourierSession) courierSessionJSON {
	j := courierSessionJSON{ID: s.ID, CourierID: s.CourierID, Mode: string(s.Mode), Status: string(s.State),
		StartedAt: s.CreatedAt, ExpiresAt: s.ExpiresAt, EndedAt: s.EndedAt, LastSeenAt: s.LastSeenAt}
	if s.StartedAt != nil {
		j.StartedAt = *s.StartedAt
	}
	if s.EndReason != "" {
		r := string(s.EndReason)
		j.EndReason = &r
	}
	return j
}

// courierSessionCreatedJSON is the public CourierSessionCreated: the tokens
// (sdk) or the link (link), shown once.
type courierSessionCreatedJSON struct {
	Session         courierSessionJSON `json:"session"`
	AccessToken     string             `json:"access_token,omitempty"`
	AccessExpiresAt *time.Time         `json:"access_expires_at,omitempty"`
	RefreshToken    string             `json:"refresh_token,omitempty"`
	LinkURL         string             `json:"link_url,omitempty"`
	LinkExpiresAt   *time.Time         `json:"link_expires_at,omitempty"`
}

// principal returns the caller, or answers 503 when sessions are not wired.
func (a *courierSessionsAPI) principal(w http.ResponseWriter, r *http.Request) (domain.Principal, bool) {
	p, ok := PrincipalFrom(r.Context())
	if !ok {
		internalError(w, r) // Mount always authenticates first
		return domain.Principal{}, false
	}
	if a.svc == nil {
		httpx.WriteProblem(w, r, unavailableProblem())
		return domain.Principal{}, false
	}
	return p, true
}

// create serves POST /v1/courier-sessions. The audit target is the new
// session's id, which also names the resource of the idempotency record.
func (a *courierSessionsAPI) create(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	var in struct {
		CourierID        string `json:"courier_id"`
		Mode             string `json:"mode"`
		ExpiresInSeconds int    `json:"expires_in_seconds"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	res, err := a.svc.Start(r.Context(), p, in.CourierID, in.Mode, time.Duration(in.ExpiresInSeconds)*time.Second)
	switch {
	case errors.Is(err, app.ErrCourierNotFound):
		httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusNotFound, Code: "courier_not_found",
			Detail: "No courier with this id exists in this environment."})
		return
	case errors.Is(err, app.ErrSessionMode), errors.Is(err, domain.ErrSessionLength):
		// The OpenAPI validator already refused these.
		httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusBadRequest, Code: "validation_failed",
			Detail: "The request does not match the API contract."})
		return
	case err != nil:
		a.fail(w, r, err, "courier_session_start_failed")
		return
	}
	SetAuditTarget(r, res.Session.ID)
	countSessionStarted(r.Context(), string(res.Session.Mode))
	out := courierSessionCreatedJSON{Session: toCourierSessionJSON(res.Session)}
	if res.Session.Mode == domain.ModeSDK {
		out.AccessToken, out.AccessExpiresAt, out.RefreshToken = res.Tokens.AccessToken, &res.Tokens.AccessExpiresAt, res.Tokens.RefreshToken
	} else {
		out.LinkURL, out.LinkExpiresAt = a.urlBase+"/s/"+res.LinkCode, &res.LinkExpiresAt
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, out)
}

// get serves GET /v1/courier-sessions/{id}.
func (a *courierSessionsAPI) get(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	s, err := a.svc.Get(r.Context(), p, r.PathValue("id"))
	a.reply(w, r, s, err, "courier_session_get_failed")
}

// end serves DELETE /v1/courier-sessions/{id}: the partner ends the session.
// Only the reason "partner" is ever passed; ending an ended session is a 200.
func (a *courierSessionsAPI) end(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	s, err := a.svc.End(r.Context(), p, r.PathValue("id"), string(domain.EndPartner))
	a.reply(w, r, s, err, "courier_session_end_failed")
}

func (a *courierSessionsAPI) reply(w http.ResponseWriter, r *http.Request, s domain.CourierSession, err error, event string) {
	if errors.Is(err, app.ErrCourierSessionNotFound) {
		httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusNotFound, Code: "not_found"})
		return
	}
	if err != nil {
		a.fail(w, r, err, event)
		return
	}
	writeJSON(w, http.StatusOK, toCourierSessionJSON(s))
}

// fail logs err (ids only, never a token) and answers 500.
func (a *courierSessionsAPI) fail(w http.ResponseWriter, r *http.Request, err error, event string) {
	attrs := []any{"error", err.Error(), "request_id", httpx.RequestIDFrom(r.Context())}
	if p, ok := PrincipalFrom(r.Context()); ok {
		attrs = append(attrs, "tenant_id", p.TenantID)
	}
	a.log.ErrorContext(r.Context(), event, attrs...)
	internalError(w, r)
}
