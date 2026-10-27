package redislimit_test

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/raduolivas/dt/backend/pkg/testkit"
	"github.com/raduolivas/dt/backend/pkg/viewerjwt"
	redislimit "github.com/raduolivas/dt/backend/services/gateway/internal/adapters/outbound/redis"
)

func TestRevokerDenyListsAndPublishes(t *testing.T) {
	t.Parallel()
	opts, err := redis.ParseURL(testkit.StartRedis(t))
	if err != nil {
		t.Fatal(err)
	}
	c := redis.NewClient(opts)
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	sub := c.Subscribe(ctx, "dt:viewer:revocations")
	t.Cleanup(func() { _ = sub.Close() })
	if _, err := sub.Receive(ctx); err != nil { // subscription confirmed
		t.Fatal(err)
	}

	const id = "tl_01j9z3k8m2q4r6s8t0v2w4x6y8"
	if err := redislimit.NewRevoker(c).Revoke(ctx, id); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-sub.Channel():
		if m.Channel != viewerjwt.RevocationsChannel || m.Payload != id {
			t.Fatalf("message = %+v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no revocation published")
	}
	ttl, err := c.TTL(ctx, "dt:viewer:revoked:"+id).Result()
	if err != nil || ttl <= 15*time.Minute || ttl > viewerjwt.RevokedTTL {
		t.Fatalf("deny-list ttl = %v, %v (want the token lifetime plus leeway)", ttl, err)
	}
}
