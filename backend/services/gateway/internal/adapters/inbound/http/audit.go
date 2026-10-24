package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
)

// maxAuditText caps the user agent and target stored per entry, in bytes.
const maxAuditText = 512

// auditTimeout bounds one audit write. It runs detached from the request
// context, so a client hanging up does not drop the entry.
const auditTimeout = 5 * time.Second

// Audit is the Middleware.Audit hook (non-GET routes). Mount places it right
// inside RateLimit, so it wraps Authorize, Idempotency and the handler. After
// them it records one app.AuditEntry for every request with a
// principal whose method is not GET or HEAD: the credential as actor, the
// route name as action, the route's path value as target, and the status the
// client received. That includes 403s, idempotency conflicts and replays. A
// panic is recorded as the 500 the outer Recover answers, then re-raised.
//
// A 429 is not recorded, so a key hammering the API cannot multiply audit
// writes; the rate limiter already accounts for those requests. Mount puts
// RateLimit outside Audit, so its 429s never get here; the status check is a
// guard for any other 429 below.
//
// Trade-off: the entry is written after the handler has run, so a failed
// write cannot undo the request. It is logged at error (without the client
// IP or any credential) and the response is left unchanged.
func Audit(store app.AuditStore, clock app.Clock, log *slog.Logger) Hook {
	if log == nil {
		log = slog.Default()
	}
	return func(next http.Handler, rt Route) http.Handler {
		wildcard := patternWildcard(rt.Pattern)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := PrincipalFrom(r.Context())
			if !ok || r.Method == http.MethodGet || r.Method == http.MethodHead {
				next.ServeHTTP(w, r)
				return
			}
			start := clock.Now()
			holder := &auditTarget{}
			r = r.WithContext(context.WithValue(r.Context(), auditTargetKey{}, holder))
			sw := &statusWriter{ResponseWriter: w}
			record := func(status int) {
				if status == http.StatusTooManyRequests {
					return
				}
				ip, _ := ClientIPFrom(r.Context())
				e := app.AuditEntry{
					TenantID: p.TenantID, Env: p.Env, ActorType: string(p.Credential), ActorID: p.CredentialID,
					Action: rt.Name, Status: status, IP: ip,
					UserAgent: auditText(r.UserAgent()), RequestID: httpx.RequestIDFrom(r.Context()), At: start,
				}
				switch {
				case holder.set():
					e.Target = auditText(holder.get())
				case wildcard != "":
					e.Target = auditText(r.PathValue(wildcard))
				}
				ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), auditTimeout)
				defer cancel()
				if err := store.Record(ctx, e); err != nil {
					log.ErrorContext(r.Context(), "audit_write_failed",
						"error", err.Error(),
						"action", rt.Name,
						"status", status,
						"tenant_id", p.TenantID,
						"credential_id", p.CredentialID,
						"request_id", e.RequestID)
				}
			}
			finished := false
			defer func() {
				if finished {
					return
				}
				v := recover()
				status := sw.status
				if !sw.wrote {
					status = http.StatusInternalServerError
				}
				record(status)
				if v != nil { // nil: runtime.Goexit, which keeps unwinding
					panic(v)
				}
			}()
			next.ServeHTTP(sw, r)
			finished = true
			if !sw.wrote {
				sw.status = http.StatusOK
			}
			record(sw.status)
		})
	}
}

type auditTargetKey struct{}

// auditTarget is the target a handler chose for the audit entry.
type auditTarget struct {
	mu     sync.Mutex
	target string
	ok     bool
}

func (a *auditTarget) put(t string) {
	a.mu.Lock()
	a.target, a.ok = t, true
	a.mu.Unlock()
}

func (a *auditTarget) set() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ok
}

func (a *auditTarget) get() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.target
}

// resourceKey holds the *auditTarget the Idempotency hook reads to name the
// resource a stored response belongs to.
type resourceKey struct{}

// SetAuditTarget makes the audit entry of r name target instead of the
// route's path value, and names target as the resource of r's idempotency
// record (so erasing it purges the record). Create routes use it to record
// the id they just made. It does nothing when r is neither audited nor
// idempotent (a GET, or no hook mounted).
func SetAuditTarget(r *http.Request, target string) {
	if h, ok := r.Context().Value(auditTargetKey{}).(*auditTarget); ok {
		h.put(target)
	}
	if h, ok := r.Context().Value(resourceKey{}).(*auditTarget); ok {
		h.put(target)
	}
}

// patternWildcard returns the name of the first {wildcard} in a mux pattern,
// or "" if it has none.
func patternWildcard(pattern string) string {
	_, rest, ok := strings.Cut(pattern, "{")
	if !ok {
		return ""
	}
	name, _, ok := strings.Cut(rest, "}")
	if !ok {
		return ""
	}
	return strings.TrimSuffix(name, "...")
}

// auditText makes s valid UTF-8 (PostgreSQL text rejects anything else) and
// cuts it to at most maxAuditText bytes on a rune boundary.
func auditText(s string) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= maxAuditText {
		return s
	}
	cut := maxAuditText
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// statusWriter remembers the status the handler wrote.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusWriter) WriteHeader(code int) {
	if !s.wrote && code >= 200 {
		s.status, s.wrote = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if !s.wrote {
		s.status, s.wrote = http.StatusOK, true
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }
