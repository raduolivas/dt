// Package consumer holds the tracking service's event consumers: the
// DELIVERIES stream (delivery.* and courier.*) and tenant settings from
// ACCOUNTS. Each handler writes the read model in the inbox transaction and
// then updates the live state through the app.
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
	"github.com/raduolivas/dt/backend/services/tracking/internal/app"
	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

// DeliveriesStream is the stream delivery.* and courier.* events arrive on.
const DeliveriesStream = "DELIVERIES"

// Event types handled from DELIVERIES.
const (
	evDeliveryCreated = "delivery.created"
	evStatusChanged   = "delivery.status_changed"
	evDeliveryUpdated = "delivery.updated"
	evErased          = "delivery.personal_data_erased"
	evCourierAssigned = "courier.assigned"
	evCourierUpserted = "courier.upserted"
)

// DeliveriesConfig is the durable consumer of the DELIVERIES stream.
// MaxAckPending 1 keeps events in stream order (a courier is upserted before
// it is assigned, a delivery created before it changes).
var DeliveriesConfig = jetstream.ConsumerConfig{
	Durable:       "tracking-deliveries",
	FilterSubject: "deliveries.>",
	DeliverPolicy: jetstream.DeliverAllPolicy,
	AckPolicy:     jetstream.AckExplicitPolicy,
	MaxAckPending: 1,
	AckWait:       30 * time.Second,
}

// Binder scopes a consumer transaction (implemented by the Postgres repo).
type Binder interface {
	Bind(ctx context.Context, tx pgx.Tx, s app.Scope) (app.Projection, error)
	Settings(tx pgx.Tx) app.SettingsProjection
}

// Deliveries returns the handler of the DELIVERIES stream. An event about a
// delivery tracking never saw (it aged out of the stream before tracking
// first read it) is logged and skipped; a malformed one is rejected as
// outbox.ErrPermanent. Other errors are retried.
func Deliveries(b Binder, svc *app.Service, log *slog.Logger) outbox.Handler {
	if log == nil {
		log = slog.Default()
	}
	return func(ctx context.Context, tx pgx.Tx, env *eventsv1.Envelope) error {
		switch env.GetType() {
		case evDeliveryCreated, evStatusChanged, evDeliveryUpdated, evErased, evCourierAssigned, evCourierUpserted:
		default:
			return nil
		}
		sc, err := scopeOf(env)
		if err != nil {
			return err
		}
		p, err := b.Bind(ctx, tx, sc)
		if err != nil {
			return err
		}
		v := env.GetSubject().GetDeliveryVersion()
		switch env.GetType() {
		case evDeliveryCreated:
			err = created(ctx, svc, p, sc, v, env)
		case evStatusChanged:
			err = statusChanged(ctx, svc, p, sc, v, env)
		case evDeliveryUpdated:
			err = updated(ctx, svc, p, sc, v, env)
		case evErased:
			err = erased(ctx, svc, p, sc, v, env)
		case evCourierAssigned:
			err = courierAssigned(ctx, svc, p, sc, v, env)
		case evCourierUpserted:
			err = courierUpserted(ctx, svc, p, sc, env)
		}
		if errors.Is(err, app.ErrNotFound) {
			log.WarnContext(ctx, "tracking_event_for_unknown_delivery",
				"event_id", env.GetEventId(), "event_type", env.GetType(), "delivery_id", env.GetSubject().GetDeliveryId())
			return nil
		}
		return err
	}
}

func scopeOf(env *eventsv1.Envelope) (app.Scope, error) {
	if env.GetTenantId() == "" {
		return app.Scope{}, fmt.Errorf("event without tenant: %w", outbox.ErrPermanent)
	}
	switch env.GetEnvironment() {
	case eventsv1.Environment_ENVIRONMENT_TEST:
		return app.Scope{TenantID: env.GetTenantId(), Env: "test"}, nil
	case eventsv1.Environment_ENVIRONMENT_LIVE:
		return app.Scope{TenantID: env.GetTenantId(), Env: "live"}, nil
	}
	return app.Scope{}, fmt.Errorf("event without environment: %w", outbox.ErrPermanent)
}

func permanent(what string) error { return fmt.Errorf("%s: %w", what, outbox.ErrPermanent) }

var statuses = map[string]domain.Status{}

func init() {
	for _, s := range []domain.Status{domain.StatusConfirmed, domain.StatusCourierAssigned, domain.StatusAtPickup,
		domain.StatusPickedUp, domain.StatusArriving, domain.StatusDelivered, domain.StatusCancelled, domain.StatusFailed} {
		statuses[string(s)] = s
	}
}

