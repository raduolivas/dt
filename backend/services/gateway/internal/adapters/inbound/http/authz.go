package httpapi

import (
	"net/http"
	"slices"

	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

// Authorize enforces rt's credential types, scope and environment rule on the
// principal Authenticate stored:
//   - a credential type not in rt.Credentials (or no principal) is 401
//     unauthenticated: that credential cannot call the route at all;
//   - a missing rt.Scope is 403 insufficient_scope;
//   - a live credential on a TestOnly route is 403 environment_not_allowed.
func Authorize(rt Route) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := PrincipalFrom(r.Context())
			if !ok || !slices.Contains(rt.Credentials, p.Credential) {
				unauthenticated(w, r)
				return
			}
			if rt.Scope != "" && !p.Has(rt.Scope) {
				httpx.WriteProblem(w, r, httpx.Problem{
					Status: http.StatusForbidden, Code: "insufficient_scope",
					Detail: "This key lacks the " + string(rt.Scope) + " scope.",
				})
				return
			}
			if rt.Env == TestOnly && p.Env != domain.EnvTest {
				httpx.WriteProblem(w, r, httpx.Problem{
					Status: http.StatusForbidden, Code: "environment_not_allowed",
					Detail: "This endpoint is available with test keys only.",
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
