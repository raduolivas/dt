package redislimit_test

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

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
