package httpapi

import "net/http"

// Viewer CORS (M3 design §3.2): any origin may call the viewer endpoints,
// because the bearer token in the request is the credential and no cookies
// are involved. Credentials are never allowed.
const (
	viewerAllowMethods  = "POST, GET, OPTIONS"
	viewerAllowHeaders  = "Authorization, Content-Type"
	viewerExposeHeaders = "Retry-After, Request-Id"
	viewerMaxAge        = "600"
)

// ViewerCORS sets Access-Control-Allow-Origin: * (and the exposed headers)
// on every response of next, problems included, so the browser can read
// them. It never sets Access-Control-Allow-Credentials.
func ViewerCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Expose-Headers", viewerExposeHeaders)
		next.ServeHTTP(w, r)
	})
}

// viewerPreflight answers a CORS preflight with 204.
func viewerPreflight(w http.ResponseWriter, _ *http.Request) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", viewerAllowMethods)
	h.Set("Access-Control-Allow-Headers", viewerAllowHeaders)
	h.Set("Access-Control-Max-Age", viewerMaxAge)
	w.WriteHeader(http.StatusNoContent)
}
