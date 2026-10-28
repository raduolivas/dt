package httpapi

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/pkg/ids"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
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
// Retry-After when the bucket is empty. A route with RateBucket "relay" is
// charged to the relay's own buckets instead (see relayCharges), so a
// fleet's GPS never consumes the key's bucket. If the limiter fails the
// request is let through without RateLimit-* headers and a warn is logged:
// the limiter never turns an outage into errors.
func PartnerRateLimit(l app.Limiter, log *slog.Logger) Hook {
	if log == nil {
		log = slog.Default()
	}
	return func(next http.Handler, rt Route) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := PrincipalFrom(r.Context())
			if !ok { // Authenticate always runs first; defensive
				next.ServeHTTP(w, r)
				return
			}
			charges := []charge{{"rl:sk:" + p.CredentialID, app.PartnerKey}}
			if rt.RateBucket == RateBucketRelay {
				charges = relayCharges(r, p)
			}
			// The RateLimit-* headers describe the first bucket (the key's,
			// or the courier's on the relay) unless another one refuses.
			var first app.Decision
			for i, c := range charges {
				d, err := l.Allow(r.Context(), c.key, c.limit)
				if err != nil {
					warnUnavailable(log, r, err)
					next.ServeHTTP(w, r)
					return
				}
				if !d.Allowed {
					setRateLimitHeaders(w, d)
					rateLimited(w, r, d)
					return
				}
				if i == 0 {
					first = d
				}
			}
			setRateLimitHeaders(w, first)
			next.ServeHTTP(w, r)
		})
	}
}

func setRateLimitHeaders(w http.ResponseWriter, d app.Decision) {
	h := w.Header()
	h.Set("RateLimit-Limit", strconv.Itoa(d.Limit))
	h.Set("RateLimit-Remaining", strconv.Itoa(d.Remaining))
	h.Set("RateLimit-Reset", strconv.Itoa(ceilSeconds(d.ResetAfter)))
}

type charge struct {
	key   string
	limit app.Limit
}

// relayCharges are the buckets one relay call is charged to, in order: the
// courier's (rl:relay:cou:<tenant>:<env>:<cou id>, 1/s burst 5), then the
// tenant's environment ceiling (rl:relay:ten:<tenant>:<env>, 500/s burst
// 1000). A hot courier is refused before it costs the tenant ceiling. The
// courier bucket names the caller's tenant and environment because it is
// charged before tracking checks that the courier is the caller's: another
// tenant posting to this courier id gets its 404 from its own bucket and
// never drains the owner's. The path id is
// not validated yet, so only a well-formed courier id names a bucket; any
// other (a 400 for the validator) is charged to the tenant ceiling alone,
// and cannot mint bucket keys.
func relayCharges(r *http.Request, p domain.Principal) []charge {
	var cs []charge
	if id := r.PathValue("id"); ids.Valid(ids.Courier, id) {
		cs = append(cs, charge{"rl:relay:cou:" + p.TenantID + ":" + string(p.Env) + ":" + id, app.RelayCourier})
	}
	return append(cs, charge{"rl:relay:ten:" + p.TenantID + ":" + string(p.Env), app.RelayTenant})
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
