package detect

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test: needs Docker (run without -short)")
	}
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgis/postgis:18-3.6",
		postgres.WithDatabase("mc"), postgres.WithUsername("mc"), postgres.WithPassword("mc"),
		postgres.BasicWaitStrategies())
	testcontainers.CleanupContainer(t, c)
	require.NoError(t, err)
	url, err := c.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	s, err := store.Open(ctx, url)
	require.NoError(t, err)
	t.Cleanup(s.Close)
	return s
}

func raw(at time.Time, kind, source, target, outcome, reason, delivery, trace, attrs string) store.Event {
	return store.Event{ObservedAt: at, OccurredAt: at, Kind: kind, Source: source, Target: target, Outcome: outcome,
		Reason: reason, DeliveryID: delivery, TraceID: trace, TenantID: "t1", Env: "test", Origin: "live", Attrs: []byte(attrs)}
}

// Scenarios 1 and 4 end to end over the real store: events in, findings out,
// then healed and cleared. The fake clock is the only time source.
func TestEngineOverTheStoreCourierStaleAndReassigned(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	ck := &clock{t: now}
	fix := func(at time.Time, trace string) []store.Event {
		return []store.Event{
			raw(at, "request", "tracking.ingest", "tracking.ingest", "ok", "", "", trace, `{"span_name":"tracking.ingest","tracking.fixes.accepted":1}`),
			raw(at, "state_change", "tracking.state", "tracking.state", "ok", "", "del_1", trace, `{"span_name":"tracking.apply"}`),
		}
	}
	require.NoError(t, st.InsertEvents(ctx, append([]store.Event{
		raw(now.Add(-time.Hour), "message_published", "deliveries", "nats.DELIVERIES", "ok", "", "del_1", "",
			`{"event_type":"delivery.status_changed","to":"picked_up"}`),
	}, fix(now.Add(-45*time.Second), "tr1")...)))

	var got []Finding
	eng := NewEngine(storeSource{st}, defaults(t), ck.now, func(f Finding) { got = append(got, f) }, nil)
	require.NoError(t, eng.Tick(ctx))
	require.Len(t, got, 1)
	f := got[0]
	require.Equal(t, "courier_stale", f.Detector)
	require.Equal(t, "courier", f.Hop)
	require.Equal(t, "del_1", f.DeliveryID)
	require.Len(t, f.Evidence, 1)
	ev, err := st.EventByID(ctx, f.Evidence[0])
	require.NoError(t, err, "evidence is a real mc_event id")
	require.Equal(t, "tracking.state", ev.Source)

	// the finding is listed as active; a restarted engine does not repeat it
	act, err := st.RecentDetectors(ctx, time.Time{}, 10)
	require.NoError(t, err)
	require.Len(t, act, 1)
	require.Equal(t, "courier", act[0].Target)
	var again []Finding
	require.NoError(t, NewEngine(storeSource{st}, defaults(t), ck.now, func(f Finding) { again = append(again, f) }, nil).Tick(ctx))
	require.Empty(t, again)

	// scenario 4: the delivery drops the courier's fixes as reassigned
	drop := raw(now.Add(time.Second), "log", "tracking.ingest", "tracking.ingest", "dropped", "delivery_reassigned", "del_1", "tr2", `{"event":"fix_dropped"}`)
	require.NoError(t, st.InsertEvents(ctx, []store.Event{drop}))
	ck.t = now.Add(2 * time.Second)
	require.NoError(t, eng.Tick(ctx))
	require.Len(t, got, 2)
	require.Equal(t, "fixes_dropped", got[1].Detector)
	require.Equal(t, "tracking.ingest", got[1].Hop)
	require.Equal(t, "delivery_reassigned", got[1].Reason)

	// heal: a fresh accepted fix clears courier_stale
	require.NoError(t, st.InsertEvents(ctx, fix(now.Add(3*time.Second), "tr3")))
	ck.t = now.Add(4 * time.Second)
	require.NoError(t, eng.Tick(ctx))
	require.Len(t, got, 3)
	require.Equal(t, SevCleared, got[2].Severity)
	require.Equal(t, "courier_stale", got[2].Detector)
	act, err = st.RecentDetectors(ctx, time.Time{}, 10)
	require.NoError(t, err)
	require.Len(t, act, 1, "only fixes_dropped is still open")
	require.Equal(t, "tracking.ingest", act[0].Target)

	all, err := st.DeliveryDetectors(ctx, "del_1")
	require.NoError(t, err)
	require.Len(t, all, 3, "warning, drops, clear (history is kept for the order page)")
}

func TestStoreQueriesStatusesHealthAndWindow(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, st.InsertEvents(ctx, []store.Event{
		raw(now.Add(-2*time.Hour), "message_published", "deliveries", "nats.DELIVERIES", "ok", "", "d1", "", `{"event_type":"delivery.created","status":"confirmed"}`),
		raw(now.Add(-time.Hour), "message_published", "deliveries", "nats.DELIVERIES", "ok", "", "d1", "", `{"event_type":"delivery.status_changed","to":"picked_up"}`),
		raw(now.Add(-48*time.Hour), "message_published", "deliveries", "nats.DELIVERIES", "ok", "", "d2", "", `{"event_type":"delivery.created","status":"confirmed"}`),
		raw(now.Add(-time.Hour), "health", "nats.TRACKING", "", "ok", "", "", "", `{"consumer":"c","pending":4}`),
		raw(now.Add(-time.Minute), "health", "nats.TRACKING", "", "ok", "", "", "", `{"consumer":"c","pending":2}`),
		raw(now.Add(-time.Minute), "health", "gateway", "", "ok", "", "", "", `{"live":true}`),
		raw(now.Add(-time.Second), "request", "gateway.courier_api", "", "ok", "", "", "", ""),
	}))
	sts, err := st.DeliveryStatuses(ctx, now.Add(-24*time.Hour))
	require.NoError(t, err)
	require.Len(t, sts, 1, "newest status of d1; d2 is older than the lookback")
	require.Contains(t, string(sts[0].Attrs), "picked_up")

	h, err := st.HealthSince(ctx, now.Add(-5*time.Minute))
	require.NoError(t, err)
	require.Len(t, h, 2, "the recent consumer and gateway rows; the superseded hour-old consumer row is left out")
	h, err = st.HealthSince(ctx, now.Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, h, 2, "the newest per source and consumer is kept however old")
	win, err := st.EventsObservedSince(ctx, now.Add(-time.Minute-time.Second), 100)
	require.NoError(t, err)
	require.Len(t, win, 1, "no health, no detector rows in the window")
}

func TestRunStoresAndEmits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := openStore(t)
	now := time.Now().UTC()
	require.NoError(t, st.InsertEvents(ctx, []store.Event{
		raw(now, "request", "gateway.courier_api", "gateway.courier_api", "rejected", "unauthorized", "", "", `{"http_status":401}`),
	}))
	got := make(chan Finding, 4)
	go Run(ctx, st, 50*time.Millisecond, func(f Finding) { got <- f }, Options{Thresholds: defaults(t)})
	select {
	case f := <-got:
		require.Equal(t, "rejected_at_gateway", f.Detector)
		require.Equal(t, "unauthorized", f.Reason)
		require.Equal(t, "gateway.courier_api", f.Hop)
	case <-time.After(5 * time.Second):
		t.Fatal("no finding in 5 s")
	}
}
