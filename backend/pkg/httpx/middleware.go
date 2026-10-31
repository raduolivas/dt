package httpx

import (
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

// Recover turns a panic into a 500 problem without exposing the panic value,
// and logs it with the stack.
func Recover(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
						panic(v)
					}
					log.ErrorContext(r.Context(), "panic",
						"request_id", RequestIDFrom(r.Context()),
						"panic", v,
						"stack", string(debug.Stack()))
					WriteProblem(w, r, Problem{Status: http.StatusInternalServerError, Code: "internal_error"})
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// CredentialPathPrefixes are URL path prefixes whose next segment is a bearer
// credential (a tracking link token). Neither the access log nor trace spans
// may carry it: RedactPath replaces the segment with a placeholder. The
// gateway's hosted page is the one such route.
var CredentialPathPrefixes = []string{"/t/"}

// RedactPath returns path with the credential segment after any of
// CredentialPathPrefixes replaced by "{token}" (the rest of the path is
// dropped, as the credential is the last segment).
func RedactPath(path string) string {
	for _, p := range CredentialPathPrefixes {
		if strings.HasPrefix(path, p) {
			return p + "{token}"
		}
	}
	return path
}

// AccessLog logs one line per request. It logs the path only, never the
// query string, which may carry tokens, and passes the path through
// RedactPath, whichever handler answered (200, 405, 429, ...).
func AccessLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			if rec.status == 0 {
				rec.status = http.StatusOK
			}
			log.InfoContext(r.Context(), "http_request",
				"method", r.Method,
				"path", RedactPath(r.URL.Path),
				"status", rec.status,
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", RequestIDFrom(r.Context()))
		})
	}
}
