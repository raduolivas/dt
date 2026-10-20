package httpx

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"net/http"
	"regexp"
	"strings"
)

// RequestIDHeader is set on every response (spec §5.1).
const RequestIDHeader = "Request-Id"

type requestIDKey struct{}

// safeRequestID limits client-supplied IDs, so they cannot inject log lines.
var safeRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

func newRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "req_" + strings.ToLower(idEncoding.EncodeToString(b))
}

// RequestID keeps a safe client Request-Id or generates one, stores it in
// the request context and echoes it on the response.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if !safeRequestID.MatchString(id) {
			id = newRequestID()
		}
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

// RequestIDFrom returns the request ID stored by RequestID, or "".
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}
