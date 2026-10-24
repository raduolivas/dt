package bootstrap_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

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
	pg := testkit.StartPostgres(t)
	t.Setenv("DATABASE_URL", pg.URL("gateway"))
	t.Setenv("MIGRATIONS_DATABASE_URL", pg.MigratorURL("gateway"))
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

// Without MIGRATIONS_DATABASE_URL, migrations run as DATABASE_URL (a
// single-role setup, where that role owns the schema).
func TestRunMigratesWithRuntimeURLWhenMigrationsURLUnset(t *testing.T) {
	t.Setenv("DATABASE_URL", testkit.StartPostgres(t).MigratorURL("gateway"))
	t.Setenv("MIGRATIONS_DATABASE_URL", "")
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

func TestRunMigrationsErrorHidesURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:runtime-secret@127.0.0.1:1/dt?connect_timeout=1")
	t.Setenv("MIGRATIONS_DATABASE_URL", "postgres://mig_x:migration-secret@127.0.0.1:1/dt?connect_timeout=1")
	t.Setenv("PORT", "0")
	t.Setenv("LOG_LEVEL", "error")
	migrations := fstest.MapFS{"0001_t.up.sql": {Data: []byte("CREATE TABLE t (id int)")}}
	err := bootstrap.Run(context.Background(), bootstrap.Options{Name: "gateway", Migrations: migrations})
	if err == nil {
		t.Fatal("want error")
	}
	for _, secret := range []string{"runtime-secret", "migration-secret", "127.0.0.1"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error echoes %q: %v", secret, err)
		}
	}
}

func TestMigrationsStayInServiceSchema(t *testing.T) {
	pg := testkit.StartPostgres(t)
	t.Setenv("DATABASE_URL", pg.URL("gateway"))
	t.Setenv("MIGRATIONS_DATABASE_URL", pg.MigratorURL("gateway"))
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

// hangingRedis listens on a free port, accepts connections and never answers
// (a hung Redis: no reset, no reply). It returns a redis:// URL for it.
func hangingRedis(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = c.Close() })
			go func() { _, _ = io.Copy(io.Discard, c) }()
		}
	}()
	return "redis://" + ln.Addr().String() + "/0"
}

func TestRedisOptionsHonorContextDeadline(t *testing.T) {
	opts, err := bootstrap.RedisOptions(hangingRedis(t))
	if err != nil {
		t.Fatal(err)
	}
	if !opts.ContextTimeoutEnabled {
		t.Fatal("ContextTimeoutEnabled = false")
	}
	c := redis.NewClient(opts)
	t.Cleanup(func() { _ = c.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = c.Ping(ctx).Err()
	elapsed := time.Since(start)
	t.Logf("ping against a hung redis returned after %v", elapsed)
	if err == nil || elapsed > 200*time.Millisecond {
		t.Fatalf("ping = %v after %v, want an error within 200ms", err, elapsed)
	}
}

func TestReadyzFailsWithinBudgetWhenRedisHangs(t *testing.T) {
	t.Setenv("DATABASE_URL", testkit.StartPostgres(t).URL("gateway"))
	t.Setenv("REDIS_URL", hangingRedis(t))
	base, _, _ := startWith(t, bootstrap.Options{Name: "test", Needs: bootstrap.Needs{Redis: true}})
	start := time.Now()
	resp := httpGet(t, base+"/readyz")
	elapsed := time.Since(start)
	var p httpx.Problem
	if err := json.Unmarshal(resp.body, &p); err != nil {
		t.Fatal(err)
	}
	t.Logf("readyz with hung redis: %d after %v", resp.status, elapsed)
	if resp.status != http.StatusServiceUnavailable || p.Detail != "failing: redis" {
		t.Fatalf("readyz: %d %+v", resp.status, p)
	}
	if elapsed > 2500*time.Millisecond {
		t.Fatalf("readyz took %v, want within its 2 s budget", elapsed)
	}
}

func TestReadyzIgnoresNonFatalRedis(t *testing.T) {
	for _, tc := range []struct{ name, url string }{
		{"refused", "redis://127.0.0.1:1/0"},
		{"hung", hangingRedis(t)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", testkit.StartPostgres(t).URL("gateway"))
			t.Setenv("REDIS_URL", tc.url)
			base, _, _ := startWith(t, bootstrap.Options{Name: "test", Needs: bootstrap.Needs{Redis: true, RedisNonFatal: true}})
			start := time.Now()
			resp := httpGet(t, base+"/readyz")
			elapsed := time.Since(start)
			t.Logf("readyz with %s redis: %d after %v", tc.name, resp.status, elapsed)
			if resp.status != http.StatusOK {
				t.Fatalf("readyz: %d %s, want 200 (redis is non-fatal)", resp.status, resp.body)
			}
			if elapsed > time.Second {
				t.Fatalf("readyz took %v, want < 1s", elapsed)
			}
		})
	}
}

func TestNonFatalRedisDoesNotHideDatabase(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@127.0.0.1:1/dt?connect_timeout=1")
	t.Setenv("REDIS_URL", "redis://127.0.0.1:1/0")
	base, _, _ := startWith(t, bootstrap.Options{Name: "test", Needs: bootstrap.Needs{Redis: true, RedisNonFatal: true}})
	resp := httpGet(t, base+"/readyz")
	var p httpx.Problem
	if err := json.Unmarshal(resp.body, &p); err != nil {
		t.Fatal(err)
	}
	if resp.status != http.StatusServiceUnavailable || p.Detail != "failing: db" {
		t.Fatalf("readyz: %d %+v, want 503 failing: db only", resp.status, p)
	}
}
