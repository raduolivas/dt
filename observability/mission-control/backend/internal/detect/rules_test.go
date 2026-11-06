package detect

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	mcconfig "github.com/raduolivas/dt/observability/mission-control/backend/config"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/config"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
)

var t0 = time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC) // the fake clock

func defaults(t *testing.T) config.Thresholds {
	t.Helper()
	var th config.Thresholds
	require.NoError(t, yaml.Unmarshal(mcconfig.DefaultThresholds, &th))
	return th
}

// mk builds a stored event as the normalizer would.
func mk(id int64, kind, source, target, outcome, reason, delivery string, at time.Time, attrs string) ev {
	e := store.Event{ID: id, ObservedAt: at, OccurredAt: at, Kind: kind, Source: source, Target: target,
		Outcome: outcome, Reason: reason, DeliveryID: delivery, TenantID: "t1", Env: "test", Origin: "live"}
	if attrs != "" {
		e.Attrs = []byte(attrs)
	}
	return wrap([]store.Event{e})[0]
}

func newIn(t *testing.T, win ...ev) *input {
	return &input{now: t0, th: defaults(t), win: win, status: map[string]ev{}, fixes: map[string]store.Fix{},
		dropAt: map[string]time.Time{}}
}

func one(t *testing.T, fs []Finding) Finding {
	t.Helper()
	require.Len(t, fs, 1, fmt.Sprintf("%+v", fs))
	return fs[0]
}

func status(id int64, delivery, to string, at time.Time) ev {
	return mk(id, "message_published", "deliveries", "nats.DELIVERIES", "ok", "", delivery, at,
		fmt.Sprintf(`{"event_type":"delivery.status_changed","to":%q}`, to))
}

// courier_stale

func TestCourierStaleFiresAmberThenRed(t *testing.T) {
	in := newIn(t)
	in.status["d1"] = status(1, "d1", "picked_up", t0.Add(-10*time.Minute))
	in.fixes["d1"] = store.Fix{EventID: 7, OccurredAt: t0.Add(-40 * time.Second)}
	f := one(t, courierStale(in))
	require.Equal(t, "courier_stale", f.Detector)
	require.Equal(t, "courier", f.Hop)
	require.Equal(t, "d1", f.DeliveryID)
	require.Equal(t, SevWarning, f.Severity)
	require.Equal(t, []int64{7}, f.Evidence)
	require.Equal(t, t0.Add(-40*time.Second), f.Since)

	in.fixes["d1"] = store.Fix{EventID: 7, OccurredAt: t0.Add(-6 * time.Minute)}
	require.Equal(t, SevFailure, one(t, courierStale(in)).Severity)
}

func TestCourierStaleQuietWhenFixIsFreshOrDeliveryEnded(t *testing.T) {
	in := newIn(t)
	in.status["d1"] = status(1, "d1", "picked_up", t0.Add(-10*time.Minute))
	in.fixes["d1"] = store.Fix{EventID: 7, OccurredAt: t0.Add(-10 * time.Second)}
	require.Empty(t, courierStale(in))

	in.fixes["d1"] = store.Fix{EventID: 7, OccurredAt: t0.Add(-10 * time.Minute)}
	in.status["d1"] = status(2, "d1", "delivered", t0.Add(-9*time.Minute))
	require.Empty(t, courierStale(in), "a delivered order is not active")
	in.status["d1"] = status(3, "d1", "confirmed", t0.Add(-9*time.Minute))
	require.Empty(t, courierStale(in), "no courier yet: not active")
}

func TestCourierStaleWithoutAnyFixCountsFromTheStatusChange(t *testing.T) {
	in := newIn(t)
	in.status["d1"] = status(5, "d1", "courier_assigned", t0.Add(-45*time.Second))
	f := one(t, courierStale(in))
	require.Equal(t, []int64{5}, f.Evidence)
	require.Equal(t, SevWarning, f.Severity)
}

// fixes_dropped

func dropped(id int64, delivery, reason string, ago time.Duration) ev {
	return mk(id, "log", "tracking.ingest", "tracking.ingest", "dropped", reason, delivery, t0.Add(-ago), `{"event":"fix_dropped"}`)
}

