package normalize

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func kvS(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}
func kvI(k string, v int64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: v}}}
}

var (
	traceID = []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	spanID  = []byte{1, 2, 3, 4, 5, 6, 7, 8}
)

func mkSpan(name string, kind tracepb.Span_SpanKind, dur time.Duration, attrs ...*commonpb.KeyValue) *tracepb.Span {
	start := uint64(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	return &tracepb.Span{TraceId: traceID, SpanId: spanID, Name: name, Kind: kind,
		StartTimeUnixNano: start, EndTimeUnixNano: start + uint64(dur), Attributes: attrs}
}

func TestSpanOutcomes(t *testing.T) {
	gw := map[string]string{"service.name": "gateway"}
	tr := map[string]string{"service.name": "tracking"}
	errSpan := mkSpan("POST /v1/deliveries", tracepb.Span_SPAN_KIND_SERVER, time.Millisecond)
	errSpan.Status = &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR, Message: "boom SECRET"}
	dropped := mkSpan("tracking.ingest", tracepb.Span_SPAN_KIND_INTERNAL, time.Millisecond)
	dropped.Events = []*tracepb.Span_Event{{Name: "fix_dropped", TimeUnixNano: 5, Attributes: []*commonpb.KeyValue{
		kvS("dt.delivery_id", "del_1"), kvS("reason", "delivery_reassigned"), kvI("count", 2)}}}

	cases := []struct {
		name         string
		res          map[string]string
		span         *tracepb.Span
		wantOutcome  string
		wantReason   string
		wantKind     string
		extraOutcome string
	}{
		{"error", gw, errSpan, "failed", "", "request", ""},
		{"401", gw, mkSpan("GET /v1/deliveries", tracepb.Span_SPAN_KIND_SERVER, time.Millisecond, kvI("http.response.status_code", 401)), "rejected", "unauthorized", "request", ""},
		{"403 legacy key", gw, mkSpan("GET /v1/deliveries", tracepb.Span_SPAN_KIND_SERVER, time.Millisecond, kvI("http.status_code", 403)), "rejected", "unauthorized", "request", ""},
		{"429", gw, mkSpan("GET /v1/deliveries", tracepb.Span_SPAN_KIND_SERVER, time.Millisecond, kvI("http.response.status_code", 429)), "rejected", "rate_limited", "request", ""},
		{"slow", gw, mkSpan("GET /v1/deliveries", tracepb.Span_SPAN_KIND_SERVER, 1500*time.Millisecond), "slow", "", "request", ""},
		{"ok", gw, mkSpan("GET /v1/deliveries", tracepb.Span_SPAN_KIND_SERVER, 10*time.Millisecond), "ok", "", "request", ""},
		{"fix_dropped", tr, dropped, "ok", "", "request", "dropped"},
	}
	n := newNormalizer()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			evs := n.FromSpan(c.res, c.span)
			require.Equal(t, c.wantOutcome, evs[0].Outcome)
			require.Equal(t, c.wantReason, evs[0].Reason)
			require.Equal(t, c.wantKind, evs[0].Kind)
			require.Equal(t, "0102030405060708090a0b0c0d0e0f10", evs[0].TraceID)
			require.NotContains(t, string(evs[0].Attrs), "SECRET")
			if c.extraOutcome != "" {
				require.Len(t, evs, 2)
				require.Equal(t, "log", evs[1].Kind)
				require.Equal(t, c.extraOutcome, evs[1].Outcome)
				require.Equal(t, "delivery_reassigned", evs[1].Reason)
				require.Equal(t, "del_1", evs[1].DeliveryID)
			}
		})
	}
}

