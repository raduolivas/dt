package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/index"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/retention"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), startPostgres(t))
	require.NoError(t, err)
	t.Cleanup(s.Close)
	return s
}

func created(tenant, env, ref, del string, at time.Time) store.Event {
	return store.Event{ObservedAt: at, OccurredAt: at, Kind: "event", Source: "deliveries", TenantID: tenant, Env: env,
		DeliveryID: del, OrderRef: ref, Origin: "live"}
}

func TestSearchByOrderRef(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	idx := index.New(s)
	now := time.Now().UTC()
	require.NoError(t, idx.OnEvent(ctx, created("t1", "test", "ORD-1", "del_1", now)))
	require.NoError(t, idx.OnEvent(ctx, store.Event{Kind: "span", DeliveryID: "del_1"}), "non-created events are ignored")
	hits, err := s.SearchOrder(ctx, "ORD-1")
	require.NoError(t, err)
	require.Len(t, hits, 1)
	require.Equal(t, "t1", hits[0].TenantID)
	require.Equal(t, "test", hits[0].Environment)
	require.Equal(t, "del_1", hits[0].DeliveryID)
	require.Equal(t, "live", hits[0].Origin)
	hits, err = s.SearchOrder(ctx, "ORD-2")
	require.NoError(t, err)
	require.Empty(t, hits)

	// replay of the same event is idempotent and keeps the first delivery id
	require.NoError(t, idx.OnEvent(ctx, created("t1", "test", "ORD-1", "del_other", now)))
	hits, _ = s.SearchOrder(ctx, "ORD-1")
	require.Len(t, hits, 1)
	require.Equal(t, "del_1", hits[0].DeliveryID)
}

func TestSearchAmbiguousOrder(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	idx := index.New(s)
	now := time.Now().UTC()
	require.NoError(t, idx.OnEvent(ctx, created("t1", "test", "ORD-9", "del_a", now)))
	require.NoError(t, idx.OnEvent(ctx, created("t2", "test", "ORD-9", "del_b", now)))
	hits, err := s.SearchOrder(ctx, "ORD-9")
	require.NoError(t, err)
	require.Len(t, hits, 2)
}

func TestSearchByDeliveryID(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	require.NoError(t, index.New(s).OnEvent(ctx, created("t1", "test", "ORD-1", "del_77", time.Now().UTC())))
	hits, err := s.SearchOrder(ctx, "del_77")
	require.NoError(t, err)
	require.Len(t, hits, 1)
	require.Equal(t, "ORD-1", hits[0].OrderRef)
}

func TestTimelineIncludesTraceSiblings(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	now := time.Now().UTC()
	require.NoError(t, s.InsertEvents(ctx, []store.Event{
		{ObservedAt: now, OccurredAt: now, Kind: "event", Source: "deliveries", DeliveryID: "del_1", TraceID: "tr1"},
		{ObservedAt: now, OccurredAt: now, Kind: "span", Source: "gateway", TraceID: "tr1"}, // no delivery id
		{ObservedAt: now, OccurredAt: now, Kind: "span", Source: "gateway", TraceID: "tr2"}, // unrelated
		{ObservedAt: now, OccurredAt: now, Kind: "event", Source: "deliveries", DeliveryID: "del_2"},
		{ObservedAt: now, OccurredAt: now, Kind: "event", Source: "deliveries", DeliveryID: "del_1"}, // no trace
	}))
	evs, next, err := s.Timeline(ctx, "del_1", 0, 100)
	require.NoError(t, err)
	require.Len(t, evs, 3)
	require.Zero(t, next)
	require.Equal(t, "gateway", evs[1].Source)
}

func TestTimelinePagination(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	now := time.Now().UTC()
	var in []store.Event
	for range 5 {
		in = append(in, store.Event{ObservedAt: now, OccurredAt: now, Kind: "event", Source: "d", DeliveryID: "del_1"})
	}
	require.NoError(t, s.InsertEvents(ctx, in))
	var got []int64
	var after int64
	pages := 0
	for {
		evs, next, err := s.Timeline(ctx, "del_1", after, 2)
		require.NoError(t, err)
		for _, e := range evs {
			got = append(got, e.ID)
		}
		pages++
		if next == 0 {
			break
		}
		after = next
	}
	require.Len(t, got, 5)
	require.Equal(t, 3, pages)
	require.IsIncreasing(t, got)

}

