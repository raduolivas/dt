package ingest

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/normalize"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
)

const (
	fetchBatch    = 100
	fetchWait     = time.Second
	retryWait     = 2 * time.Second
	nakDelay      = 2 * time.Second
	defaultRetain = 7 * 24 * time.Hour
)

// Streams MC reads. ACCOUNTS has no max age, so it starts from now-retention;
// the others are bounded to 7 days by DT and start from the beginning.
var natsStreams = []struct {
	Stream, Durable string
	ByStartTime     bool
}{
	{"ACCOUNTS", "mc-accounts", true},
	{"DELIVERIES", "mc-deliveries", false},
	{"TRACKING", "mc-tracking", false},
	{"GATEWAY", "mc-gateway", false},
}

// NATSOptions are the optional settings of RunNATS.
type NATSOptions struct {
	Self      *Self
	Log       *slog.Logger
	Retention time.Duration // ACCOUNTS start window; default 7 days
}

// RunNATS reads the four DT streams through read-only durable consumers
// (mc-accounts, mc-deliveries, mc-tracking, mc-gateway) until ctx ends. A
// durable is created when absent with its deliver policy; an existing one is
// reused as-is (ruling R12). Messages are acked after the insert. It never
// publishes. A stream that does not exist yet is retried.
func RunNATS(ctx context.Context, js jetstream.JetStream, st *store.Store, n *normalize.Normalizer, o NATSOptions) error {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Retention <= 0 {
		o.Retention = defaultRetain
	}
	var wg sync.WaitGroup
	for _, s := range natsStreams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := &streamConsumer{js: js, st: st, n: n, o: o, stream: s.Stream, durable: s.Durable, byStart: s.ByStartTime,
				source: "nats." + s.Stream}
			c.run(ctx)
		}()
	}
	wg.Wait()
	return nil
}

type streamConsumer struct {
	js              jetstream.JetStream
	st              *store.Store
	n               *normalize.Normalizer
	o               NATSOptions
	stream, durable string
	byStart         bool
	source          string
	warn            rateLog
}

func (c *streamConsumer) run(ctx context.Context) {
	for ctx.Err() == nil {
		cons, err := c.ensure(ctx)
		if err != nil {
			c.warn.log(c.o.Log, "nats consumer not ready", "stream", c.stream, "error", err.Error())
			sleep(ctx, retryWait)
			continue
		}
		for ctx.Err() == nil {
			if err := c.fetchOnce(ctx, cons); err != nil {
				c.warn.log(c.o.Log, "nats fetch failed", "stream", c.stream, "error", err.Error())
				sleep(ctx, retryWait)
				if errors.Is(err, jetstream.ErrConsumerNotFound) || errors.Is(err, jetstream.ErrStreamNotFound) {
					break // set up again
				}
			}
		}
	}
}

func (c *streamConsumer) ensure(ctx context.Context) (jetstream.Consumer, error) {
	cons, err := c.js.Consumer(ctx, c.stream, c.durable)
	if err == nil {
		return cons, nil // reuse as-is
	}
	if !errors.Is(err, jetstream.ErrConsumerNotFound) {
		return nil, err
	}
	cfg := jetstream.ConsumerConfig{
		Durable:       c.durable,
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckWait:       30 * time.Second,
	}
	if c.byStart {
		start := time.Now().Add(-c.o.Retention)
		cfg.DeliverPolicy = jetstream.DeliverByStartTimePolicy
		cfg.OptStartTime = &start
	}
	return c.js.CreateConsumer(ctx, c.stream, cfg)
}

func (c *streamConsumer) fetchOnce(ctx context.Context, cons jetstream.Consumer) error {
	mb, err := cons.Fetch(fetchBatch, jetstream.FetchMaxWait(fetchWait))
	if err != nil {
		return err
	}
	var (
		msgs []jetstream.Msg
		evs  []store.Event
		lag  time.Duration
	)
	now := time.Now().UTC()
	for m := range mb.Messages() {
		env := &eventsv1.Envelope{}
		if err := proto.Unmarshal(m.Data(), env); err != nil || env.GetType() == "" {
			c.o.Self.Drop(c.source, 1)
			c.warn.log(c.o.Log, "invalid envelope dropped", "stream", c.stream, "subject", m.Subject())
			_ = m.Ack()
			continue
		}
		ev, err := c.n.FromEnvelope(m.Subject(), env, now)
		if err != nil {
			c.o.Self.Drop(c.source, 1)
			c.warn.log(c.o.Log, "event not normalized, dropped", "stream", c.stream, "subject", m.Subject())
			_ = m.Ack()
			continue
		}
		if md, err := m.Metadata(); err == nil {
			lag = max(lag, now.Sub(md.Timestamp))
		}
		msgs = append(msgs, m)
		evs = append(evs, ev)
	}
	if len(evs) > 0 {
		if err := c.st.InsertEvents(ctx, evs); err != nil {
			c.o.Self.StorageError(c.source)
			c.warn.log(c.o.Log, "insert failed, will retry", "stream", c.stream, "error", err.Error())
			for _, m := range msgs {
				_ = m.NakWithDelay(nakDelay)
			}
			sleep(ctx, nakDelay)
			return nil
		}
		for _, m := range msgs {
			_ = m.Ack()
		}
		c.o.Self.Lag(c.source, lag)
	} else if mb.Error() == nil {
		c.o.Self.Lag(c.source, 0)
	}
	if err := mb.Error(); err != nil && !errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return err
	}
	return nil
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