func TestSpanKindsAndWires(t *testing.T) {
	n := newNormalizer()
	got := func(res string, s *tracepb.Span) (kind, src, tgt string) {
		e := n.FromSpan(map[string]string{"service.name": res}, s)[0]
		return e.Kind, e.Source, e.Target
	}
	k, s, tg := got("deliveries", mkSpan("consume deliveries.x", tracepb.Span_SPAN_KIND_CONSUMER, 1, kvS("messaging.destination.name", "tracking.tracking.arrival")))
	require.Equal(t, []string{"message_consumed", "nats.TRACKING", "deliveries.consumers"}, []string{k, s, tg})

	k, s, tg = got("tracking", mkSpan("publish tracking.tracking.eta_changed", tracepb.Span_SPAN_KIND_PRODUCER, 1))
	require.Equal(t, []string{"request", "tracking.outbox", "nats.TRACKING"}, []string{k, s, tg})

	k, s, tg = got("gateway", mkSpan("/dt.deliveries.v1.DeliveryService/Get", tracepb.Span_SPAN_KIND_CLIENT, 1))
	require.Equal(t, []string{"request", "gateway", "deliveries.rpc"}, []string{k, s, tg})

	k, s, tg = got("tracking", mkSpan("GET", tracepb.Span_SPAN_KIND_CLIENT, 1, kvS("db.system", "redis")))
	require.Equal(t, []string{"request", "tracking.state", "redis"}, []string{k, s, tg})

	k, s, tg = got("tracking", mkSpan("tracking.apply", tracepb.Span_SPAN_KIND_INTERNAL, 1))
	require.Equal(t, []string{"state_change", "tracking.state", "tracking.state"}, []string{k, s, tg})

	k, s, _ = got("deliveries", mkSpan("deliveries.assign", tracepb.Span_SPAN_KIND_INTERNAL, 1))
	require.Equal(t, "state_change", k)
	require.Equal(t, "deliveries.state_machine", s)
}

func TestSpanAttrsRedactedAndCounted(t *testing.T) {
	n := newNormalizer()
	var total int
	n.OnRedacted = func(d int) { total += d }
	sp := mkSpan("POST /v1/courier/location", tracepb.Span_SPAN_KIND_SERVER, 1,
		kvS("dt.tenant_id", "ten_1"), kvS("dt.environment", "test"), kvS("dt.delivery_id", "del_1"), kvS("dt.request_id", "req_1"),
		kvS("http.request.header.authorization", "Bearer x"), kvS("dropoff_lat", "1.5"), kvS("x", "dt_sk_test_abc"),
		kvS("db.statement", "select * from t where name='Ann'"))
	e := n.FromSpan(map[string]string{"service.name": "gateway"}, sp)[0]
	require.Equal(t, 3, total)
	require.Equal(t, "ten_1", e.TenantID)
	require.Equal(t, "test", e.Env)
	require.Equal(t, "del_1", e.DeliveryID)
	for _, bad := range []string{"Bearer", "1.5", "dt_sk_", "select", "authorization"} {
		require.NotContains(t, string(e.Attrs), bad)
	}
	require.Contains(t, string(e.Attrs), "req_1")
}

func TestStandardOTelKeysAreKeptAndNotCounted(t *testing.T) {
	n := newNormalizer()
	var total int
	n.OnRedacted = func(d int) { total += d }
	sp := mkSpan("consume tracking.tracking.eta_changed", tracepb.Span_SPAN_KIND_CONSUMER, 1,
		kvS("messaging.destination.name", "tracking.tracking.eta_changed"), kvS("server.address", "nats"),
		kvS("client.address", "203.0.113.9"), kvS("network.peer.address", "10.0.0.7"),
		kvS("url.full", "http://x/?a=b"), kvS("db.statement", "select 1"))
	e := n.FromSpan(map[string]string{"service.name": "deliveries"}, sp)[0]
	require.Equal(t, 0, total, "standard keys and expected skips are not redaction hits")
	require.Contains(t, string(e.Attrs), "messaging.destination.name")
	require.Contains(t, string(e.Attrs), "server.address")
	for _, bad := range []string{"client.address", "203.0.113.9", "network.peer.address", "10.0.0.7", "url.full", "select 1"} {
		require.NotContains(t, string(e.Attrs), bad)
	}
}

