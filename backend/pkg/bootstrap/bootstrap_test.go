package bootstrap_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/raduolivas/dt/backend/pkg/bootstrap"
	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/pkg/testkit"
)

// start runs the service until the test ends and returns its base URL and
// a channel with Run's result.
func start(t *testing.T) (string, context.CancelFunc, <-chan error) {
	return startWith(t, bootstrap.Options{Name: "test"})
}

func startWith(t *testing.T, opts bootstrap.Options) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	t.Setenv("PORT", "0")
	t.Setenv("LOG_LEVEL", "error")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	addr := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		opts.Ready = func(a string) { addr <- a }
		done <- bootstrap.Run(ctx, opts)
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

var errBoom = errors.New("boom")

func TestRunAppliesMigrationsBeforeSetup(t *testing.T) {
	t.Setenv("DATABASE_URL", testkit.StartPostgres(t).URL("gateway"))
	migrations := fstest.MapFS{"0001_t.up.sql": {Data: []byte("CREATE TABLE t (id int)")}}
	var queryErr error
	startWith(t, bootstrap.Options{
		Name:       "gateway",
		Migrations: migrations,
		Setup: func(ctx context.Context, d bootstrap.Deps, _ *http.ServeMux) ([]bootstrap.Worker, error) {
			var n int
			queryErr = d.DB.QueryRow(ctx, "SELECT count(*) FROM t").Scan(&n)
			return nil, queryErr
		},
	})
	if queryErr != nil {
		t.Fatalf("table t missing in Setup: %v", queryErr)
	}
}

func TestMigrationsStayInServiceSchema(t *testing.T) {
	pg := testkit.StartPostgres(t)
	t.Setenv("DATABASE_URL", pg.URL("gateway"))
	migrations := fstest.MapFS{"0001_t.up.sql": {Data: []byte("CREATE TABLE t (id int)")}}
	startWith(t, bootstrap.Options{Name: "gateway", Migrations: migrations})

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, pg.URL("admin"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var inSvc, inPublic *string
	err = conn.QueryRow(ctx, "SELECT to_regclass('gateway.schema_migrations')::text, to_regclass('public.schema_migrations')::text").Scan(&inSvc, &inPublic)
	if err != nil {
		t.Fatal(err)
	}
	if inSvc == nil || inPublic != nil {
		t.Fatalf("schema_migrations: gateway=%v public=%v", inSvc, inPublic)
	}
}

func TestRunFailsWhenNATSNeededButUnset(t *testing.T) {
	t.Setenv("PORT", "0")
	t.Setenv("DATABASE_URL", "postgres://u:p@127.0.0.1:1/dt")
	t.Setenv("NATS_URL", "")
	err := bootstrap.Run(context.Background(), bootstrap.Options{Name: "test", Needs: bootstrap.Needs{NATS: true}})
	if err == nil || !strings.Contains(err.Error(), "NATS_URL") {
		t.Fatalf("want NATS_URL error, got %v", err)
	}
}

func TestWorkerErrorStopsRun(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@127.0.0.1:1/dt?connect_timeout=1")
	_, _, done := startWith(t, bootstrap.Options{
		Name: "test",
		Setup: func(context.Context, bootstrap.Deps, *http.ServeMux) ([]bootstrap.Worker, error) {
			return []bootstrap.Worker{func(context.Context) error { return errBoom }}, nil
		},
	})
	select {
	case err := <-done:
		if !errors.Is(err, errBoom) {
			t.Fatalf("want errBoom, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after worker error")
	}
}

func TestReadyzIncludesRedisAndNATS(t *testing.T) {
	t.Setenv("DATABASE_URL", testkit.StartPostgres(t).URL("gateway"))
	t.Setenv("REDIS_URL", testkit.StartRedis(t))
	t.Setenv("NATS_URL", testkit.StartNATS(t))
	base, _, _ := startWith(t, bootstrap.Options{Name: "test", Needs: bootstrap.Needs{Redis: true, NATS: true}})
	if resp := httpGet(t, base+"/readyz"); resp.status != http.StatusOK {
		t.Fatalf("readyz: %d %s", resp.status, resp.body)
	}
}

func TestReadyzFailsWhenRedisUnreachable(t *testing.T) {
	t.Setenv("DATABASE_URL", testkit.StartPostgres(t).URL("gateway"))
	t.Setenv("REDIS_URL", "redis://127.0.0.1:1/0")
	base, _, _ := startWith(t, bootstrap.Options{Name: "test", Needs: bootstrap.Needs{Redis: true}})
	resp := httpGet(t, base+"/readyz")
	var p httpx.Problem
	if err := json.Unmarshal(resp.body, &p); err != nil {
		t.Fatal(err)
	}
	if resp.status != http.StatusServiceUnavailable || !strings.Contains(p.Detail, "failing: redis") {
		t.Fatalf("readyz: %d %+v", resp.status, p)
	}
}