func TestFixesDroppedGroupedByReason(t *testing.T) {
	in := newIn(t, dropped(1, "d1", "delivery_reassigned", 5*time.Second), dropped(2, "d1", "delivery_reassigned", 3*time.Second),
		dropped(3, "d1", "speed", time.Second))
	fs := fixesDropped(in)
	require.Len(t, fs, 2)
	by := map[string]Finding{}
	for _, f := range fs {
		by[f.Reason] = f
	}
	r := by["delivery_reassigned"]
	require.Equal(t, "tracking.ingest", r.Hop)
	require.Equal(t, "d1", r.DeliveryID)
	require.Equal(t, SevWarning, r.Severity)
	require.Equal(t, []int64{1, 2}, r.Evidence)
	require.Equal(t, t0.Add(-5*time.Second), r.Since)
}

func TestFixesDroppedStateUpdateFailedIsFailureAndOldDropsAreQuiet(t *testing.T) {
	in := newIn(t, dropped(1, "d1", "state_update_failed", time.Second))
	require.Equal(t, SevFailure, one(t, fixesDropped(in)).Severity)
	require.Empty(t, fixesDropped(newIn(t, dropped(1, "d1", "speed", 2*time.Minute))), "outside the window")
	require.Empty(t, fixesDropped(newIn(t, mk(1, "log", "tracking.ingest", "tracking.ingest", "failed", "x", "d1", t0, ""))))
}

// rejected_at_gateway

func TestRejectedAtGatewayNamesTheRouteAndReason(t *testing.T) {
	in := newIn(t,
		mk(1, "request", "gateway.courier_api", "gateway.courier_api", "rejected", "unauthorized", "", t0.Add(-2*time.Second), `{"http_status":401}`),
		mk(2, "request", "gateway.courier_api", "gateway.courier_api", "rejected", "unauthorized", "", t0.Add(-time.Second), `{"http_status":401}`),
		mk(3, "request", "gateway.partner_api", "gateway.partner_api", "rejected", "rate_limited", "", t0.Add(-time.Second), `{"http_status":429}`),
		mk(4, "request", "gateway.partner_api", "gateway.partner_api", "ok", "", "", t0.Add(-time.Second), `{"http_status":422}`),
	)
	fs := rejectedAtGateway(in)
	require.Len(t, fs, 3)
	got := map[string]Finding{}
	for _, f := range fs {
		got[f.Hop+"/"+f.Reason] = f
	}
	require.Equal(t, []int64{1, 2}, got["gateway.courier_api/unauthorized"].Evidence)
	require.Contains(t, got, "gateway.partner_api/rate_limited")
	require.Contains(t, got, "gateway.partner_api/client_error")
}

func TestRejectedAtGatewayQuiet(t *testing.T) {
	in := newIn(t,
		mk(1, "request", "deliveries.rpc", "deliveries.rpc", "rejected", "unauthorized", "", t0, `{"http_status":401}`),
		mk(2, "request", "gateway.partner_api", "gateway.partner_api", "ok", "", "", t0, `{"http_status":404}`),
		mk(3, "request", "gateway.partner_api", "gateway.partner_api", "ok", "", "", t0, `{"http_status":200}`),
		mk(4, "request", "gateway.courier_api", "gateway.courier_api", "rejected", "unauthorized", "", t0.Add(-3*time.Minute), `{"http_status":401}`))
	require.Empty(t, rejectedAtGateway(in))
}

// stuck_status

func TestStuckStatus(t *testing.T) {
	in := newIn(t)
	in.status["d1"] = status(1, "d1", "at_pickup", t0.Add(-11*time.Minute))
	f := one(t, stuckStatus(in))
	require.Equal(t, "stuck_status", f.Detector)
	require.Equal(t, "stuck_at_pickup", f.Reason)
	require.Equal(t, "deliveries.state_machine", f.Hop)
	require.Equal(t, []int64{1}, f.Evidence)
	require.Equal(t, t0.Add(-11*time.Minute+10*time.Minute), f.Since, "since = when the limit was passed")

	in.status["d1"] = status(1, "d1", "at_pickup", t0.Add(-9*time.Minute))
	require.Empty(t, stuckStatus(in))
	in.status["d1"] = status(2, "d1", "delivered", t0.Add(-48*time.Hour))
	require.Empty(t, stuckStatus(in), "final statuses have no limit")
}

