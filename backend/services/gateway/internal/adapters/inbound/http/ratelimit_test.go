package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-redis/redis_rate/v10"
	"github.com/redis/go-redis/v9"

	"github.com/raduolivas/dt/backend/pkg/apikey"
	"github.com/raduolivas/dt/backend/pkg/testkit"
	httpapi "github.com/raduolivas/dt/backend/services/gateway/internal/adapters/inbound/http"
	redislimit "github.com/raduolivas/dt/backend/services/gateway/internal/adapters/outbound/redis"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

func redisClient(t *testing.T) *redis.Client {
	t.Helper()
	opts, err := redis.ParseURL(testkit.StartRedis(t))
	if err != nil {
		t.Fatal(err)
	}
	c := redis.NewClient(opts)
	t.Cleanup(func() { _ = c.Close() })
	// Warm the client without the limiter's 50 ms budget: the first call
	// dials and loads the Lua script, which under parallel test load takes
	// longer than 50 ms and would make the limiter fail open.
	if _, err := redis_rate.NewLimiter(c).Allow(context.Background(), "warmup", redis_rate.PerHour(1)); err != nil {
		t.Fatal(err)
	}
	return c
}

// limited mounts the production routes with both limiter hooks on lim.
func limited(t *testing.T, lim app.Limiter, keys ...testKey) *edge {
	t.Helper()
	return newEdge(t, keys, withMiddleware(func(m *httpapi.Middleware) {
		m.RateLimit = httpapi.PartnerRateLimit(lim, m.Log)
		m.FailedAuth = httpapi.FailedAuthLimit(lim, m.Log)
	}))
}

// slowRefill keeps each limit's burst but refills one token per hour, so a
// slow CI machine cannot win tokens back (real Redis time cannot be faked)
// while a test fires its burst. The sustained rates are constants checked by
// the app and adapter tests.
type slowRefill struct{ app.Limiter }

func (s slowRefill) Allow(ctx context.Context, key string, l app.Limit) (app.Decision, error) {
	l.Rate, l.Period = 1, time.Hour
	return s.Limiter.Allow(ctx, key, l)
}

func fromIP(r *http.Request, ip string) *http.Request {
	r.RemoteAddr = ip + ":4000"
	return r
}

func TestPartnerKeyBurstThen429(t *testing.T) {
	t.Parallel()
	k := mint(t, apikey.Test, "ak_burst", domain.ScopeDeliveriesRead)
	e := limited(t, slowRefill{redislimit.NewLimiter(redisClient(t))}, k)

	first, last := -1, -1
	for i := range app.PartnerKey.Burst {
		rec := e.do(get("/v1/me", "Bearer "+k.raw))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d: %s", i+1, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("RateLimit-Limit") != "100" {
			t.Fatalf("RateLimit-Limit = %q", rec.Header().Get("RateLimit-Limit"))
		}
		rem, err := strconv.Atoi(rec.Header().Get("RateLimit-Remaining"))
		if err != nil {
			t.Fatalf("RateLimit-Remaining: %v", err)
		}
		if first < 0 {
			first = rem
		}
		last = rem
		if rec.Header().Get("RateLimit-Reset") == "" {
			t.Fatal("RateLimit-Reset missing")
		}
	}
	if first >= 100 || last >= first {
		t.Fatalf("RateLimit-Remaining did not decrease: first %d, last %d", first, last)
	}

	rec := e.do(get("/v1/me", "Bearer "+k.raw)) // the 101st
	wantProblem(t, rec, http.StatusTooManyRequests, "rate_limited")
	if ra, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || ra < 1 {
		t.Fatalf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	if rec.Header().Get("RateLimit-Limit") != "100" || rec.Header().Get("RateLimit-Remaining") != "0" {
		t.Fatalf("429 headers: %v", rec.Header())
	}
}

