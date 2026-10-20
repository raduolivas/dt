package bootstrap_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/raduolivas/dt/backend/pkg/bootstrap"
	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/pkg/testkit"
)

// start runs the service until the test ends and returns its base URL and
// a channel with Run's result.
func start(t *testing.T) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	t.Setenv("PORT", "0")
	t.Setenv("LOG_LEVEL", "error")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	addr := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- bootstrap.Run(ctx, bootstrap.Options{Name: "test", Ready: func(a string) { addr <- a }})
	}()
	select {
	case a := <-addr:
		return "http://" + a, cancel, done
	case err := <-done:
		t.Fatalf("Run returned early: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("service did not start")
	}
	return "", cancel, done
}

type response struct {
	status int
	header http.Header
	body   []byte
}

func httpGet(t *testing.T, url string) response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: body}
}

func TestRunRequiresDatabaseURL(t *testing.T) {
	t.Setenv("PORT", "0")
	t.Setenv("DATABASE_URL", "")
	err := bootstrap.Run(context.Background(), bootstrap.Options{Name: "test"})
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("want DATABASE_URL error, got %v", err)
	}
}

func TestRunServesLivezWithRequestID(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@127.0.0.1:1/dt?connect_timeout=1")
	base, cancel, done := start(t)

	live := httpGet(t, base+"/livez")
	if live.status != http.StatusOK || live.header.Get(httpx.RequestIDHeader) == "" {
		t.Fatalf("livez: %d, Request-Id %q", live.status, live.header.Get(httpx.RequestIDHeader))
	}

	ready := httpGet(t, base+"/readyz")
	var p httpx.Problem
	if err := json.Unmarshal(ready.body, &p); err != nil {
		t.Fatal(err)
	}
	if ready.status != http.StatusServiceUnavailable || p.Code != "not_ready" {
		t.Fatalf("readyz: %d %+v", ready.status, p)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(11 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestReadyzHealthyWithDatabase(t *testing.T) {
	t.Setenv("DATABASE_URL", testkit.StartPostgres(t).URL("gateway"))
	base, _, _ := start(t)
	if resp := httpGet(t, base+"/readyz"); resp.status != http.StatusOK {
		t.Fatalf("readyz: %d %s", resp.status, resp.body)
	}
}
