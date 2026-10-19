// Package httpx holds the HTTP building blocks every service shares:
// RFC 9457 problem responses, request IDs, recovery, access logs,
// health endpoints and a server with graceful shutdown.
package httpx

import (
	"encoding/json"
	"net/http"
)

// FieldError describes one invalid input field.
type FieldError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Problem is an RFC 9457 problem+json body (spec §5.1).
type Problem struct {
	Type      string       `json:"type"`
	Title     string       `json:"title"`
	Status    int          `json:"status"`
	Code      string       `json:"code"`
	Detail    string       `json:"detail,omitempty"`
	RequestID string       `json:"request_id"`
	Errors    []FieldError `json:"errors,omitempty"`
}

// WriteProblem writes p as application/problem+json, filling defaults and
// the request ID from r's context.
func WriteProblem(w http.ResponseWriter, r *http.Request, p Problem) {
	if p.Type == "" {
		p.Type = "about:blank"
	}
	if p.Title == "" {
		p.Title = http.StatusText(p.Status)
	}
	p.RequestID = RequestIDFrom(r.Context())
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}
