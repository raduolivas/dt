package outbox_test

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/outbox"
	"github.com/raduolivas/dt/backend/pkg/testkit"
)

// starting limits concurrent container starts: rootless Docker's port
// forwarder fails with "address already in use" when many start at once.
var starting = make(chan struct{}, 2)

func startContainers(t *testing.T) (*testkit.Postgres, string) {
	t.Helper()
	starting <- struct{}{}
	defer func() { <-starting }()
	return testkit.StartPostgres(t), testkit.StartNATS(t)
}

// env holds the real dependencies of one test.
type env struct {
	pool *pgxpool.Pool
	js   jetstream.JetStream
	log  *slog.Logger
}

// setup starts Postgres and NATS, creates the outbox and inbox tables (and
// the handlers' applied table) in the gateway schema as mig_gateway, as a
// migration would, and creates the ACCOUNTS stream. The returned pool
// connects as svc_gateway.
func setup(t *testing.T) *env {
	t.Helper()
	pg, nurl := startContainers(t)
	ctx := context.Background()
	mig, err := pgx.Connect(ctx, pg.MigratorURL("gateway"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = mig.Exec(ctx, outbox.SchemaSQL+"; CREATE TABLE applied (event_id text)")
	_ = mig.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, pg.URL("gateway"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	nc, err := nats.Connect(nurl)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	if err := outbox.EnsureStream(ctx, js, outbox.StreamAccounts); err != nil {
		t.Fatal(err)
	}
	// EnsureStream is idempotent.
	if err := outbox.EnsureStream(ctx, js, outbox.StreamAccounts); err != nil {
		t.Fatal(err)
	}
	return &env{pool: pool, js: js, log: slog.New(slog.DiscardHandler)}
}

func newEnvelope(t *testing.T) *eventsv1.Envelope {
	t.Helper()
	e, err := outbox.NewEnvelope("apikey.revoked", "ten_01", eventsv1.Environment_ENVIRONMENT_TEST,
		"accounts", nil, &eventsv1.ApiKeyRevoked{KeyId: "ak_01"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return e
}

var revokedSubject = outbox.SubjectFor(outbox.StreamAccounts, "apikey.revoked")

// enqueue writes e to the outbox and commits (or rolls back).
func (e *env) enqueue(t *testing.T, ctx context.Context, ev *eventsv1.Envelope, commit bool) {
	t.Helper()
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := outbox.Enqueue(ctx, tx, revokedSubject, ev); err != nil {
		t.Fatal(err)
	}
	if commit {
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

func (e *env) relay(js jetstream.JetStream) *outbox.Relay {
	return &outbox.Relay{Pool: e.pool, JS: js, Log: e.log, Interval: 50 * time.Millisecond, Batch: 100}
}

// runFor runs the workers until d has passed and fails if one returns an error.
func runFor(t *testing.T, d time.Duration, workers ...func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	errs := make(chan error, len(workers))
	for _, w := range workers {
		go func() { errs <- w(ctx) }()
	}
	for range workers {
		if err := <-errs; err != nil {
			t.Fatalf("worker returned %v", err)
		}
	}
}

func (e *env) streamMsgs(t *testing.T) uint64 {
	t.Helper()
	s, err := e.js.Stream(context.Background(), outbox.StreamAccounts.Name)
	if err != nil {
		t.Fatal(err)
	}
	info, err := s.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return info.State.Msgs
}

func (e *env) publishedAt(t *testing.T, eventID string) *time.Time {
	t.Helper()
	var at *time.Time
	if err := e.pool.QueryRow(context.Background(),
		`SELECT published_at FROM outbox WHERE event_id = $1`, eventID).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at
}

func TestEnqueueRolledBackIsNeverPublished(t *testing.T) {
	t.Parallel()
	e := setup(t)
	e.enqueue(t, context.Background(), newEnvelope(t), false)
	runFor(t, time.Second, e.relay(e.js).Run)
	if n := e.streamMsgs(t); n != 0 {
		t.Fatalf("stream has %d messages, want 0", n)
	}
}

func TestRelayPublishesOnceWithMsgID(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ev := newEnvelope(t)
	e.enqueue(t, context.Background(), ev, true)
	runFor(t, time.Second, e.relay(e.js).Run, e.relay(e.js).Run)

	if n := e.streamMsgs(t); n != 1 {
		t.Fatalf("stream has %d messages, want 1", n)
	}
	s, err := e.js.Stream(context.Background(), outbox.StreamAccounts.Name)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := s.GetLastMsgForSubject(context.Background(), revokedSubject)
	if err != nil {
		t.Fatal(err)
	}
	if got := msg.Header.Get(jetstream.MsgIDHeader); got != ev.GetEventId() {
		t.Errorf("Nats-Msg-Id = %q, want %q", got, ev.GetEventId())
	}
	if e.publishedAt(t, ev.GetEventId()) == nil {
		t.Error("published_at is not set")
	}
}

func TestRelayPublishesInCreationOrder(t *testing.T) {
	t.Parallel()
	e := setup(t)
	var want []string
	for range 5 {
		ev := newEnvelope(t)
		want = append(want, ev.GetEventId())
		e.enqueue(t, context.Background(), ev, true)
	}
	// Batch 2 makes the relay take several ticks.
	r := e.relay(e.js)
	r.Batch = 2
	runFor(t, time.Second, r.Run)
	s, err := e.js.Stream(context.Background(), outbox.StreamAccounts.Name)
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range want {
		msg, err := s.GetMsg(context.Background(), uint64(i+1))
		if err != nil {
			t.Fatal(err)
		}
		if got := msg.Header.Get(jetstream.MsgIDHeader); got != id {
			t.Errorf("message %d is %q, want %q", i+1, got, id)
		}
	}
}

// flakyJS fails every publish while failing is set, like a NATS outage.
type flakyJS struct {
	jetstream.JetStream
	failing atomic.Bool
	calls   atomic.Int32
}

func (f *flakyJS) PublishMsg(ctx context.Context, m *nats.Msg, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	f.calls.Add(1)
	if f.failing.Load() {
		return nil, errors.New("nats: no responders available for request")
	}
	return f.JetStream.PublishMsg(ctx, m, opts...)
}

func TestRelayRetriesAfterNATSOutage(t *testing.T) {
	t.Parallel()
	e := setup(t)
	js := &flakyJS{JetStream: e.js}
	js.failing.Store(true)
	ev := newEnvelope(t)
	e.enqueue(t, context.Background(), ev, true)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.relay(js).Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("relay returned %v", err)
		}
	})

	time.Sleep(time.Second)
	if js.calls.Load() < 2 {
		t.Fatalf("relay tried %d times during the outage, want retries", js.calls.Load())
	}
	if e.publishedAt(t, ev.GetEventId()) != nil || e.streamMsgs(t) != 0 {
		t.Fatal("row was marked published during the outage")
	}

	js.failing.Store(false)
	deadline := time.Now().Add(3 * time.Second)
	for e.publishedAt(t, ev.GetEventId()) == nil {
		if time.Now().After(deadline) {
			t.Fatal("not published within 3 s after NATS came back")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n := e.streamMsgs(t); n != 1 {
		t.Fatalf("stream has %d messages, want 1", n)
	}
}

func TestRelayDeletesOldPublishedRows(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	old, fresh, pending := newEnvelope(t), newEnvelope(t), newEnvelope(t)
	for _, ev := range []*eventsv1.Envelope{old, fresh} {
		e.enqueue(t, ctx, ev, true)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE outbox SET published_at = now() - interval '8 days' WHERE event_id = $1`,
		old.GetEventId()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE outbox SET published_at = now() - interval '6 days' WHERE event_id = $1`,
		fresh.GetEventId()); err != nil {
		t.Fatal(err)
	}
	// The pending row stays even though NATS rejects it.
	e.enqueue(t, ctx, pending, true)
	js := &flakyJS{JetStream: e.js}
	js.failing.Store(true)
	runFor(t, 500*time.Millisecond, e.relay(js).Run)

	rows, err := e.pool.Query(ctx, `SELECT event_id FROM outbox ORDER BY event_id`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != fresh.GetEventId() || got[1] != pending.GetEventId() {
		t.Fatalf("rows = %v, want [%s %s]", got, fresh.GetEventId(), pending.GetEventId())
	}
}

// useSpanRecorder installs a global tracer provider with a span recorder and
// the W3C propagator. Tests using it are not parallel: they swap globals.
func useSpanRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	prevProp, prevTP := otel.GetTextMapPropagator(), otel.GetTracerProvider()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})
	return rec
}

func endedSpan(rec *tracetest.SpanRecorder, name string) sdktrace.ReadOnlySpan {
	for _, s := range rec.Ended() {
		if s.Name() == name {
			return s
		}
	}
	return nil
}

func TestRelayPublishSpanParentsConsumer(t *testing.T) {
	rec := useSpanRecorder(t)
	e := setup(t)
	ctx, root := otel.Tracer("test").Start(context.Background(), "create key")
	ev := newEnvelope(t)
	e.enqueue(t, ctx, ev, true)
	root.End()

	rctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.relay(e.js).Run(rctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("relay returned %v", err)
		}
	})
	r := &recorder{}
	e.consume(t, r.handle)
	waitFor(t, 5*time.Second, "the event", func() bool { return r.count() == 1 })

	subject := revokedSubject
	var pub, con sdktrace.ReadOnlySpan
	waitFor(t, 5*time.Second, "publish and consume spans", func() bool {
		pub, con = endedSpan(rec, "publish "+subject), endedSpan(rec, "consume "+subject)
		return pub != nil && con != nil
	})
	if pub.SpanKind() != trace.SpanKindProducer {
		t.Errorf("publish kind = %v, want producer", pub.SpanKind())
	}
	if pub.Parent().SpanID() != root.SpanContext().SpanID() {
		t.Errorf("publish parent = %s, want %s", pub.Parent().SpanID(), root.SpanContext().SpanID())
	}
	if con.Parent().SpanID() != pub.SpanContext().SpanID() {
		t.Errorf("consume parent = %s, want publish span %s", con.Parent().SpanID(), pub.SpanContext().SpanID())
	}
	tid := root.SpanContext().TraceID()
	if pub.SpanContext().TraceID() != tid || con.SpanContext().TraceID() != tid {
		t.Errorf("trace ids differ: root %s publish %s consume %s", tid, pub.SpanContext().TraceID(), con.SpanContext().TraceID())
	}
	want := map[string]string{
		"messaging.system":           "nats",
		"messaging.destination.name": subject,
		"messaging.message.id":       ev.GetEventId(),
		"dt.event_id":                ev.GetEventId(),
	}
	got := map[string]string{}
	for _, a := range pub.Attributes() {
		got[string(a.Key)] = a.Value.AsString()
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("attribute %s = %q, want %q", k, got[k], v)
		}
	}
}

func TestRelayPublishSpanErrorOnFailure(t *testing.T) {
	rec := useSpanRecorder(t)
	e := setup(t)
	js := &flakyJS{JetStream: e.js}
	js.failing.Store(true)
	e.enqueue(t, context.Background(), newEnvelope(t), true)

	runFor(t, 400*time.Millisecond, e.relay(js).Run)

	pub := endedSpan(rec, "publish "+revokedSubject)
	if pub == nil {
		t.Fatal("no publish span recorded")
	}
	if pub.Status().Code != codes.Error {
		t.Errorf("status = %v, want Error", pub.Status().Code)
	}
	var recorded bool
	for _, ev := range pub.Events() {
		recorded = recorded || ev.Name == "exception"
	}
	if !recorded {
		t.Error("publish span did not record the error")
	}
}