func TestLimitsArePerKey(t *testing.T) {
	t.Parallel()
	a := mint(t, apikey.Test, "ak_a", domain.ScopeDeliveriesRead)
	b := mint(t, apikey.Test, "ak_b", domain.ScopeDeliveriesRead)
	e := limited(t, slowRefill{redislimit.NewLimiter(redisClient(t))}, a, b)

	denied := false
	for range app.PartnerKey.Burst + 10 {
		if e.do(get("/v1/me", "Bearer "+a.raw)).Code == http.StatusTooManyRequests {
			denied = true
			break
		}
	}
	if !denied {
		t.Fatal("key A was never limited")
	}
	if rec := e.do(get("/v1/me", "Bearer "+b.raw)); rec.Code != http.StatusOK {
		t.Fatalf("key B status = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestFailedAuthPerIPLimit(t *testing.T) {
	t.Parallel()
	e := limited(t, slowRefill{redislimit.NewLimiter(redisClient(t))})
	bad := func(ip string) *httptest.ResponseRecorder {
		return e.do(fromIP(get("/v1/me", "Bearer "+mint(t, apikey.Test, "ak_x").raw), ip))
	}

	for i := range app.FailedAuth.Burst {
		wantProblem(t, bad("203.0.113.10"), http.StatusUnauthorized, "unauthenticated")
		_ = i
	}
	rec := bad("203.0.113.10")
	wantProblem(t, rec, http.StatusTooManyRequests, "rate_limited")
	if ra, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || ra < 1 {
		t.Fatalf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	if rec.Header().Get("WWW-Authenticate") != "" {
		t.Fatal("429 carries WWW-Authenticate")
	}
	// Another IP is untouched.
	wantProblem(t, bad("203.0.113.11"), http.StatusUnauthorized, "unauthenticated")
}

func TestFailedAuthDoesNotCountSuccesses(t *testing.T) {
	t.Parallel()
	k := mint(t, apikey.Test, "ak_ok", domain.ScopeDeliveriesRead)
	e := limited(t, slowRefill{redislimit.NewLimiter(redisClient(t))}, k)
	for range app.FailedAuth.Burst + 5 {
		if rec := e.do(fromIP(get("/v1/me", "Bearer "+k.raw), "203.0.113.20")); rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
	}
	wantProblem(t, e.do(fromIP(get("/v1/me", ""), "203.0.113.20")), http.StatusUnauthorized, "unauthenticated")
}

func TestRateLimitFailsOpenWhenRedisDown(t *testing.T) {
	t.Parallel()
	// Nothing listens on port 1: every call fails fast or times out.
	c := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:1", DialTimeout: 20 * time.Millisecond,
		ReadTimeout: 20 * time.Millisecond, WriteTimeout: 20 * time.Millisecond,
	})
	t.Cleanup(func() { _ = c.Close() })
	k := mint(t, apikey.Test, "ak_down", domain.ScopeDeliveriesRead)
	e := limited(t, redislimit.NewLimiter(c), k)

	start := time.Now()
	rec := e.do(get("/v1/me", "Bearer "+k.raw))
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("took %v, want < 200ms", elapsed)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	for name := range rec.Header() {
		if strings.HasPrefix(strings.ToLower(name), "ratelimit") {
			t.Fatalf("unexpected header %s", name)
		}
	}
	logs := e.logs.String()
	if n := strings.Count(logs, "rate_limiter_unavailable"); n != 1 {
		t.Fatalf("rate_limiter_unavailable logged %d times: %s", n, logs)
	}
	if strings.Contains(logs, k.raw) || strings.Contains(logs, k.lookup) {
		t.Fatalf("log contains the key: %s", logs)
	}

	// The failed-auth limiter fails open too: the normal 401, one warn.
	e.logs.Reset()
	wantProblem(t, e.do(get("/v1/me", "Bearer "+mint(t, apikey.Test, "ak_y").raw)), http.StatusUnauthorized, "unauthenticated")
	if n := strings.Count(e.logs.String(), "rate_limiter_unavailable"); n != 1 {
		t.Fatalf("rate_limiter_unavailable logged %d times", n)
	}
}

func TestFailedAuthUnknownIPSharesOneBucket(t *testing.T) {
	t.Parallel()
	var seen []string
	e := limited(t, limiterFunc(func(key string) {
		seen = append(seen, key)
	}))
	r := get("/v1/me", "")
	r.RemoteAddr = "garbage"
	wantProblem(t, e.do(r), http.StatusUnauthorized, "unauthenticated")
	if fmt.Sprint(seen) != "[rl:authfail:unknown]" {
		t.Fatalf("keys = %v", seen)
	}
}

// limiterFunc allows everything and records the keys.
type limiterFunc func(key string)

func (f limiterFunc) Allow(_ context.Context, key string, l app.Limit) (app.Decision, error) {
	f(key)
	return app.Decision{Allowed: true, Limit: l.Burst, Remaining: l.Burst - 1}, nil
}
