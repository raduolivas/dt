package redis_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/raduolivas/dt/backend/pkg/bootstrap"
	"github.com/raduolivas/dt/backend/pkg/testkit"
	"github.com/raduolivas/dt/backend/services/tracking/internal/adapters/outbound/redis"
	"github.com/raduolivas/dt/backend/services/tracking/internal/app"
	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

func client(t *testing.T) *goredis.Client {
	t.Helper()
	opts, err := bootstrap.RedisOptions(testkit.StartRedis(t))
	if err != nil {
		t.Fatal(err)
	}
	c := goredis.NewClient(opts)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestStatePutIsCompareAndSet(t *testing.T) {
	rdb := client(t)
	ctx := context.Background()
	s := redis.NewStates(rdb, nil)
	st := domain.State{DeliveryID: "del_1", TenantID: "ten_a", Env: "test", Status: domain.StatusPickedUp, Version: 100}
	if ok, err := s.Put(ctx, st, 0); err != nil || !ok {
		t.Fatalf("first put: %v %v", ok, err)
	}
	// A writer that loaded nothing (or an older version) loses.
	st2 := st
	st2.Version = 200
	if ok, _ := s.Put(ctx, st2, 0); ok {
		t.Fatal("put with a stale previous version succeeded")
	}
	if ok, _ := s.Put(ctx, st2, 50); ok {
		t.Fatal("put with a wrong previous version succeeded")
	}
	if ok, _ := s.Put(ctx, st2, 100); !ok {
		t.Fatal("put on the current version failed")
	}
	if _, err := s.Put(ctx, st2, 200); err == nil {
		t.Fatal("put without a higher version must error")
	}
	got, ok, err := s.Get(ctx, "del_1")
	if err != nil || !ok || got.Version != 200 || got.Status != domain.StatusPickedUp {
		t.Fatalf("Get = %+v %v %v", got, ok, err)
	}
	if ttl := rdb.TTL(ctx, "dt:state:del_1").Val(); ttl <= redis.FinalStateTTL {
		t.Fatalf("active state TTL %v", ttl)
	}
	st3 := got
	st3.Status, st3.Version = domain.StatusDelivered, 300
	if ok, _ := s.Put(ctx, st3, 200); !ok {
		t.Fatal("final put failed")
	}
	if ttl := rdb.TTL(ctx, "dt:state:del_1").Val(); ttl > redis.FinalStateTTL || ttl < redis.FinalStateTTL-time.Minute {
		t.Fatalf("final state TTL %v, want 24 h", ttl)
	}
	if _, ok, _ := s.Get(ctx, "del_missing"); ok {
		t.Fatal("missing state found")
	}
}

func TestActiveSetRebuildsAndKeepsScope(t *testing.T) {
	rdb := client(t)
	ctx := context.Background()
	calls := 0
	src := func(_ context.Context, sc app.Scope, cou string) ([]string, error) {
		calls++
		if sc.TenantID != "ten_a" || cou != "cou_1" {
			return nil, app.ErrCourierNotFound
		}
		return []string{"del_1", "del_2"}, nil
	}
	s := redis.NewStates(rdb, src)
	ids, err := s.ActiveDeliveriesForCourier(ctx, "ten_a", "test", "cou_1")
	if err != nil || len(ids) != 2 || calls != 1 {
		t.Fatalf("rebuild: %v %v calls=%d", ids, err, calls)
	}
	// Cached now.
	if ids, _ := s.ActiveDeliveriesForCourier(ctx, "ten_a", "test", "cou_1"); len(ids) != 2 || calls != 1 {
		t.Fatalf("cached: %v calls=%d", ids, calls)
	}
	// Another tenant (or environment) asking for the cached courier.
	if _, err := s.ActiveDeliveriesForCourier(ctx, "ten_b", "test", "cou_1"); !errors.Is(err, app.ErrCourierNotFound) {
		t.Fatalf("other tenant: %v", err)
	}
	if _, err := s.ActiveDeliveriesForCourier(ctx, "ten_a", "live", "cou_1"); !errors.Is(err, app.ErrCourierNotFound) {
		t.Fatalf("other environment: %v", err)
	}
	if err := s.SetCourierActive(ctx, "cou_1", "del_1", false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCourierActive(ctx, "cou_1", "del_3", true); err != nil {
		t.Fatal(err)
	}
	ids, _ = s.ActiveDeliveriesForCourier(ctx, "ten_a", "test", "cou_1")
	if len(ids) != 2 || !contains(ids, "del_2") || !contains(ids, "del_3") {
		t.Fatalf("after updates: %v", ids)
	}
	// A set that is not cached is not created by an update.
	if err := s.SetCourierActive(ctx, "cou_9", "del_9", true); err != nil {
		t.Fatal(err)
	}
	if rdb.Exists(ctx, "dt:courier:cou_9:active").Val() != 0 {
		t.Fatal("update created a partial set")
	}
	// Courier with no active delivery: cached as empty, still scoped.
	s2 := redis.NewStates(rdb, func(context.Context, app.Scope, string) ([]string, error) { return nil, nil })
	if ids, err := s2.ActiveDeliveriesForCourier(ctx, "ten_a", "test", "cou_idle"); err != nil || len(ids) != 0 {
		t.Fatalf("idle: %v %v", ids, err)
	}
	if _, err := s2.ActiveDeliveriesForCourier(ctx, "ten_b", "test", "cou_idle"); !errors.Is(err, app.ErrCourierNotFound) {
		t.Fatalf("idle courier, other tenant: %v", err)
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func TestSamplerAllowsOncePerMinute(t *testing.T) {
	rdb := client(t)
	ctx := context.Background()
	s := redis.NewStates(rdb, nil)
	if ok, err := s.Allow(ctx, "cou_1"); err != nil || !ok {
		t.Fatalf("first: %v %v", ok, err)
	}
	if ok, _ := s.Allow(ctx, "cou_1"); ok {
		t.Fatal("second within the minute allowed")
	}
	if ok, _ := s.Allow(ctx, "cou_2"); !ok {
		t.Fatal("other courier refused")
	}
	if ttl := rdb.TTL(ctx, "dt:courier:cou_1:reported").Val(); ttl <= 0 || ttl > time.Minute {
		t.Fatalf("ttl %v", ttl)
	}
}

func TestPublisherSendsSnapshotJSON(t *testing.T) {
	rdb := client(t)
	ctx := context.Background()
	sub := rdb.Subscribe(ctx, redis.SnapChannel("del_1"))
	t.Cleanup(func() { _ = sub.Close() })
	if _, err := sub.Receive(ctx); err != nil { // subscription confirmation
		t.Fatal(err)
	}
	p := redis.NewPublisher(rdb)
	if err := p.Snapshot(ctx, "del_1", domain.TrackingSnapshot{DeliveryID: "del_1", Version: 42}); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-sub.Channel():
		var got domain.TrackingSnapshot
		if err := json.Unmarshal([]byte(m.Payload), &got); err != nil || got.Version != 42 || m.Channel != "dt:snap:del_1" {
			t.Fatalf("got %s on %s: %v", m.Payload, m.Channel, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no message")
	}
}