func status(s string) (domain.Status, error) {
	if st, ok := statuses[s]; ok {
		return st, nil
	}
	return "", permanent("unknown status")
}

func latLng(p *eventsv1.LatLng) (domain.LatLng, error) {
	if p == nil || p.GetLat() < -90 || p.GetLat() > 90 || p.GetLng() < -180 || p.GetLng() > 180 {
		return domain.LatLng{}, permanent("invalid location")
	}
	return domain.LatLng{Lat: p.GetLat(), Lng: p.GetLng()}, nil
}

func created(ctx context.Context, svc *app.Service, p app.Projection, sc app.Scope, v uint64, env *eventsv1.Envelope) error {
	var ev eventsv1.DeliveryCreated
	if err := env.GetPayload().UnmarshalTo(&ev); err != nil {
		return permanent("delivery.created payload")
	}
	m := domain.Model(ev.GetDeliveryModel())
	if m != domain.ModelDirect && m != domain.ModelEndToEnd {
		return permanent("unknown model")
	}
	st, err := status(ev.GetStatus())
	if err != nil {
		return err
	}
	pickup, err := latLng(ev.GetPickup())
	if err != nil {
		return err
	}
	dropoff, err := latLng(ev.GetDropoff())
	if err != nil {
		return err
	}
	in := app.DeliveryCreated{
		ID: ev.GetDeliveryId(), Model: m, Status: st, Pickup: pickup, PickupName: ev.GetPickupName(),
		Dropoff: dropoff, CreatedAt: ev.GetCreatedAt().AsTime(),
	}
	if ev.GetDeliveryId() == "" || ev.GetCreatedAt() == nil {
		return permanent("delivery.created without id or created_at")
	}
	if ev.GetReadyAt() != nil {
		r := ev.GetReadyAt().AsTime()
		in.ReadyAt = &r
	}
	return svc.OnDeliveryCreated(ctx, p, sc, v, in)
}

func statusChanged(ctx context.Context, svc *app.Service, p app.Projection, sc app.Scope, v uint64, env *eventsv1.Envelope) error {
	var ev eventsv1.DeliveryStatusChanged
	if err := env.GetPayload().UnmarshalTo(&ev); err != nil {
		return permanent("delivery.status_changed payload")
	}
	from, err := status(ev.GetFrom())
	if err != nil {
		return err
	}
	to, err := status(ev.GetTo())
	if err != nil {
		return err
	}
	at := env.GetOccurredAt().AsTime()
	if ev.GetOccurredAt() != nil {
		at = ev.GetOccurredAt().AsTime()
	}
	actor := ev.GetActor()
	switch actor {
	case "partner", "courier", "system":
	default:
		return permanent("unknown actor")
	}
	return svc.OnStatusChanged(ctx, p, sc, v, app.StatusChanged{ID: ev.GetDeliveryId(), From: from, To: to, Actor: actor, At: at})
}

func updated(ctx context.Context, svc *app.Service, p app.Projection, sc app.Scope, v uint64, env *eventsv1.Envelope) error {
	var ev eventsv1.DeliveryUpdated
	if err := env.GetPayload().UnmarshalTo(&ev); err != nil {
		return permanent("delivery.updated payload")
	}
	in := app.DeliveryUpdated{ID: ev.GetDeliveryId()}
	for _, f := range ev.GetChangedFields() {
		switch f {
		case "dropoff.location":
			d, err := latLng(ev.GetDropoff())
			if err != nil {
				return err
			}
			in.Dropoff = &d
		case "ready_at":
			in.ReadyAtChanged = true
			if ev.GetReadyAt() != nil {
				r := ev.GetReadyAt().AsTime()
				in.ReadyAt = &r
			}
		}
	}
	return svc.OnDeliveryUpdated(ctx, p, sc, v, in)
}

func erased(ctx context.Context, svc *app.Service, p app.Projection, sc app.Scope, v uint64, env *eventsv1.Envelope) error {
	var ev eventsv1.DeliveryPersonalDataErased
	if err := env.GetPayload().UnmarshalTo(&ev); err != nil {
		return permanent("delivery.personal_data_erased payload")
	}
	at := env.GetOccurredAt().AsTime()
	if ev.GetErasedAt() != nil {
		at = ev.GetErasedAt().AsTime()
	}
	return svc.OnPersonalDataErased(ctx, p, sc, v, ev.GetDeliveryId(), at)
}
