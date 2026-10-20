package outbox

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
)

// Enqueue stores env in the outbox table as part of tx, to be published on
// subject by a Relay once tx commits. The trace context in ctx (W3C
// traceparent and baggage, via the global propagator) is stored with it, so
// consumers continue the producer's trace.
func Enqueue(ctx context.Context, tx pgx.Tx, subject string, env *eventsv1.Envelope) error {
	if env.GetEventId() == "" {
		return errors.New("outbox: envelope has no event_id")
	}
	if subject == "" {
		return errors.New("outbox: subject is required")
	}
	payload, err := proto.Marshal(env)
	if err != nil {
		return fmt.Errorf("outbox: marshal %s: %w", env.GetEventId(), err)
	}
	headers := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, headers)
	if _, err := tx.Exec(ctx,
		`INSERT INTO outbox (event_id, subject, payload, headers) VALUES ($1, $2, $3, $4)`,
		env.GetEventId(), subject, payload, map[string]string(headers)); err != nil {
		return fmt.Errorf("outbox: enqueue %s: %w", env.GetEventId(), err)
	}
	return nil
}

// headerCarrier adapts NATS headers to OpenTelemetry. NATS header names are
// case-sensitive, so unlike propagation.HeaderCarrier it does not
// canonicalise them.
type headerCarrier nats.Header

func (c headerCarrier) Get(key string) string {
	if v := c[key]; len(v) > 0 {
		return v[0]
	}
	return ""
}

func (c headerCarrier) Set(key, value string) { c[key] = []string{value} }

func (c headerCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}
