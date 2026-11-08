package ingest_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	tracesvc "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/outbox"
	"github.com/raduolivas/dt/backend/pkg/testkit"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/config"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/ingest"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/normalize"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/topology"
)

func newStore(t *testing.T) *store.Store {
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
	st, err := store.Open(ctx, url)
	require.NoError(t, err)
	t.Cleanup(st.Close)
	return st
}

func newNorm(self *ingest.Self) *normalize.Normalizer {
	return &normalize.Normalizer{Topology: topology.Default(), Thresholds: config.Thresholds{Slow: time.Second}, OnRedacted: self.Redacted}
}

func newJS(t *testing.T) jetstream.JetStream {
	t.Helper()
	_, js := newNATS(t)
	return js
}

// newNATS starts NATS with DT's four streams and returns its URL and a client.
func newNATS(t *testing.T) (string, jetstream.JetStream) {
	t.Helper()
	url := testkit.StartNATS(t)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx := context.Background()
	for _, cfg := range []jetstream.StreamConfig{outbox.StreamAccounts, outbox.StreamDeliveries, outbox.StreamTracking, outbox.StreamGateway} {
		require.NoError(t, outbox.EnsureStream(ctx, js, cfg))
	}
	return url, js
}

func publish(t *testing.T, js jetstream.JetStream, eventType, delivery string) {
	t.Helper()
	env, err := outbox.NewEnvelope(eventType, "ten_1", eventsv1.Environment_ENVIRONMENT_TEST, "api",
		&eventsv1.Subject{DeliveryId: delivery},
		&eventsv1.DeliveryStatusChanged{DeliveryId: delivery, From: "a", To: "b"}, time.Now())
	require.NoError(t, err)
	b, err := proto.Marshal(env)
	require.NoError(t, err)
	_, err = js.Publish(context.Background(), outbox.SubjectFor(outbox.StreamDeliveries, eventType), b)
	require.NoError(t, err)
}

func TestCatchUpFromStreamHistory(t *testing.T) {
	js, st := newJS(t), newStore(t)
	for _, d := range []string{"del_1", "del_2", "del_3"} {
		publish(t, js, "delivery.status_changed", d)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	self := ingest.NewSelf(st, nil)
	go func() {
		defer close(done)
		_ = ingest.RunNATS(ctx, js, st, newNorm(self), ingest.NATSOptions{Self: self})
	}()
	require.Eventually(t, func() bool { n, _ := st.CountEvents(ctx); return n == 3 }, 15*time.Second, 100*time.Millisecond)
	cancel()
	<-done
}

func TestDoesNotDisturbDTConsumers(t *testing.T) {
	js, st := newJS(t), newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	dt, err := js.CreateConsumer(ctx, "DELIVERIES", jetstream.ConsumerConfig{Durable: "tracking-deliveries", AckPolicy: jetstream.AckExplicitPolicy})
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		publish(t, js, "delivery.status_changed", "del_x")
	}
	self := ingest.NewSelf(st, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = ingest.RunNATS(ctx, js, st, newNorm(self), ingest.NATSOptions{Self: self})
	}()
	require.Eventually(t, func() bool { n, _ := st.CountEvents(ctx); return n == 5 }, 15*time.Second, 100*time.Millisecond)
	cancel()
	<-done
	info, err := dt.Info(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 5, info.NumPending)
	require.Zero(t, info.NumAckPending)
	mc, err := js.Consumer(context.Background(), "DELIVERIES", "mc-deliveries")
	require.NoError(t, err)
	mi, err := mc.Info(context.Background())
	require.NoError(t, err)
	require.Equal(t, jetstream.AckExplicitPolicy, mi.Config.AckPolicy)
	require.Equal(t, jetstream.DeliverAllPolicy, mi.Config.DeliverPolicy)
	ai, err := js.Consumer(context.Background(), "ACCOUNTS", "mc-accounts")
	require.NoError(t, err)
	acc, _ := ai.Info(context.Background())
	require.Equal(t, jetstream.DeliverByStartTimePolicy, acc.Config.DeliverPolicy)
}

func TestInvalidEnvelopeIsAckedAndCounted(t *testing.T) {
	js, st := newJS(t), newStore(t)
	_, err := js.Publish(context.Background(), "deliveries.junk", []byte("not an envelope \xff\xff"))
	require.NoError(t, err)
	publish(t, js, "delivery.status_changed", "del_1")
	ctx, cancel := context.WithCancel(context.Background())
	self := ingest.NewSelf(st, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = ingest.RunNATS(ctx, js, st, newNorm(self), ingest.NATSOptions{Self: self})
	}()
	require.Eventually(t, func() bool { n, _ := st.CountEvents(ctx); return n == 1 }, 15*time.Second, 100*time.Millisecond)
	require.Eventually(t, func() bool {
		c, _ := js.Consumer(context.Background(), "DELIVERIES", "mc-deliveries")
		i, _ := c.Info(context.Background())
		return i != nil && i.NumPending == 0 && i.NumAckPending == 0
	}, 10*time.Second, 100*time.Millisecond)
	require.EqualValues(t, 1, self.Snapshot()["nats.DELIVERIES"].Drops)
	cancel()
	<-done
}