func TestTimelineLimitIsClamped(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	now := time.Now().UTC()
	in := make([]store.Event, store.MaxTimelineLimit+11)
	for i := range in {
		in[i] = store.Event{ObservedAt: now, OccurredAt: now, Kind: "event", Source: "d", DeliveryID: "del_1"}
	}
	require.NoError(t, s.InsertEvents(ctx, in))
	evs, next, err := s.Timeline(ctx, "del_1", 0, 100000)
	require.NoError(t, err)
	require.Len(t, evs, store.MaxTimelineLimit, "limit is clamped, not rejected")
	require.NotZero(t, next)
	rest, next2, err := s.Timeline(ctx, "del_1", next, 100000)
	require.NoError(t, err)
	require.Len(t, rest, 11)
	require.Zero(t, next2)
}

func TestRetentionKeepsOrderWhileEventsExist(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	now := time.Now().UTC()
	old := now.Add(-8 * 24 * time.Hour)
	idx := index.New(s)
	require.NoError(t, idx.OnEvent(ctx, created("t", "e", "LIVE", "del_live", old)))
	require.NoError(t, s.InsertEvents(ctx, []store.Event{
		{ObservedAt: old, OccurredAt: old, Kind: "event", Source: "d", DeliveryID: "del_live"},
		{ObservedAt: now, OccurredAt: now, Kind: "event", Source: "d", DeliveryID: "del_live"},
	}))
	require.NoError(t, idx.OnEvent(ctx, created("t", "e", "DEAD", "del_dead", old)))
	cutoff := now.Add(-7 * 24 * time.Hour)
	_, err := s.DeleteOlderThan(ctx, cutoff)
	require.NoError(t, err)
	n, err := s.DeleteOrdersOlderThan(ctx, cutoff)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	h, _ := s.SearchOrder(ctx, "LIVE")
	require.Len(t, h, 1, "order stays searchable while its events exist")
	h, _ = s.SearchOrder(ctx, "DEAD")
	require.Empty(t, h)
}

func TestTimelineUsesIndexes(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	now := time.Now().UTC()
	in := make([]store.Event, 50000)
	for i := range in {
		in[i] = store.Event{ObservedAt: now, OccurredAt: now, Kind: "event", Source: "d",
			DeliveryID: fmt.Sprintf("del_%d", i%2000), TraceID: fmt.Sprintf("tr_%d", i%2000)}
		if i%3 == 0 {
			in[i].DeliveryID = "" // span-like sibling
		}
	}
	for i := 0; i < len(in); i += 5000 {
		require.NoError(t, s.InsertEvents(ctx, in[i:i+5000]))
	}
	plan, err := s.ExplainTimeline(ctx, "del_7", 0, 100)
	require.NoError(t, err)
	t.Log("\n" + plan)
	require.NotContains(t, plan, "Seq Scan on mc_event")
	require.NotContains(t, plan, "mc_event_pkey")
	require.Contains(t, plan, "mc_event_delivery_idx")
	require.Contains(t, plan, "mc_event_trace_idx")
}

func TestDowntimeOverlap(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	t0 := time.Now().UTC().Truncate(time.Millisecond).Add(-2 * time.Hour)
	require.NoError(t, s.Heartbeat(ctx, t0))
	require.NoError(t, s.RecordStartup(ctx, t0.Add(time.Hour)))
	ivs, err := s.Downtime(ctx, t0.Add(30*time.Minute), t0.Add(90*time.Minute))
	require.NoError(t, err)
	require.Len(t, ivs, 1)
	require.True(t, ivs[0].Start.Equal(t0))
	ivs, err = s.Downtime(ctx, t0.Add(2*time.Hour), t0.Add(3*time.Hour))
	require.NoError(t, err)
	require.Empty(t, ivs)
}

func TestRetentionDeletesOld(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := openStore(t)
	now := time.Now().UTC()
	old := now.Add(-8 * 24 * time.Hour)
	require.NoError(t, s.InsertEvents(ctx, []store.Event{
		{ObservedAt: old, OccurredAt: old, Kind: "event", Source: "d"},
		{ObservedAt: now, OccurredAt: now, Kind: "event", Source: "d"},
	}))
	idx := index.New(s)
	require.NoError(t, idx.OnEvent(ctx, created("t", "e", "OLD", "del_o", old)))
	require.NoError(t, idx.OnEvent(ctx, created("t", "e", "NEW", "del_n", now)))

	done := make(chan struct{})
	go func() { defer close(done); retention.Run(ctx, s, time.Hour, 7*24*time.Hour, nil) }() // first prune runs at startup
	require.Eventually(t, func() bool { n, _ := s.CountEvents(ctx); return n == 1 }, 5*time.Second, 50*time.Millisecond)
	require.Eventually(t, func() bool { h, _ := s.SearchOrder(ctx, "OLD"); return len(h) == 0 }, 5*time.Second, 50*time.Millisecond)
	h, _ := s.SearchOrder(ctx, "NEW")
	require.Len(t, h, 1)
	cancel()
	<-done
}