// processed_not_published

func applied(id int64, trace string, ago time.Duration, extra string) ev {
	return mk(id, "state_change", "deliveries.state_machine", "deliveries.state_machine", "ok", "", "d1", t0.Add(-ago),
		`{"span_name":"deliveries.assign"`+extra+`}`).withTrace(trace)
}

func (e ev) withTrace(tr string) ev { e.TraceID = tr; return e }

func publish(id int64, trace string) ev {
	return mk(id, "request", "deliveries.outbox", "nats.DELIVERIES", "ok", "", "", t0.Add(-time.Second),
		`{"span_name":"publish deliveries.delivery.courier_assigned"}`).withTrace(trace)
}

func TestProcessedNotPublishedFires(t *testing.T) {
	f := one(t, processedNotPublished(newIn(t, applied(1, "tr1", 45*time.Second, ""))))
	require.Equal(t, "deliveries.outbox", f.Hop)
	require.Equal(t, "d1", f.DeliveryID)
	require.Equal(t, "no_publish", f.Reason)
	require.Equal(t, []int64{1}, f.Evidence)
}

func TestProcessedNotPublishedQuiet(t *testing.T) {
	require.Empty(t, processedNotPublished(newIn(t, applied(1, "tr1", 45*time.Second, ""), publish(2, "tr1"))), "published")
	require.Empty(t, processedNotPublished(newIn(t, applied(1, "tr1", 20*time.Second, ""))), "not yet 30 s")
	require.Empty(t, processedNotPublished(newIn(t, applied(1, "tr1", 45*time.Second, `,"deliveries.outcome":"duplicate"`))), "no event was produced")
	require.Len(t, processedNotPublished(newIn(t, applied(1, "tr1", 45*time.Second, ""), publish(2, "other"))), 1,
		"a publish in another trace does not match")
}

// consumer_lagging

func cons(id int64, stream, name string, pending int, ago time.Duration) ev {
	return mk(id, "health", "nats."+stream, "", "ok", "", "", t0.Add(-ago),
		fmt.Sprintf(`{"consumer":%q,"pending":%d,"ack_pending":0}`, name, pending))
}

func TestConsumerLaggingFiresWhenNotShrinking(t *testing.T) {
	in := newIn(t)
	in.health = []ev{cons(1, "TRACKING", "tracking-cons", 5, 90*time.Second)}
	f := one(t, consumerLagging(in))
	require.Equal(t, "nats.TRACKING", f.Hop)
	require.Equal(t, "pending_not_shrinking", f.Reason)
	require.Equal(t, []int64{1}, f.Evidence)
	require.Equal(t, "tracking-cons", f.Attrs["consumer"])

	in.health = []ev{cons(1, "TRACKING", "tracking-cons", 5, 90*time.Second), cons(2, "TRACKING", "tracking-cons", 7, 30*time.Second)}
	one(t, consumerLagging(in)) // growing is not shrinking
}

func TestConsumerLaggingQuiet(t *testing.T) {
	in := newIn(t)
	in.health = []ev{cons(1, "TRACKING", "c", 5, 90*time.Second), cons(2, "TRACKING", "c", 3, 30*time.Second)}
	require.Empty(t, consumerLagging(in), "shrinking")
	in.health = []ev{cons(1, "TRACKING", "c", 0, 90*time.Second)}
	require.Empty(t, consumerLagging(in), "nothing pending")
	in.health = []ev{cons(1, "TRACKING", "c", 5, 30*time.Second)}
	require.Empty(t, consumerLagging(in), "pending for only 30 s")
	in.health = []ev{cons(1, "TRACKING", "mc-TRACKING", 500, 10*time.Minute)}
	require.Empty(t, consumerLagging(in), "Mission Control's own consumer is mc_behind's business")
	in.health = []ev{cons(1, "TRACKING", "c", 5, 90*time.Second), cons(2, "TRACKING", "c", 5, 80*time.Second), cons(3, "TRACKING", "c", 1, 10*time.Second)}
	require.Empty(t, consumerLagging(in), "it shrank inside the last minute")
}