func TestCredentialKeysInStandardNamespacesAreDroppedAndCounted(t *testing.T) {
	n := newNormalizer()
	var total int
	n.OnRedacted = func(d int) { total += d }
	sp := mkSpan("GET /v1/x", tracepb.Span_SPAN_KIND_SERVER, 1,
		kvS("http.request.header.authorization", "Bearer abc"), kvS("http.route", "/v1/x"))
	e := n.FromSpan(map[string]string{"service.name": "gateway"}, sp)[0]
	require.Equal(t, 1, total)
	require.NotContains(t, string(e.Attrs), "authorization")
	require.Contains(t, string(e.Attrs), "http.route")
}

// M2: a DSN or connection string can carry user:pass@; neither is stored.
func TestConnectionStringsAndDSNNeverStored(t *testing.T) {
	n := newNormalizer()
	var total int
	n.OnRedacted = func(d int) { total += d }
	sp := mkSpan("SELECT", tracepb.Span_SPAN_KIND_CLIENT, 1,
		kvS("db.connection_string", "postgres://u:hunter2@db/x"),
		kvS("messaging.connection_string", "nats://u:hunter3@nats"),
		kvS("db.dsn", "postgres://u:hunter4@db/x"), kvS("db.system", "postgresql"))
	e := n.FromSpan(map[string]string{"service.name": "deliveries"}, sp)[0]
	for _, bad := range []string{"hunter2", "hunter3", "hunter4", "connection_string", "dsn"} {
		require.NotContains(t, string(e.Attrs), bad)
	}
	require.Contains(t, string(e.Attrs), "db.system")
	require.Equal(t, 1, total, "the dsn credential key is counted; connection strings are skipped like SQL text")
}

func TestIDsPreferDTKeys(t *testing.T) {
	for i := 0; i < 50; i++ {
		tn, env, d := ids(map[string]any{"tenant_id": "wrong", "dt.tenant_id": "right",
			"environment": "wrong", "dt.environment": "right", "delivery_id": "wrong", "dt.delivery_id": "right"})
		require.Equal(t, []string{"right", "right", "right"}, []string{tn, env, d})
	}
}

func TestLogKeepsTraceID(t *testing.T) {
	n := newNormalizer()
	var total int
	n.OnRedacted = func(d int) { total += d }
	rec := &logspb.LogRecord{
		TimeUnixNano: 42, TraceId: traceID, SpanId: spanID,
		SeverityNumber: logspb.SeverityNumber_SEVERITY_NUMBER_ERROR,
		Body:           &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "snapshot_publish_failed"}},
		Attributes:     []*commonpb.KeyValue{kvS("delivery_id", "del_1"), kvS("customer_name", "Ann"), kvS("reason", "nats_down")},
	}
	e := n.FromLog(map[string]string{"service.name": "tracking"}, rec)
	require.Equal(t, "0102030405060708090a0b0c0d0e0f10", e.TraceID)
	require.Equal(t, "0102030405060708", e.SpanID)
	require.Equal(t, "log", e.Kind)
	require.Equal(t, "tracking", e.Source)
	require.Equal(t, "failed", e.Outcome)
	require.Equal(t, "del_1", e.DeliveryID)
	require.Equal(t, "nats_down", e.Reason)
	require.Equal(t, 1, total)
	require.NotContains(t, string(e.Attrs), "Ann")
	require.Contains(t, string(e.Attrs), "snapshot_publish_failed")
	require.Equal(t, time.Unix(0, 42).UTC(), e.OccurredAt)
}

func TestNanoTimeClampsInsteadOfWrapping(t *testing.T) {
	require.Equal(t, time.Unix(0, 5).UTC(), nanoTime(5))
	require.False(t, nanoTime(1<<63+10).Before(time.Unix(0, 0)), "values past int64 must not wrap negative")
}
