package outbox_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/outbox"
)

const consumerName = "test-consumer"

func consumerConfig() jetstream.ConsumerConfig {
	return jetstream.ConsumerConfig{
		Durable:       consumerName,
		FilterSubject: "accounts.apikey.>",
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
		MaxAckPending: 1,
		AckWait:       time.Second,
	}
}

// recorder is a Handler that records the event ids it sees. It writes a row
// through tx so tests can tell committed work from rolled-back work.
type recorder struct {
	mu    sync.Mutex
	calls []string
	ctxs  []context.Context
	fail  func(call int) error
}

func (r *recorder) handle(ctx context.Context, tx pgx.Tx, env *eventsv1.Envelope) error {
	r.mu.Lock()
	r.calls = append(r.calls, env.GetEventId())
	r.ctxs = append(r.ctxs, ctx)
	n := len(r.calls)
	r.mu.Unlock()
	if _, err := tx.Exec(ctx, `INSERT INTO applied (event_id) VALUES ($1)`, env.GetEventId()); err != nil {
		return err
	}
	if r.fail != nil {
		return r.fail(n)
	}
	return nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// consume runs Consume until the test ends and checks it stops cleanly.
func (e *env) consume(t *testing.T, h outbox.Handler) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- outbox.Consume(ctx, e.js, e.pool, e.log, outbox.StreamAccounts.Name, consumerConfig(), h)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Consume returned %v, want nil after cancel", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Consume did not stop after cancel")
		}
	})
	waitFor(t, 5*time.Second, "the consumer", func() bool {
		_, err := e.js.Consumer(context.Background(), outbox.StreamAccounts.Name, consumerName)
		return err == nil
	})
}

func (e *env) publish(t *testing.T, data []byte, msgID string) {
	t.Helper()
	msg := nats.NewMsg(revokedSubject)
	msg.Data = data
	if _, err := e.js.PublishMsg(context.Background(), msg, jetstream.WithMsgID(msgID)); err != nil {
		t.Fatal(err)
	}
}

