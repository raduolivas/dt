package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/pkg/viewerjwt"
)

// maxTokenBytes bounds a viewer token (a few hundred bytes in practice).
const maxTokenBytes = 2 << 10

var (
	errTokenInvalid = errors.New("viewer token invalid")
	errUnavailable  = errors.New("viewer verification unavailable")
)

// verify checks a viewer token. Errors are errTokenInvalid or
// errUnavailable (keys could not be loaded); neither carries the token.
func (h *Hub) verify(ctx context.Context, token string) (viewerjwt.Claims, error) {
	if token == "" || len(token) > maxTokenBytes {
		return viewerjwt.Claims{}, errTokenInvalid
	}
	c, err := h.d.Verifier.Verify(ctx, token, time.Now())
	switch {
	case err == nil:
		return c, nil
	case errors.Is(err, viewerjwt.ErrUnavailable):
		h.log.WarnContext(ctx, "viewer_keys_unavailable")
		return viewerjwt.Claims{}, errUnavailable
	default:
		return viewerjwt.Claims{}, errTokenInvalid
	}
}

// bearerToken returns the request's single "Authorization: Bearer" token.
func bearerToken(r *http.Request) (string, bool) {
	vals := r.Header.Values("Authorization")
	if len(vals) != 1 {
		return "", false
	}
	scheme, token, found := strings.Cut(strings.TrimSpace(vals[0]), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsAny(token, " \t") {
		return "", false
	}
	return token, true
}

// Viewer CORS (M3 design §3.2): any origin, because the bearer token is the
// credential and no cookies are involved. Credentials are never allowed.
const (
	viewerAllowMethods  = "GET, OPTIONS"
	viewerAllowHeaders  = "Authorization"
	viewerExposeHeaders = "Retry-After, Request-Id"
	viewerMaxAge        = "600"
)

// viewerCORS sets Access-Control-Allow-Origin: * on every response,
// problems included. It never sets Access-Control-Allow-Credentials.
func viewerCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Expose-Headers", viewerExposeHeaders)
		next.ServeHTTP(w, r)
	})
}

func viewerPreflight(w http.ResponseWriter, _ *http.Request) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", viewerAllowMethods)
	h.Set("Access-Control-Allow-Headers", viewerAllowHeaders)
	h.Set("Access-Control-Max-Age", viewerMaxAge)
	w.WriteHeader(http.StatusNoContent)
}

// Problems.
func unauthorized(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusUnauthorized, Code: "viewer_token_invalid"})
}

func unavailable(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", "5")
	httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusServiceUnavailable, Code: "tracking_unavailable"})
}

func tooManyStreams(w http.ResponseWriter, r *http.Request, status int) {
	w.Header().Set("Retry-After", "5")
	httpx.WriteProblem(w, r, httpx.Problem{Status: status, Code: "too_many_streams"})
}
