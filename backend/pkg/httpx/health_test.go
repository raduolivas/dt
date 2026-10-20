package httpx_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/raduolivas/dt/backend/pkg/httpx"
)

func get(h http.Handler) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	return rec
}

func TestLivez(t *testing.T) {
	rec := get(httpx.Livez())
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"status":"ok"}` {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestReadyzAllHealthy(t *testing.T) {
	ok := func(context.Context) error { return nil }
	rec := get(httpx.Readyz(map[string]httpx.Check{"db": ok, "nats": ok}, time.Second))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"status":"ok"}` {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestReadyzFailingCheck(t *testing.T) {
	fail := func(context.Context) error { return errors.New("password authentication failed") }
	rec := get(httpx.Readyz(map[string]httpx.Check{"db": fail}, time.Second))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Code != "not_ready" || !strings.Contains(p.Detail, "db") {
		t.Fatalf("problem = %+v", p)
	}
	if strings.Contains(rec.Body.String(), "password authentication failed") {
		t.Fatalf("check error leaked: %s", rec.Body.String())
	}
}

func TestReadyzTimesOut(t *testing.T) {
	block := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	start := time.Now()
	rec := get(httpx.Readyz(map[string]httpx.Check{"db": block}, 100*time.Millisecond))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
	if d := time.Since(start); d >= time.Second {
		t.Fatalf("readyz took %v", d)
	}
}
