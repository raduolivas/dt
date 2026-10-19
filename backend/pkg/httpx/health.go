package httpx

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Check reports whether one dependency is ready.
type Check func(ctx context.Context) error

func writeOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}` + "\n"))
}

// Livez answers 200 while the process is running.
func Livez() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { writeOK(w) })
}

// Readyz runs every check concurrently within timeout. It answers 503 with
// the names of failing checks, never their errors, which may hold secrets.
// A check that ignores its context still cannot hold the probe past timeout.
func Readyz(checks map[string]Check, timeout time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		type result struct {
			name string
			err  error
		}
		results := make(chan result, len(checks))
		for name, check := range checks {
			go func() { results <- result{name, check(ctx)} }()
		}

		failing := []string{}
		pending := make(map[string]struct{}, len(checks))
		for name := range checks {
			pending[name] = struct{}{}
		}
	collect:
		for len(pending) > 0 {
			select {
			case res := <-results:
				delete(pending, res.name)
				if res.err != nil {
					failing = append(failing, res.name)
				}
			case <-ctx.Done():
				break collect
			}
		}
		for name := range pending {
			failing = append(failing, name)
		}

		if len(failing) == 0 {
			writeOK(w)
			return
		}
		sort.Strings(failing)
		WriteProblem(w, r, Problem{
			Status: http.StatusServiceUnavailable,
			Code:   "not_ready",
			Detail: "failing: " + strings.Join(failing, ", "),
		})
	})
}
