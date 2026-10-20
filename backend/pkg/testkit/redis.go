package testkit

import (
	"context"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

// RedisImage matches the local stack.
const RedisImage = "redis:8.2-alpine"

// StartRedis starts a container and returns a redis:// URL. It terminates the
// container when t finishes and skips under -short.
func StartRedis(t testing.TB) string {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test: needs Docker (run without -short)")
	}
	ctx := context.Background()
	c, err := tcredis.Run(ctx, RedisImage)
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatalf("start redis: %v", err)
	}
	url, err := c.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("redis url: %v", err)
	}
	return url
}
