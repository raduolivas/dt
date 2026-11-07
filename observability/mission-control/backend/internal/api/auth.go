package api

import "net/http"

// cspValue confines the UI to its own origin: no inline script, no external
// hosts, and no framing by any site (the sim break buttons must not be
// clickjackable). Every JSON string is data and is never turned into HTML
// server-side.
const cspValue = "default-src 'self'; frame-ancestors 'none'"

// AuthSeam is the place where access control plugs in. Today it is a no-op:
// Mission Control binds 127.0.0.1 only and has no login. MC-6 (access control
// for staging) replaces the body with a real check; nothing else changes.
func AuthSeam(next http.Handler) http.Handler { return next }

// CSP sets Content-Security-Policy (default-src 'self'; frame-ancestors
// 'none') and X-Content-Type-Options: nosniff on every response.
func CSP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", cspValue)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}
