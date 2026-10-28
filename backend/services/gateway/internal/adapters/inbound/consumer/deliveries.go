package consumer

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go/jetstream"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/outbox"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

// EventStatusChanged is the deliveries event the gateway reads.
const EventStatusChanged = "delivery.status_changed"

// DeliveriesStream is the deliveries service's stream. The gateway never
// creates it; it waits for it.
var DeliveriesStream = outbox.StreamDeliveries.Name

// DeliveriesConfig is the durable consumer that expires tracking links when
// their delivery becomes final.
var DeliveriesConfig = jetstream.ConsumerConfig{
	Durable:       "gateway-deliveries",
	FilterSubject: outbox.SubjectFor(outbox.StreamDeliveries, EventStatusChanged),
	DeliverPolicy: jetstream.DeliverAllPolicy,
	AckPolicy:     jetstream.AckExplicitPolicy,
	MaxAckPending: 1,
	AckWait:       30 * time.Second,
}

// LinkExpirer is satisfied by *app.TrackingLinks. ExpireOnFinal is
// idempotent (the first final time wins), so it runs outside the inbox
// transaction without harm on a redelivery.
type LinkExpirer interface {
	ExpireOnFinal(ctx context.Context, deliveryID string, finalAt time.Time) error
}

// Deliveries returns the handler of delivery.status_changed: a change into a
// final status makes the delivery's tracking links expire 30 minutes later.
// Malformed events are outbox.ErrPermanent; store errors are retried.
func Deliveries(links LinkExpirer) outbox.Handler {
	return func(ctx context.Context, _ pgx.Tx, env *eventsv1.Envelope) error {
		if env.GetType() != EventStatusChanged {
			return nil
		}
		p := &eventsv1.DeliveryStatusChanged{}
		if err := unpack(env, p); err != nil {
			return err
		}
		if !domain.FinalStatus(p.GetTo()) {
			return nil
		}
		at := p.GetOccurredAt()
		if at == nil {
			at = env.GetOccurredAt()
		}
		if p.GetDeliveryId() == "" || at == nil {
			return permanent("status_changed needs delivery_id and occurred_at")
		}
		if err := links.ExpireOnFinal(ctx, p.GetDeliveryId(), at.AsTime()); err != nil {
			return fmt.Errorf("expire tracking links: %w", err)
		}
		return nil
	}
}
