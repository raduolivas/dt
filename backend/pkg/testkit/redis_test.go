package testkit_test

import (
	"context"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/raduolivas/dt/backend/pkg/testkit"
)

func TestStartRedisPings(t *testing.T) {
	url := testkit.StartRedis(t)
	opt, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	c := redis.NewClient(opt)
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Ping(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
}
