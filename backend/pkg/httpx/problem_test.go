package httpx_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/raduolivas/dt/backend/pkg/httpx"
)

func TestWriteProblem(t *testing.T) {
	h := httpx.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusConflict, Code: "invalid_transition"})
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type = %q", ct)
	}
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Type != "about:blank" || p.Title != "Conflict" || p.Code != "invalid_transition" {
		t.Fatalf("problem = %+v", p)
	}
	if p.RequestID == "" || p.RequestID != rec.Header().Get(httpx.RequestIDHeader) {
		t.Fatalf("request_id %q != header %q", p.RequestID, rec.Header().Get(httpx.RequestIDHeader))
	}
}