// snapshot_not_delivered

func TestSnapshotNotDeliveredOnPublishFailure(t *testing.T) {
	in := newIn(t, mk(1, "log", "tracking.state", "tracking.state", "failed", "snapshot_publish_failed", "d1", t0.Add(-5*time.Second), ""))
	f := one(t, snapshotNotDelivered(in))
	require.Equal(t, "tracking.state", f.Hop)
	require.Equal(t, "snapshot_publish_failed", f.Reason)
	require.Equal(t, "d1", f.DeliveryID)
}

func TestSnapshotNotDeliveredZeroViewersWhileConnected(t *testing.T) {
	viewer := mk(1, "request", "tracking.hub", "tracking.hub", "ok", "", "d1", t0.Add(-5*time.Minute),
		`{"span_name":"GET /v1/tracking/stream","duration_ms":400000}`)
	fan := mk(2, "request", "tracking.hub", "tracking.hub", "ok", "", "d1", t0.Add(-10*time.Second),
		`{"span_name":"tracking.viewer_fanout","tracking.viewers":0}`)
	f := one(t, snapshotNotDelivered(newIn(t, viewer, fan)))
	require.Equal(t, "tracking.hub", f.Hop)
	require.Equal(t, "no_viewers_reached", f.Reason)
	require.Equal(t, []int64{1, 2}, f.Evidence)
}

func TestSnapshotNotDeliveredQuiet(t *testing.T) {
	require.Empty(t, snapshotNotDelivered(newIn(t)))
	fan := mk(2, "request", "tracking.hub", "tracking.hub", "ok", "", "d1", t0.Add(-10*time.Second),
		`{"span_name":"tracking.viewer_fanout","tracking.viewers":0}`)
	require.Empty(t, snapshotNotDelivered(newIn(t, fan)), "nobody was connected")
	left := mk(1, "request", "tracking.hub", "tracking.hub", "ok", "", "d1", t0.Add(-5*time.Minute),
		`{"span_name":"GET /v1/tracking/stream","duration_ms":60000}`)
	require.Empty(t, snapshotNotDelivered(newIn(t, left, fan)), "the viewer had left")
	fan2 := mk(3, "request", "tracking.hub", "tracking.hub", "ok", "", "d1", t0.Add(-10*time.Second),
		`{"span_name":"tracking.viewer_fanout","tracking.viewers":2}`)
	viewer := mk(1, "request", "tracking.hub", "tracking.hub", "ok", "", "d1", t0.Add(-5*time.Minute),
		`{"span_name":"GET /v1/tracking/stream","duration_ms":400000}`)
	require.Empty(t, snapshotNotDelivered(newIn(t, viewer, fan2)), "viewers received it")
}

// storm

func failures(n int, reason string) []ev {
	var out []ev
	for i := 0; i < n; i++ {
		out = append(out, mk(int64(i+1), "request", "tracking.state", "redis", "retried", reason, "", t0.Add(-time.Duration(i)*time.Second), ""))
	}
	return out
}

func TestStormMoreThanTenOfTheSameReasonOnOneWire(t *testing.T) {
	f := one(t, storm(newIn(t, failures(11, "timeout")...)))
	require.Equal(t, "tracking.state>redis", f.Hop)
	require.Equal(t, "timeout", f.Reason)
	require.Len(t, f.Evidence, 11)
}

func TestStormQuiet(t *testing.T) {
	require.Empty(t, storm(newIn(t, failures(10, "timeout")...)), "exactly 10 is not more than 10")
	mixed := append(failures(6, "a"), failures(6, "b")...)
	for i := range mixed {
		mixed[i].ID = int64(i + 1)
	}
	require.Empty(t, storm(newIn(t, mixed...)), "reasons are counted apart")
	old := failures(12, "timeout")
	for i := range old {
		old[i].OccurredAt = t0.Add(-5 * time.Minute)
		old[i].ObservedAt = t0.Add(-5 * time.Minute)
	}
	require.Empty(t, storm(newIn(t, old...)), "outside the minute")
}

// dependency_failing (ruling R15)

