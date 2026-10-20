package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/raduolivas/dt/backend/pkg/apikey"
	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

type principalKey struct{}

// PrincipalFrom returns the principal Authenticate stored in ctx.
func PrincipalFrom(ctx context.Context) (domain.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(domain.Principal)
	return p, ok
}

// unauthenticated answers 401 with the one indistinguishable problem.
func unauthenticated(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusUnauthorized, Code: "unauthenticated"})
}

// bearerToken extracts the token of a single "Bearer <token>" Authorization
// header. The scheme is case-insensitive and surrounding whitespace is ignored
// (RFC 9110 §11.1); anything else (no header, two headers, another scheme, a
// token with inner whitespace) yields ok=false.
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

// Authenticate resolves the request's secret key to a principal, in order:
//
//  1. A bearer token that looks like a secret key, sent with an Origin header,
//     is 403 secret_key_in_browser, before any lookup: the key leaked to a
//     browser and must not be used from one.
//  2. A key in the query string (api_key), a missing header, another scheme
//     (e.g. Basic) or a bad key is 401 unauthenticated with
//     WWW-Authenticate: Bearer.
//  3. A valid key used from outside its IP allowlist is 403 ip_not_allowed.
//  4. Any other failure (the key store is down) fails closed with 500
//     internal_error, logged without the credential.
//
// It needs ClientIP to have run. Neither the Authorization header nor the key
// is ever logged.
func Authenticate(auth Authenticator, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := bearerToken(r)
			if ok && apikey.LooksLikeSecretKey(token) && r.Header.Get("Origin") != "" {
				httpx.WriteProblem(w, r, httpx.Problem{
					Status: http.StatusForbidden, Code: "secret_key_in_browser",
					Detail: "Secret keys must never be used from a browser. Roll this key.",
				})
				return
			}
			// A key in the URL may already sit in proxy and browser logs; refuse
			// it even next to a valid header so the habit never works.
			if !ok || r.URL.Query().Has("api_key") {
				unauthenticated(w, r)
				return
			}
			ip, _ := ClientIPFrom(r.Context())
			p, err := auth.Authenticate(r.Context(), token, ip)
			switch {
			case err == nil:
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
			case errors.Is(err, app.ErrUnauthenticated):
				unauthenticated(w, r)
			case errors.Is(err, app.ErrIPNotAllowed):
				httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusForbidden, Code: "ip_not_allowed"})
			default:
				log.ErrorContext(r.Context(), "authenticate_failed",
					"error", err.Error(),
					"request_id", httpx.RequestIDFrom(r.Context()))
				internalError(w, r)
			}
		})
	}
}
