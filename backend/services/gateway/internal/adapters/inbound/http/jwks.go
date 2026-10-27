package httpapi

import "net/http"

// JWKSPath is where the gateway publishes the viewer token keys.
const JWKSPath = "/.well-known/jwks.json"

// jwksHandler serves the public viewer-token keys, cacheable for 5 minutes
// (the verifier's cache time).
func jwksHandler(jwks []byte) http.Handler {
	body := append([]byte(nil), jwks...)
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "application/json")
		h.Set("Cache-Control", "public, max-age=300")
		h.Set("Access-Control-Allow-Origin", "*")
		_, _ = w.Write(body)
	})
}
