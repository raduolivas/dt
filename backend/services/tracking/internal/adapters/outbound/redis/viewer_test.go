package redis_test

import (
	"context"
	"sync"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/raduolivas/dt/backend/pkg/viewerjwt"
	"github.com/raduolivas/dt/backend/services/tracking/internal/adapters/outbound/redis"
	"github.com/raduolivas/dt/backend/services/tracking/internal/app"
	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

func TestActiveSetScopedAndScored(t *testing.T) {
	rdb := client(t)
	ctx := context.Background()
	s := redis.NewStates(rdb, nil)
	a, b := app.Scope{TenantID: "ten_a", Env: "test"}, app.Scope{TenantID: "ten_b", Env: "live"}
	t0 := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	if err := s.SetActive(ctx, a, "del_1", t0, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetActive(ctx, b, "del_2", t0.Add(time.Minute), true); err != nil {
		t.Fatal(err)
	}
	// A newer fix moves the score.
	if err := s.SetActive(ctx, a, "del_1", t0.Add(10*time.Second), true); err != nil {
		t.Fatal(err)
	}
	got, err := s.ActiveSince(ctx, t0.Add(30*time.Second))
	if err != nil || len(got) != 1 || got[0] != (app.ActiveDelivery{Scope: a, DeliveryID: "del_1"}) {
		t.Fatalf("ActiveSince = %+v, %v", got, err)
	}
	got, _ = s.ActiveSince(ctx, t0.Add(time.Hour))
	if len(got) != 2 || got[1] != (app.ActiveDelivery{Scope: b, DeliveryID: "del_2"}) {
		t.Fatalf("ActiveSince(all) = %+v", got)
	}
	if err := s.SetActive(ctx, a, "del_1", time.Time{}, false); err != nil {
		t.Fatal(err)
	}
	if n := rdb.ZCard(ctx, "dt:active").Val(); n != 1 {
		t.Fatalf("dt:active has %d members after a removal", n)
	}
	// A malformed member is skipped, never mistaken for a scope.
	rdb.ZAdd(ctx, "dt:active", goredisZ(0, "del_bad"))
	got, err = s.ActiveSince(ctx, t0.Add(time.Hour))
	if err != nil || len(got) != 1 {
		t.Fatalf("with a malformed member: %+v, %v", got, err)
	}
}

func TestLeaseSingleHolder(t *testing.T) {
	rdb := client(t)
	ctx := context.Background()
	l := redis.NewLease(rdb, "dt:lease:sweeper")
	if ok, err := l.Hold(ctx, "a", 10*time.Second); err != nil || !ok {
		t.Fatalf("a: %v %v", ok, err)
	}
	if ok, _ := l.Hold(ctx, "b", 10*time.Second); ok {
		t.Fatal("b took a held lease")
	}
	if ok, _ := l.Hold(ctx, "a", 10*time.Second); !ok {
		t.Fatal("a could not renew")
	}
	if ttl := rdb.PTTL(ctx, "dt:lease:sweeper").Val(); ttl <= 9*time.Second {
		t.Fatalf("lease ttl after renew = %v", ttl)
	}
	if err := l.Release(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := l.Hold(ctx, "b", 10*time.Second); ok {
		t.Fatal("b released a's lease")
	}
	if err := l.Release(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := l.Hold(ctx, "b", 100*time.Millisecond); !ok {
		t.Fatal("b could not take a released lease")
	}
	time.Sleep(200 * time.Millisecond)
	if ok, _ := l.Hold(ctx, "a", 10*time.Second); !ok {
		t.Fatal("a could not take an expired lease")
	}
}

func TestViewerLinksReadDenyListAndExpiry(t *testing.T) {
	rdb := client(t)
	ctx := context.Background()
	v := redis.NewViewerLinks(rdb)
	if r, err := v.Revoked(ctx, "tl_1"); err != nil || r {
		t.Fatalf("fresh link revoked = %v, %v", r, err)
	}
	rdb.Set(ctx, viewerjwt.RevokedKey("tl_1"), "1", time.Minute)
	if r, err := v.Revoked(ctx, "tl_1"); err != nil || !r {
		t.Fatalf("deny-listed link revoked = %v, %v", r, err)
	}
	if _, ok, err := v.Expiry(ctx, "tl_1"); err != nil || ok {
		t.Fatalf("no expiry: %v %v", ok, err)
	}
	at := time.Unix(1_800_000_000, 0).UTC()
	rdb.Set(ctx, viewerjwt.ExpiryKey("tl_1"), at.Unix(), time.Minute)
	if got, ok, err := v.Expiry(ctx, "tl_1"); err != nil || !ok || !got.Equal(at) {
		t.Fatalf("expiry = %v %v %v", got, ok, err)
	}
	rdb.Set(ctx, viewerjwt.ExpiryKey("tl_2"), "garbage", time.Minute)
	if _, _, err := v.Expiry(ctx, "tl_2"); err == nil {
		t.Fatal("malformed expiry accepted")
	}
}

func TestSnapshotLimiterFixedWindow(t *testing.T) {
	rdb := client(t)
	ctx := context.Background()
	l := redis.NewViewerLinks(rdb)
	for i := range 30 {
		if ok, _, err := l.AllowSnapshot(ctx, "tokhash"); err != nil || !ok {
			t.Fatalf("request %d: %v %v", i, ok, err)
		}
	}
	ok, retry, err := l.AllowSnapshot(ctx, "tokhash")
	if err != nil || ok || retry <= 0 || retry > time.Minute {
		t.Fatalf("31st request: %v %v %v", ok, retry, err)
	}
	if ok, _, _ := l.AllowSnapshot(ctx, "other"); !ok {
		t.Fatal("another token was limited")
	}
}

// feedEvents records what a Feed delivers.
type feedEvents struct {
	mu      sync.Mutex
	snaps   map[string][]string
	revoked []string
	expiry  map[string]time.Time
	resyncs int
	notify  chan struct{}
}

func newFeedEvents() *feedEvents {
	return &feedEvents{snaps: map[string][]string{}, expiry: map[string]time.Time{}, notify: make(chan struct{}, 64)}
}

func (e *feedEvents) poke() {
	select {
	case e.notify <- struct{}{}:
	default:
	}
}

func (e *feedEvents) Snapshot(id string, b []byte) {
	e.mu.Lock()
	e.snaps[id] = append(e.snaps[id], string(b))
	e.mu.Unlock()
	e.poke()
}

func (e *feedEvents) Revoked(id string) {
	e.mu.Lock()
	e.revoked = append(e.revoked, id)
	e.mu.Unlock()
	e.poke()
}

func (e *feedEvents) Expiry(id string, at time.Time) {
	e.mu.Lock()
	e.expiry[id] = at
	e.mu.Unlock()
	e.poke()
}

func (e *feedEvents) Resync() {
	e.mu.Lock()
	e.resyncs++
	e.mu.Unlock()
	e.poke()
}

func (e *feedEvents) waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		e.mu.Lock()
		ok := cond()
		e.mu.Unlock()
		if ok {
			return
		}
		select {
		case <-e.notify:
		case <-time.After(50 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func TestFeedDeliversSnapshotsRevocationsAndExpiries(t *testing.T) {
	rdb := client(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	feed := redis.NewFeed(rdb, nil)
	ev := newFeedEvents()
	done := make(chan error, 1)
	go func() { done <- feed.Run(ctx, ev) }()

	if err := feed.Subscribe(ctx, "del_1"); err != nil {
		t.Fatal(err)
	}
	if err := feed.Subscribe(ctx, "del_1"); err != nil { // a second local stream
		t.Fatal(err)
	}
	pub := redis.NewPublisher(rdb)
	// Subscribe returned: the subscription is active, nothing is missed.
	if err := pub.Snapshot(ctx, "del_1", domain.TrackingSnapshot{DeliveryID: "del_1", Version: 7}); err != nil {
		t.Fatal(err)
	}
	ev.waitFor(t, "snapshot", func() bool { return len(ev.snaps["del_1"]) == 1 })

	rdb.Publish(ctx, viewerjwt.RevocationsChannel, "tl_1")
	at := time.Unix(1_800_000_000, 0).UTC()
	rdb.Publish(ctx, viewerjwt.ExpiriesChannel, viewerjwt.EncodeExpiry("tl_2", at))
	rdb.Publish(ctx, viewerjwt.ExpiriesChannel, "garbage")
	ev.waitFor(t, "revocation and expiry", func() bool {
		return len(ev.revoked) == 1 && ev.revoked[0] == "tl_1" && ev.expiry["tl_2"].Equal(at)
	})

	// One of two streams leaves: still subscribed. Both leave: unsubscribed.
	feed.Unsubscribe(ctx, "del_1")
	_ = pub.Snapshot(ctx, "del_1", domain.TrackingSnapshot{DeliveryID: "del_1", Version: 8})
	ev.waitFor(t, "second snapshot", func() bool { return len(ev.snaps["del_1"]) == 2 })
	feed.Unsubscribe(ctx, "del_1")
	deadline := time.Now().Add(5 * time.Second)
	for {
		n, _ := rdb.PubSubNumSub(ctx, "dt:snap:del_1").Result()
		if n["dt:snap:del_1"] == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("still subscribed after the last stream left")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
}

func TestFeedResubscribesAndResyncsAfterReconnect(t *testing.T) {
	rdb := client(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	feed := redis.NewFeed(rdb, nil)
	ev := newFeedEvents()
	go func() { _ = feed.Run(ctx, ev) }()
	if err := feed.Subscribe(ctx, "del_r"); err != nil {
		t.Fatal(err)
	}
	if err := rdb.ClientKillByFilter(ctx, "TYPE", "pubsub").Err(); err != nil {
		t.Fatal(err)
	}
	ev.waitFor(t, "resync", func() bool { return ev.resyncs >= 1 })
	pub := redis.NewPublisher(rdb)
	ev.waitFor(t, "snapshot after reconnect", func() bool {
		_ = pub.Snapshot(ctx, "del_r", domain.TrackingSnapshot{DeliveryID: "del_r", Version: 1})
		return len(ev.snaps["del_r"]) > 0
	})
}

func goredisZ(score float64, m string) goredis.Z { return goredis.Z{Score: score, Member: m} }