func (e *env) count(t *testing.T, query string) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", d, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (e *env) consumerInfo(t *testing.T) *jetstream.ConsumerInfo {
	t.Helper()
	c, err := e.js.Consumer(context.Background(), outbox.StreamAccounts.Name, consumerName)
	if err != nil {
		t.Fatal(err)
	}
	info, err := c.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func marshal(t *testing.T, ev *eventsv1.Envelope) []byte {
	t.Helper()
	b, err := proto.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestConsumeIsIdempotent(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ev := newEnvelope(t)
	b := marshal(t, ev)
	e.publish(t, b, "first")
	e.publish(t, b, "second")
	if n := e.streamMsgs(t); n != 2 {
		t.Fatalf("stream has %d messages, want 2 (distinct msg ids)", n)
	}
	r := &recorder{}
	e.consume(t, r.handle)

	waitFor(t, 5*time.Second, "both messages acked", func() bool {
		info := e.consumerInfo(t)
		return info.AckFloor.Stream == 2 && info.NumAckPending == 0
	})
	if n := r.count(); n != 1 {
		t.Fatalf("handler ran %d times, want 1", n)
	}
	if n := e.count(t, `SELECT count(*) FROM inbox`); n != 1 {
		t.Fatalf("inbox has %d rows, want 1", n)
	}
	if n := e.count(t, `SELECT count(*) FROM inbox WHERE consumer = '`+consumerName+`'`); n != 1 {
		t.Fatalf("inbox row is not keyed by the consumer name")
	}
}

func TestConsumeNaksOnHandlerError(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ev := newEnvelope(t)
	e.publish(t, marshal(t, ev), ev.GetEventId())
	r := &recorder{fail: func(call int) error {
		if call == 1 {
			return errors.New("transient")
		}
		return nil
	}}
	start := time.Now()
	e.consume(t, r.handle)

	waitFor(t, 6*time.Second, "the retry", func() bool { return r.count() == 2 })
	if d := time.Since(start); d < 2*time.Second {
		t.Errorf("retried after %v, want a 2 s nak delay", d)
	}
	waitFor(t, 3*time.Second, "the ack", func() bool { return e.consumerInfo(t).NumAckPending == 0 })
	if n := e.count(t, `SELECT count(*) FROM inbox`); n != 1 {
		t.Fatalf("inbox has %d rows, want 1", n)
	}
	// The failed attempt's writes were rolled back with its inbox row.
	if n := e.count(t, `SELECT count(*) FROM applied`); n != 1 {
		t.Fatalf("applied has %d rows, want 1", n)
	}
	if n := r.count(); n != 2 {
		t.Fatalf("handler ran %d times, want 2", n)
	}
}

func TestConsumeTermsGarbage(t *testing.T) {
	t.Parallel()
	e := setup(t)
	e.publish(t, []byte("this is not a protobuf envelope"), "garbage")
	ev := newEnvelope(t)
	e.publish(t, marshal(t, ev), ev.GetEventId())
	r := &recorder{}
	e.consume(t, r.handle)

	// MaxAckPending is 1, so the valid event is only delivered once the
	// garbage has been settled. Unsettled, it would come back after AckWait.
	waitFor(t, 5*time.Second, "the valid event", func() bool { return r.count() == 1 })
	time.Sleep(1500 * time.Millisecond) // longer than AckWait
	info := e.consumerInfo(t)
	if info.NumRedelivered != 0 || info.NumAckPending != 0 || info.AckFloor.Stream != 2 {
		t.Fatalf("consumer state = redelivered %d, ack pending %d, ack floor %d; want 0, 0, 2",
			info.NumRedelivered, info.NumAckPending, info.AckFloor.Stream)
	}
	if n := r.count(); n != 1 {
		t.Fatalf("handler ran %d times, want 1 (only the valid event)", n)
	}
	if n := e.count(t, `SELECT count(*) FROM inbox`); n != 1 {
		t.Fatalf("inbox has %d rows, want 1", n)
	}
}

func TestConsumeTermsPermanentHandlerErrors(t *testing.T) {
	t.Parallel()
	e := setup(t)
	poison := newEnvelope(t)
	e.publish(t, marshal(t, poison), poison.GetEventId())
	ev := newEnvelope(t)
	e.publish(t, marshal(t, ev), ev.GetEventId())
	r := &recorder{fail: func(call int) error {
		if call == 1 {
			return fmt.Errorf("unpack payload: %w", outbox.ErrPermanent)
		}
		return nil
	}}
	e.consume(t, r.handle)

	// MaxAckPending is 1: the next event only arrives once the poison one is
	// settled, and a nak would bring it back first.
	waitFor(t, 5*time.Second, "the next event", func() bool { return r.count() == 2 })
	time.Sleep(1500 * time.Millisecond) // longer than AckWait
	info := e.consumerInfo(t)
	if info.NumRedelivered != 0 || info.NumAckPending != 0 || info.AckFloor.Stream != 2 {
		t.Fatalf("consumer state = redelivered %d, ack pending %d, ack floor %d; want 0, 0, 2",
			info.NumRedelivered, info.NumAckPending, info.AckFloor.Stream)
	}
	r.mu.Lock()
	calls := slices.Clone(r.calls)
	r.mu.Unlock()
	if !slices.Equal(calls, []string{poison.GetEventId(), ev.GetEventId()}) {
		t.Fatalf("handler calls = %v, want the poison event once, then the next", calls)
	}
	// The poison event's writes and inbox row were rolled back.
	if n := e.count(t, `SELECT count(*) FROM inbox`); n != 1 {
		t.Fatalf("inbox has %d rows, want 1", n)
	}
	if n := e.count(t, `SELECT count(*) FROM applied`); n != 1 {
		t.Fatalf("applied has %d rows, want 1", n)
	}
}

// Not parallel: it swaps the global propagator, and parallel tests only
// start after every sequential test has finished.
func TestRelayPropagatesTraceContext(t *testing.T) {
	prev := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() { otel.SetTextMapPropagator(prev) })

	e := setup(t)
	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	ctx, span := tp.Tracer("test").Start(context.Background(), "create key")
	want := span.SpanContext().TraceID()
	e.enqueue(t, ctx, newEnvelope(t), true)
	span.End()

	r := &recorder{}
	relayCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.relay(e.js).Run(relayCtx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("relay returned %v", err)
		}
	})
	e.consume(t, r.handle)

	waitFor(t, 5*time.Second, "the event", func() bool { return r.count() == 1 })
	r.mu.Lock()
	got := trace.SpanContextFromContext(r.ctxs[0]).TraceID()
	r.mu.Unlock()
	if got != want {
		t.Fatalf("handler trace id = %s, want %s", got, want)
	}
}
