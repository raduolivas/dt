package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
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

type logPathKey struct{}

// SetLogPath makes AccessLog record path instead of the request's real one.
// A handler whose URL path carries a bearer credential (a tracking link
// token) calls it with the route pattern. It is a no-op outside AccessLog.
func SetLogPath(r *http.Request, path string) {
	if p, ok := r.Context().Value(logPathKey{}).(*string); ok {
		*p = path
	}
}

// AccessLog logs one line per request. It logs the path only, never the
// query string, which may carry tokens; a handler can replace the path with
// SetLogPath.
func AccessLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			path := r.URL.Path
			next.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), logPathKey{}, &path)))
			if rec.status == 0 {
				rec.status = http.StatusOK
			}
			log.InfoContext(r.Context(), "http_request",
				"method", r.Method,
				"path", path,
				"status", rec.status,
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", RequestIDFrom(r.Context()))
		})
	}
}
