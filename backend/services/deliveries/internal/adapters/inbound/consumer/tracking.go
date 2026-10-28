package consumer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go/jetstream"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/outbox"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/app"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/domain"
)

// TrackingStream is the stream tracking.arrival_detected arrives on.
const TrackingStream = "TRACKING"

// arrivalEventType is the tracking event this consumer handles.
const arrivalEventType = "tracking.arrival_detected"

// TrackingConfig is the durable consumer of tracking's arrivals. Each
// delivery gets at most a few, and they are independent, so order across
// deliveries does not matter.
var TrackingConfig = jetstream.ConsumerConfig{
	Durable:       "deliveries-tracking",
	FilterSubject: "tracking." + arrivalEventType,
	DeliverPolicy: jetstream.DeliverAllPolicy,
	AckPolicy:     jetstream.AckExplicitPolicy,
	MaxAckPending: 16,
	AckWait:       30 * time.Second,
}

// Reporter applies a status report; it is satisfied by *app.Service.
type Reporter interface {
	Report(ctx context.Context, sc app.Scope, id string, r domain.Report) (*domain.Delivery, domain.Outcome, error)
}

// Tracking returns the handler that applies arriving, with actor system,
// when tracking detects the courier's arrival (design M3 §5.1, deferred from
// M2 C3). The report's event id is the envelope's, so a redelivery is a
// duplicate in the delivery's history as well as in the inbox. Tracking may
// announce an arrival again (after losing its live state, under a new event
// id): the delivery is then arriving already (stale) or past it (an invalid
// transition), and either is a no-op, as is a delivery that is gone. An
// event that can never apply is rejected as outbox.ErrPermanent.
func Tracking(r Reporter, log *slog.Logger) outbox.Handler {
	if log == nil {
		log = slog.Default()
	}
	return func(ctx context.Context, _ pgx.Tx, env *eventsv1.Envelope) error {
		if env.GetType() != arrivalEventType {
			return nil
		}
		var ev eventsv1.ArrivalDetected
		if err := env.GetPayload().UnmarshalTo(&ev); err != nil {
			return fmt.Errorf("arrival payload: %w", outbox.ErrPermanent)
		}
		var envName string
		switch env.GetEnvironment() {
		case eventsv1.Environment_ENVIRONMENT_TEST:
			envName = "test"
		case eventsv1.Environment_ENVIRONMENT_LIVE:
			envName = "live"
		default:
			return fmt.Errorf("arrival without environment: %w", outbox.ErrPermanent)
		}
		if env.GetTenantId() == "" || ev.GetDeliveryId() == "" {
			return fmt.Errorf("arrival without tenant or delivery: %w", outbox.ErrPermanent)
		}
		at := env.GetOccurredAt().AsTime()
		if ev.GetAt() != nil {
			at = ev.GetAt().AsTime()
		}
		sc := app.Scope{TenantID: env.GetTenantId(), Env: envName, ActorID: string(domain.ActorSystem)}
		_, outcome, err := r.Report(ctx, sc, ev.GetDeliveryId(), domain.Report{
			EventID: env.GetEventId(), Type: domain.ReportArriving, Actor: domain.ActorSystem, OccurredAt: at,
		})
		var verr *domain.ValidationError
		switch {
		case err == nil:
			log.DebugContext(ctx, "arrival_applied", "event_id", env.GetEventId(), "outcome", string(outcome))
			return nil
		case errors.Is(err, domain.ErrInvalidTransition), errors.Is(err, app.ErrNotFound):
			log.DebugContext(ctx, "arrival_ignored", "event_id", env.GetEventId(), "error", err)
			return nil
		case errors.As(err, &verr):
			log.ErrorContext(ctx, "arrival_invalid", "event_id", env.GetEventId(), "field", verr.Field)
			return fmt.Errorf("invalid arrival: %w", outbox.ErrPermanent)
		}
		return err
	}
}
