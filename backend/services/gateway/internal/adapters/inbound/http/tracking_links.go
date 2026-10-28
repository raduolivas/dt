package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

// trackingLinkJSON is the public TrackingLink.
type trackingLinkJSON struct {
	URL       string     `json:"url"`
	Token     string     `json:"token"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// Issuing a link after a create: up to 3 quick retries, all within a bound.
const (
	issueAttempts = 4
	issueBackoff  = 25 * time.Millisecond
	issueBudget   = 2 * time.Second
)

func (a *deliveriesAPI) linkJSON(l domain.TrackingLink, token string) *trackingLinkJSON {
	return &trackingLinkJSON{URL: a.trackingBase + "/t/" + token, Token: token, ExpiresAt: l.ExpiresAt}
}

// issueLink issues the new delivery's tracking link. A failure after the
// retries is logged (ids only) and answered as tracking null: the delivery
// exists, and a 5xx would make the client's idempotent retry re-run the
// create and get external_id_conflict. The partner then calls rotate.
func (a *deliveriesAPI) issueLink(r *http.Request, deliveryID string) *trackingLinkJSON {
	if a.links == nil {
		return nil
	}
	p, ok := PrincipalFrom(r.Context())
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), issueBudget)
	defer cancel()
	var err error
	for i := range issueAttempts {
		if i > 0 {
			select {
			case <-ctx.Done():
			case <-time.After(issueBackoff << (i - 1)):
			}
		}
		if ctx.Err() != nil {
			break
		}
		var l domain.TrackingLink
		var tok string
		if l, tok, err = a.links.Issue(ctx, p, deliveryID); err == nil {
			return a.linkJSON(l, tok)
		}
	}
	if err == nil {
		err = ctx.Err()
	}
	a.log.ErrorContext(r.Context(), "tracking_link_issue_failed",
		"error", err.Error(), "tenant_id", p.TenantID, "delivery_id", deliveryID,
		"request_id", httpx.RequestIDFrom(r.Context()))
	return nil
}

// rotateTrackingLink serves POST /deliveries/{id}/tracking-link/rotate. The
// delivery is looked up first, so another tenant's (or an unknown) delivery
// is 404 and gets no link. Then the current link, if any, is revoked and a
// new one returned.
func (a *deliveriesAPI) rotateTrackingLink(w http.ResponseWriter, r *http.Request) {
	sc, ok := scopeFor(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if _, err := callRPC(r.Context(), func(ctx context.Context) (any, error) {
		return a.c.GetDelivery(ctx, deliveriesGetRequest(sc, id))
	}); err != nil {
		writeRPCError(w, r, a.log, err)
		return
	}
	p, _ := PrincipalFrom(r.Context())
	if a.links == nil {
		trackingUnavailable(w, r)
		return
	}
	l, tok, err := a.links.Rotate(r.Context(), p, id)
	if err != nil {
		a.log.ErrorContext(r.Context(), "tracking_link_rotate_failed",
			"error", err.Error(), "tenant_id", p.TenantID, "delivery_id", id,
			"request_id", httpx.RequestIDFrom(r.Context()))
		trackingUnavailable(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, a.linkJSON(l, tok))
}

func trackingUnavailable(w http.ResponseWriter, r *http.Request) {
	httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusServiceUnavailable, Code: "tracking_unavailable",
		Detail: "Tracking is temporarily unavailable. Retry shortly."})
}
