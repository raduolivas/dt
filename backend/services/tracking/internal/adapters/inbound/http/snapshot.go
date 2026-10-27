package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/services/tracking/internal/app"
)

// snapshot serves GET /v1/tracking/snapshot: the current snapshot for the
// bearer token's delivery, refused when the token is invalid or its link
// revoked or expired; 30 a minute per token.
func (h *Hub) snapshot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	token, ok := bearerToken(r)
	if !ok {
		unauthorized(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
	defer cancel()
	c, err := h.verify(ctx, token)
	switch {
	case errors.Is(err, errUnavailable):
		unavailable(w, r)
		return
	case err != nil:
		unauthorized(w, r)
		return
	}
	// The limit is per token: keyed by a hash, never the token itself.
	sum := sha256.Sum256([]byte(token))
	allowed, retry, err := h.d.Links.AllowSnapshot(ctx, hex.EncodeToString(sum[:16]))
	if err != nil {
		h.log.WarnContext(ctx, "viewer_limit_failed", "error", err)
		unavailable(w, r)
		return
	}
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retry.Seconds()))))
		httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusTooManyRequests, Code: "rate_limited"})
		return
	}
	revoked, err := h.d.Links.Revoked(ctx, c.LinkID)
	if err != nil {
		h.log.WarnContext(ctx, "viewer_link_check_failed", "error", err)
		unavailable(w, r)
		return
	}
	at, known, err := h.d.Links.Expiry(ctx, c.LinkID)
	if err != nil {
		h.log.WarnContext(ctx, "viewer_link_check_failed", "error", err)
		unavailable(w, r)
		return
	}
	if revoked || (known && !time.Now().Before(at)) {
		unauthorized(w, r)
		return
	}
	snap, err := h.d.Snapshots.ViewerSnapshot(ctx, scopeOf(c), c.DeliveryID)
	switch {
	case errors.Is(err, app.ErrNotFound):
		unauthorized(w, r)
		return
	case err != nil:
		h.log.WarnContext(ctx, "viewer_snapshot_failed", "delivery_id", c.DeliveryID, "error", err)
		unavailable(w, r)
		return
	}
	body, err := json.Marshal(sanitize(snap))
	if err != nil {
		unavailable(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
