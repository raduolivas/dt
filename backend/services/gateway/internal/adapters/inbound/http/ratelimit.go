package httpapi

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
)

// unknownIPKey is the single bucket for requests whose client IP could not be
// determined, so they cannot dodge the failed-auth limit.
const unknownIPKey = "rl:authfail:unknown"

// ceilSeconds rounds d up to whole seconds, at least 1 when d is positive.
func ceilSeconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return int((d + time.Second - 1) / time.Second)
}

func rateLimited(w http.ResponseWriter, r *http.Request, d app.Decision) {
	w.Header().Set("Retry-After", strconv.Itoa(max(ceilSeconds(d.RetryAfter), 1)))
	httpx.WriteProblem(w, r, httpx.Problem{
		Status: http.StatusTooManyRequests, Code: "rate_limited",
		Detail: "Too many requests. Retry after the number of seconds in Retry-After.",
	})
}

func warnUnavailable(log *slog.Logger, r *http.Request, err error) {
	log.WarnContext(r.Context(), "rate_limiter_unavailable",
		"error", err.Error(),
		"request_id", httpx.RequestIDFrom(r.Context()))
}

// PartnerRateLimit is the Middleware.RateLimit hook: it counts each
// authenticated request against rl:sk:<credential_id> (app.PartnerKey), sets
// RateLimit-Limit/-Remaining/-Reset, and answers 429 rate_limited with
// Retry-After when the bucket is empty. If the limiter fails the request is
// let through without RateLimit-* headers and a warn is logged: the limiter
// never turns an outage into errors.
func PartnerRateLimit(l app.Limiter, log *slog.Logger) Hook {
	if log == nil {
		log = slog.Default()
	}
	return func(next http.Handler, _ Route) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := PrincipalFrom(r.Context())
			if !ok { // Authenticate always runs first; defensive
				next.ServeHTTP(w, r)
				return
			}
			d, err := l.Allow(r.Context(), "rl:sk:"+p.CredentialID, app.PartnerKey)
			if err != nil {
				warnUnavailable(log, r, err)
				next.ServeHTTP(w, r)
				return
			}
			h := w.Header()
			h.Set("RateLimit-Limit", strconv.Itoa(d.Limit))
			h.Set("RateLimit-Remaining", strconv.Itoa(d.Remaining))
			h.Set("RateLimit-Reset", strconv.Itoa(ceilSeconds(d.ResetAfter)))
			if !d.Allowed {
				rateLimited(w, r, d)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// FailedAuthLimit is the Middleware.FailedAuth hook. It wraps Authenticate and
// counts every 401 it writes against rl:authfail:<client ip> (app.FailedAuth).
// When that bucket is empty the 401 is replaced by 429 rate_limited with
// Retry-After (and no WWW-Authenticate). Requests that authenticate cost
// nothing. A request with no resolvable client IP shares the single bucket
// rl:authfail:unknown. If the limiter fails the normal 401 is answered.
func FailedAuthLimit(l app.Limiter, log *slog.Logger) Hook {
	if log == nil {
		log = slog.Default()
	}
	return func(next http.Handler, _ Route) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := unknownIPKey
			if ip, ok := ClientIPFrom(r.Context()); ok && ip.IsValid() {
				key = "rl:authfail:" + ip.String()
			}
			next.ServeHTTP(&failedAuthWriter{ResponseWriter: w, r: r, l: l, log: log, key: key}, r)
		})
	}
}

// failedAuthWriter intercepts the 401 header write to charge the bucket.
type failedAuthWriter struct {
	http.ResponseWriter
	r       *http.Request
	l       app.Limiter
	log     *slog.Logger
	key     string
	swapped bool // a 429 replaced the 401; drop the 401's body
}

func (f *failedAuthWriter) WriteHeader(status int) {
	if status == http.StatusUnauthorized && !f.swapped {
		d, err := f.l.Allow(f.r.Context(), f.key, app.FailedAuth)
		switch {
		case err != nil:
			warnUnavailable(f.log, f.r, err)
		case !d.Allowed:
			f.swapped = true
			f.Header().Del("WWW-Authenticate")
			rateLimited(f.ResponseWriter, f.r, d)
			return
		}
	}
	if !f.swapped {
		f.ResponseWriter.WriteHeader(status)
	}
}

func (f *failedAuthWriter) Write(b []byte) (int, error) {
	if f.swapped {
		return len(b), nil
	}
	return f.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (f *failedAuthWriter) Unwrap() http.ResponseWriter { return f.ResponseWriter }