func spanRequest(n int) []byte {
	var spans []*tracepb.Span
	for i := 0; i < n; i++ {
		spans = append(spans, &tracepb.Span{
			TraceId: bytes.Repeat([]byte{1}, 16), SpanId: bytes.Repeat([]byte{byte(i + 1)}, 8),
			Name: "GET /x", Kind: tracepb.Span_SPAN_KIND_SERVER,
			StartTimeUnixNano: uint64(time.Now().UnixNano()), EndTimeUnixNano: uint64(time.Now().UnixNano()) + 1000,
		})
	}
	b, _ := proto.Marshal(&tracesvc.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{{Key: "service.name",
			Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "gateway"}}}}},
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: spans}},
	}}})
	return b
}

func TestOTLPHandlerAlways200AndCountsDrops(t *testing.T) {
	self := ingest.NewSelf(nil, nil)
	w := ingest.NewWriter(nil, self, 1) // not running: the buffer stays full
	h := ingest.OTLPHandler(w, newNorm(self), self, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(spanRequest(3)))
	req.Header.Set("Content-Type", "application/x-protobuf")
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.GreaterOrEqual(t, self.Snapshot()[ingest.SourceOTLP].Drops, int64(1))

	// Garbage still gets 200 (and is counted), so the collector never retries.
	before := self.Snapshot()[ingest.SourceOTLP].Drops
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/logs", bytes.NewReader([]byte("garbage\xff"))))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, before+1, self.Snapshot()[ingest.SourceOTLP].Drops)
}

func TestOTLPGzipAndBodyCap(t *testing.T) {
	self := ingest.NewSelf(nil, nil)
	w := ingest.NewWriter(nil, self, 100)
	h := ingest.OTLPHandler(w, newNorm(self), self, nil)

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(spanRequest(2))
	require.NoError(t, zw.Close())
	req := httptest.NewRequest(http.MethodPost, "/v1/traces", &gz)
	req.Header.Set("Content-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Zero(t, self.Snapshot()[ingest.SourceOTLP].Drops)
	require.Equal(t, 2, queued(w))

	// A gzip bomb above 4 MiB decoded is rejected (counted), still 200.
	var big bytes.Buffer
	zw = gzip.NewWriter(&big)
	_, _ = zw.Write(make([]byte, ingest.MaxOTLPBody+10))
	require.NoError(t, zw.Close())
	req = httptest.NewRequest(http.MethodPost, "/v1/traces", &big)
	req.Header.Set("Content-Encoding", "gzip")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.EqualValues(t, 1, self.Snapshot()[ingest.SourceOTLP].Drops)
}

func queued(w *ingest.Writer) int { return w.Len() }

func TestHealthPollEmitsOnChangeOnly(t *testing.T) {
	var ready atomic.Bool
	ready.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" && !ready.Load() {
			rw.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		rw.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	events := make(chan store.Event, 20)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ingest.PollHealth(ctx, map[string]string{"tracking": srv.URL}, 20*time.Millisecond, func(e store.Event) { events <- e })

	first := <-events
	require.Equal(t, "health", first.Kind)
	require.Equal(t, "ok", first.Outcome)
	time.Sleep(200 * time.Millisecond) // many polls, no change
	require.Empty(t, events)
	ready.Store(false)
	second := <-events
	require.Equal(t, "failed", second.Outcome)
	require.Equal(t, "not_ready", second.Reason)
	require.JSONEq(t, `{"live":true,"ready":false}`, string(second.Attrs))
	time.Sleep(200 * time.Millisecond)
	require.Empty(t, events)
}

func TestPollConsumersEmitsPendingChanges(t *testing.T) {
	var pending atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/jsz", r.URL.Path)
		_, _ = rw.Write([]byte(`{"account_details":[{"stream_detail":[{"name":"DELIVERIES","consumer_detail":[{"name":"tracking-deliveries","num_pending":` +
			itoa(pending.Load()) + `,"num_ack_pending":0}]}]}]}`))
	}))
	defer srv.Close()
	events := make(chan store.Event, 20)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ingest.PollConsumers(ctx, srv.URL, 20*time.Millisecond, func(e store.Event) { events <- e })
	var got []store.Event
	for len(got) < 2 { // monitor up + consumer
		got = append(got, <-events)
	}
	pending.Store(7)
	e := <-events
	require.Equal(t, "nats.DELIVERIES", e.Source)
	require.JSONEq(t, `{"consumer":"tracking-deliveries","pending":7,"ack_pending":0}`, string(e.Attrs))
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestSelfFlushAndStartupDowntime(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	require.NoError(t, st.Heartbeat(ctx, time.Now().Add(-time.Minute)))
	self := ingest.NewSelf(st, nil)
	self.Drop("otlp", 2)
	self.Redacted(3)
	self.StorageError("nats.TRACKING")
	self.Lag("nats.TRACKING", 1500*time.Millisecond)
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); _ = self.Run(rctx) }()
	require.Eventually(t, func() bool { n, _ := st.CountDowntime(ctx); return n == 1 }, 5*time.Second, 50*time.Millisecond)
	require.Eventually(t, func() bool {
		rows, _ := st.SelfRows(ctx)
		return len(rows) >= 3
	}, 5*time.Second, 50*time.Millisecond)
	cancel()
	<-done
	rows, err := st.SelfRows(ctx)
	require.NoError(t, err)
	by := map[string]store.SelfRow{}
	for _, r := range rows {
		by[r.Source] = r
	}
	require.EqualValues(t, 2, by["otlp"].Drops)
	require.EqualValues(t, 3, by[ingest.SourceRedacted].Redacted)
	require.EqualValues(t, 1, by["nats.TRACKING"].StorageErrors)
	require.EqualValues(t, 1500, by["nats.TRACKING"].LagMS)
}
