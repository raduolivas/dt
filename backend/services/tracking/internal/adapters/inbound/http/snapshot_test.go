package httpapi_test

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

type response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

func (e *env) get(tok string) response {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/v1/tracking/snapshot", nil)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	return response{res.StatusCode, res.Header, b}
}

func TestSnapshotEndpoint(t *testing.T) {
	e := newEnv(t)
	tok := e.token(claimsA)
	res := e.get(tok)
	var s domain.TrackingSnapshot
	if res.StatusCode != 200 || json.Unmarshal(res.Body, &s) != nil || s.Version != 10 {
		t.Fatalf("GET = %d %+v", res.StatusCode, s)
	}
	if res.Header.Get("Cache-Control") != "no-store" || res.Header.Get("Access-Control-Allow-Origin") != "*" ||
		res.Header.Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("headers = %v", res.Header)
	}

	for name, h := range map[string]string{"none": "", "garbage": "x.y.z"} {
		if res := e.get(h); res.StatusCode != 401 || res.Header.Get("WWW-Authenticate") != "Bearer" ||
			res.Header.Get("Content-Type") != "application/problem+json" {
			t.Fatalf("%s: %d %v", name, res.StatusCode, res.Header)
		}
	}

	// Preflight.
	req, _ := http.NewRequest(http.MethodOptions, e.srv.URL+"/v1/tracking/snapshot", nil)
	req.Header.Set("Origin", "https://partner.example")
	req.Header.Set("Access-Control-Request-Method", "GET")
	pre, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = pre.Body.Close()
	if pre.StatusCode != 204 || pre.Header.Get("Access-Control-Allow-Origin") != "*" ||
		pre.Header.Get("Access-Control-Allow-Methods") != "GET, OPTIONS" ||
		pre.Header.Get("Access-Control-Allow-Headers") != "Authorization" ||
		pre.Header.Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("preflight = %d %v", pre.StatusCode, pre.Header)
	}

	// 30 a minute per token, then 429 with Retry-After.
	for range 29 {
		if res := e.get(tok); res.StatusCode != 200 {
			t.Fatalf("within the limit: %d", res.StatusCode)
		}
	}
	if res := e.get(tok); res.StatusCode != 429 || res.Header.Get("Retry-After") == "" {
		t.Fatalf("over the limit: %d %v", res.StatusCode, res.Header)
	}

	// Revoked and expired links: 401.
	tok2 := e.token(claimsA)
	e.revoke(linkA)
	if res := e.get(tok2); res.StatusCode != 401 {
		t.Fatalf("revoked: %d", res.StatusCode)
	}
	other := claimsA
	other.LinkID = linkB
	e.expire(linkB, time.Now().Add(-time.Second))
	if res := e.get(e.token(other)); res.StatusCode != 401 {
		t.Fatalf("expired: %d", res.StatusCode)
	}
}
