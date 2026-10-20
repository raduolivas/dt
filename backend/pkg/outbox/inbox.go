package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/ids"
)

// RetryDelay is how long a message waits after its handler fails.
const RetryDelay = 2 * time.Second

const tracerName = "github.com/raduolivas/dt/backend/pkg/outbox"

// Handler applies one event. Its writes must go through tx: they commit
// together with the event's inbox row, or not at all. Returning an error
// rolls both back and redelivers the event after RetryDelay.
type Handler func(ctx context.Context, tx pgx.Tx, env *eventsv1.Envelope) error

// Consume creates or updates the consumer cfg on stream and handles its
// messages one at a time until ctx is cancelled, then returns nil. It returns
// an error if the consumer cannot be created or the subscription ends for
// another reason (e.g. the consumer was deleted), which stops the service.
//
// The consumer's name (cfg.Durable, or cfg.Name) keys its inbox rows, so it
// must be stable. Events are processed in stream order only if
// cfg.MaxAckPending is 1; otherwise a failed event is retried after later
// ones.
//
// For each message: an envelope that does not parse is terminated (never
// redelivered) and logged; a known event id is acked without running h;
// otherwise h runs in the transaction that records the event id, and the
// message is acked after commit. If h or the database fails, the message is
// nacked with RetryDelay. An ack lost after commit only causes a redelivery,
// which the inbox absorbs.
func Consume(ctx context.Context, js jetstream.JetStream, pool *pgxpool.Pool, log *slog.Logger,
	stream string, cfg jetstream.ConsumerConfig, h Handler,
) error {
	name := cfg.Durable
	if name == "" {
		name = cfg.Name
	}
	if name == "" {
		return errors.New("outbox: consumer needs a Durable or Name (it keys the inbox)")
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	cons, err := js.CreateOrUpdateConsumer(ctx, stream, cfg)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("outbox: consumer %s on %s: %w", name, stream, err)
	}
	it, err := cons.Messages()
	if err != nil {
		return fmt.Errorf("outbox: consumer %s on %s: %w", name, stream, err)
	}
	defer it.Stop()

	c := &consumer{name: name, pool: pool, log: log, h: h, tracer: otel.Tracer(tracerName)}
	for {
		msg, err := it.Next(jetstream.NextContext(ctx))
		if ctx.Err() != nil {
			if msg != nil {
				// Not handled; the server redelivers it after AckWait.
				_ = msg.Nak()
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("outbox: consumer %s on %s: %w", name, stream, err)
		}
		c.handle(ctx, msg)
	}
}

type consumer struct {
	name   string
	pool   *pgxpool.Pool
	log    *slog.Logger
	h      Handler
	tracer trace.Tracer
}

func (c *consumer) handle(ctx context.Context, msg jetstream.Msg) {
	// Only event-safe fields are logged: never the payload.
	attrs := []any{slog.String("consumer", c.name), slog.String("subject", msg.Subject())}

	env := &eventsv1.Envelope{}
	if err := decode(msg.Data(), env); err != nil {
		c.log.ErrorContext(ctx, "outbox_invalid_event", append(attrs, slog.Any("error", err))...)
		if err := msg.Term(); err != nil {
			c.log.WarnContext(ctx, "outbox_term_failed", append(attrs, slog.Any("error", err))...)
		}
		return
	}
	attrs = append(attrs, slog.String("event_id", env.GetEventId()), slog.String("event_type", env.GetType()))

	ctx = otel.GetTextMapPropagator().Extract(ctx, headerCarrier(msg.Headers()))
	ctx, span := c.tracer.Start(ctx, "consume "+msg.Subject(),
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "nats"),
			attribute.String("messaging.destination.name", msg.Subject()),
			attribute.String("messaging.consumer.group.name", c.name),
			attribute.String("messaging.message.id", env.GetEventId()),
		))
	defer span.End()

	if err := c.apply(ctx, env); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "handler failed")
		c.log.WarnContext(ctx, "outbox_handler_failed", append(attrs, slog.Any("error", err))...)
		if err := msg.NakWithDelay(RetryDelay); err != nil {
			c.log.WarnContext(ctx, "outbox_nak_failed", append(attrs, slog.Any("error", err))...)
		}
		return
	}
	if err := msg.Ack(); err != nil {
		// Committed already: the redelivery will be a duplicate.
		c.log.WarnContext(ctx, "outbox_ack_failed", append(attrs, slog.Any("error", err))...)
	}
}

// apply records env in the inbox and runs the handler, in one transaction.
// A duplicate commits the no-op and skips the handler.
func (c *consumer) apply(ctx context.Context, env *eventsv1.Envelope) error {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	tag, err := tx.Exec(ctx, `INSERT INTO inbox (consumer, event_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		c.name, env.GetEventId())
	if err != nil {
		return fmt.Errorf("inbox: %w", err)
	}
	if tag.RowsAffected() == 1 {
		if err := c.h(ctx, tx, env); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// decode parses an envelope and checks the fields the inbox and handlers
// rely on.
func decode(data []byte, env *eventsv1.Envelope) error {
	if err := proto.Unmarshal(data, env); err != nil {
		return fmt.Errorf("not an envelope: %w", err)
	}
	if !ids.Valid(ids.Event, env.GetEventId()) {
		return errors.New("envelope has no valid event_id")
	}
	if env.GetType() == "" {
		return errors.New("envelope has no type")
	}
	return nil
}
