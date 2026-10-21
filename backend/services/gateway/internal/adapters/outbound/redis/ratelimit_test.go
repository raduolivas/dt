package redislimit_test

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/raduolivas/dt/backend/pkg/bootstrap"
	"github.com/raduolivas/dt/backend/pkg/testkit"
	redislimit "github.com/raduolivas/dt/backend/services/gateway/internal/adapters/outbound/redis"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
)

func TestLimiterAllowsBurstThenDenies(t *testing.T) {
	t.Parallel()
	opts, err := redis.ParseURL(testkit.StartRedis(t))
	if err != nil {
		t.Fatal(err)
	}
	c := redis.NewClient(opts)
	t.Cleanup(func() { _ = c.Close() })
	l := redislimit.NewLimiter(c)

	// Slow refill so the test is not racing the clock.
	lim := app.Limit{Rate: 1, Period: time.Hour, Burst: 3}
	for i := range 3 {
		d, err := l.Allow(context.Background(), "k", lim)
		if err != nil || !d.Allowed || d.Limit != 3 || d.Remaining != 2-i {
			t.Fatalf("call %d: %+v, %v", i, d, err)
		}
	}
	d, err := l.Allow(context.Background(), "k", lim)
	if err != nil || d.Allowed || d.RetryAfter <= 0 || d.Remaining != 0 {
		t.Fatalf("4th call: %+v, %v", d, err)
	}
}

// hangingRedis accepts connections and never answers (a hung Redis: no
// reset, no reply). It returns a redis:// URL for it.
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

// TestLimiterGivesUpOnHungRedis builds the client exactly as bootstrap does
// and checks the 50 ms call budget holds against a Redis that never replies,
// so the gateway can fail open (R21).
func TestLimiterGivesUpOnHungRedis(t *testing.T) {
	t.Parallel()
	opts, err := bootstrap.RedisOptions(hangingRedis(t))
	if err != nil {
		t.Fatal(err)
	}
	c := redis.NewClient(opts)
	t.Cleanup(func() { _ = c.Close() })
	l := redislimit.NewLimiter(c)

	for i := range 2 { // a fresh dial, then a pooled (or redialled) conn
		start := time.Now()
		_, err := l.Allow(context.Background(), "k", app.PartnerKey)
		elapsed := time.Since(start)
		t.Logf("call %d against a hung redis returned after %v", i+1, elapsed)
		if err == nil || elapsed > 200*time.Millisecond {
			t.Fatalf("call %d: Allow = %v after %v, want an error well under 200ms", i+1, err, elapsed)
		}
	}
}