func failedTo(id int64, target string, ago time.Duration) ev {
	return mk(id, "request", "tracking.state", target, "failed", "", "", t0.Add(-ago), `{"span_name":"get"}`)
}

func TestDependencyFailingOnThreeFailedCalls(t *testing.T) {
	f := one(t, dependencyFailing(newIn(t, failedTo(1, "redis", 3*time.Second), failedTo(2, "redis", 2*time.Second), failedTo(3, "redis", time.Second))))
	require.Equal(t, "redis", f.Hop)
	require.Equal(t, SevFailure, f.Severity)
	require.Equal(t, []int64{1, 2, 3}, f.Evidence)
	require.Equal(t, t0.Add(-3*time.Second), f.Since)
	g := one(t, dependencyFailing(newIn(t, failedTo(1, "pg.tracking", 3*time.Second), failedTo(2, "pg.tracking", 2*time.Second), failedTo(3, "pg.tracking", time.Second))))
	require.Equal(t, "pg.tracking", g.Hop)
}

func TestDependencyFailingOsrmFromLogsBecauseTheClientIsNotTraced(t *testing.T) {
	lg := func(id int64, ago time.Duration) ev {
		return mk(id, "log", "tracking", "", "", "", "", t0.Add(-ago), `{"body":"route_fallback","severity":"WARN"}`)
	}
	f := one(t, dependencyFailing(newIn(t, lg(1, 3*time.Second), lg(2, 2*time.Second), lg(3, time.Second))))
	require.Equal(t, "osrm", f.Hop)
	require.Equal(t, "route_fallback", f.Reason)
	require.Equal(t, []int64{1, 2, 3}, f.Evidence)
}

func TestDependencyFailingFromHealth(t *testing.T) {
	in := newIn(t)
	in.health = []ev{mk(9, "health", "tracking", "", "failed", "not_ready", "", t0.Add(-time.Hour), `{"live":true,"ready":false,"dependency":"redis"}`)}
	f := one(t, dependencyFailing(in))
	require.Equal(t, "redis", f.Hop)
	require.Equal(t, []int64{9}, f.Evidence)
}

func TestDependencyFailingQuiet(t *testing.T) {
	require.Empty(t, dependencyFailing(newIn(t, failedTo(1, "redis", 3*time.Second), failedTo(2, "redis", 2*time.Second))), "two failures")
	require.Empty(t, dependencyFailing(newIn(t, failedTo(1, "deliveries.rpc", 3*time.Second), failedTo(2, "deliveries.rpc", 2*time.Second), failedTo(3, "deliveries.rpc", time.Second))), "not a dependency")
	require.Empty(t, dependencyFailing(newIn(t, failedTo(1, "redis", 5*time.Minute), failedTo(2, "redis", 5*time.Minute), failedTo(3, "redis", 5*time.Minute))), "outside the minute")
	in := newIn(t)
	in.health = []ev{mk(9, "health", "tracking", "", "ok", "", "", t0, `{"dependency":"redis"}`)}
	require.Empty(t, dependencyFailing(in))
}

// mc_behind

func TestMCBehind(t *testing.T) {
	in := newIn(t)
	in.self = []store.SelfRow{{Source: "nats.TRACKING", LagMS: 11000}}
	f := one(t, mcBehind(in))
	require.Equal(t, "mission-control", f.Hop)
	require.Equal(t, "ingest_lag", f.Reason)
	require.Equal(t, "nats.TRACKING", f.Attrs["source"])
	require.Empty(t, f.Evidence, "self-health counters are not mc_event rows")

	in.self = []store.SelfRow{{Source: "otlp", Drops: 4}}
	in.dropAt["otlp"] = t0.Add(-10 * time.Second)
	require.Equal(t, "events_dropped", one(t, mcBehind(in)).Reason)
}

func TestMCBehindQuiet(t *testing.T) {
	in := newIn(t)
	in.self = []store.SelfRow{{Source: "nats.TRACKING", LagMS: 5000}, {Source: "otlp", Drops: 4}}
	in.dropAt["otlp"] = t0.Add(-5 * time.Minute)
	require.Empty(t, mcBehind(in), "lag under 10 s; the drops stopped over a minute ago")
}
