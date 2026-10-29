package httpx_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/pkg/logx"
)

func TestRecoverReturnsProblem(t *testing.T) {
	var logs bytes.Buffer
	log := logx.New(&logs, slog.LevelDebug, "test")
	h := httpx.RequestID(httpx.Recover(log)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("secret-detail")
	})))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Code != "internal_error" {
		t.Fatalf("code = %q", p.Code)
	}
	if strings.Contains(rec.Body.String(), "secret-detail") {
		t.Fatalf("panic value leaked to client: %s", rec.Body.String())
	}
	id := rec.Header().Get(httpx.RequestIDHeader)
	if !strings.Contains(logs.String(), `"request_id":"`+id+`"`) || !strings.Contains(logs.String(), `"stack":`) {
		t.Fatalf("log missing request_id or stack: %s", logs.String())
	}
}

func TestAccessLogOmitsQuery(t *testing.T) {
	var logs bytes.Buffer
	log := logx.New(&logs, slog.LevelDebug, "test")
	h := httpx.RequestID(httpx.AccessLog(log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	req := httptest.NewRequest(http.MethodGet, "/v1/x?token=abc", nil)
	// Fixed ID: a random one could itself contain "abc".
	req.Header.Set(httpx.RequestIDHeader, "fixed-id")
	h.ServeHTTP(httptest.NewRecorder(), req)

	out := logs.String()
	for _, want := range []string{`"path":"/v1/x"`, `"status":200`, `"duration_ms"`, `"request_id":"fixed-id"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s in %s", want, out)
		}
	}
	if strings.Contains(out, "abc") {
		t.Fatalf("query leaked: %s", out)
	}
}

func TestAccessLogRedactsCredentialPaths(t *testing.T) {
	var logs bytes.Buffer
	log := logx.New(&logs, slog.LevelDebug, "test")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /t/{token}", func(http.ResponseWriter, *http.Request) {})
	h := httpx.AccessLog(log)(mux)
	// Handled (200), rejected by the mux (405) and unmatched (404) alike.
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(m, "/t/secretsecret", nil))
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/t/secretsecret/more", nil))
	out := logs.String()
	if strings.Contains(out, "secretsecret") || strings.Count(out, `"path":"/t/{token}"`) != 3 {
		t.Fatalf("log = %s", out)
	}
}

func TestRedactPath(t *testing.T) {
	for in, want := range map[string]string{"/v1/x": "/v1/x", "/t/abc": "/t/{token}", "/t/": "/t/{token}", "/tx/abc": "/tx/abc", "/logos/a/b": "/logos/a/b"} {
		if got := httpx.RedactPath(in); got != want {
			t.Errorf("RedactPath(%q) = %q, want %q", in, got, want)
		}
	}
}
