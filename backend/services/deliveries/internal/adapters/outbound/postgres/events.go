package postgres

import (
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/outbox"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/domain"
)

// Source is the envelope source of every event this service publishes.
const Source = "deliveries"

// envelope converts domain event e of tenant/env into an outbox envelope.
// Events without a time of their own (delivery.updated, courier.upserted)
// occur at fallback.
func envelope(tenantID, env string, e domain.Event, fallback time.Time) (*eventsv1.Envelope, error) {
	penv, err := protoEnv(env)
	if err != nil {
		return nil, err
	}
	msg, subj, at, err := toProto(e)
	if err != nil {
		return nil, err
	}
	if at.IsZero() {
		at = fallback
	}
	return outbox.NewEnvelope(e.Type, tenantID, penv, Source, subj, msg, at)
}

// toProto returns e's payload, envelope subject and occurrence time (zero
// when the payload has none).
func toProto(e domain.Event) (proto.Message, *eventsv1.Subject, time.Time, error) {
	switch p := e.Payload.(type) {
	case domain.DeliveryCreated:
		return &eventsv1.DeliveryCreated{
			DeliveryId: p.DeliveryID, ExternalId: p.ExternalID, DeliveryModel: string(p.Model),
			Status: string(p.Status), PickupPolicy: string(p.Policy), Pickup: latLng(p.Pickup),
			PickupName: p.PickupName, Dropoff: latLng(p.Dropoff), CreatedAt: timestamppb.New(p.CreatedAt),
		}, deliverySubject(p.DeliveryID), p.CreatedAt, nil
	case domain.StatusChanged:
		return &eventsv1.DeliveryStatusChanged{
			DeliveryId: p.DeliveryID, From: string(p.From), To: string(p.To), Actor: string(p.Actor),
			OccurredAt: timestamppb.New(p.OccurredAt), Overrode: p.Overrode,
		}, deliverySubject(p.DeliveryID), p.OccurredAt, nil
	case domain.PickupConfirmed:
		return &eventsv1.DeliveryPickupConfirmed{
			DeliveryId: p.DeliveryID, By: string(p.By), OccurredAt: timestamppb.New(p.OccurredAt),
		}, deliverySubject(p.DeliveryID), p.OccurredAt, nil
	case domain.PickupConfirmationPending:
		by := make([]string, len(p.ConfirmedBy))
		for i, b := range p.ConfirmedBy {
			by[i] = string(b)
		}
		return &eventsv1.DeliveryPickupConfirmationPending{
			DeliveryId: p.DeliveryID, ConfirmedBy: by, Since: timestamppb.New(p.Since),
		}, deliverySubject(p.DeliveryID), time.Time{}, nil
	case domain.DeliveryUpdated:
		m := &eventsv1.DeliveryUpdated{DeliveryId: p.DeliveryID, ChangedFields: p.ChangedFields}
		if p.Dropoff != nil {
			m.Dropoff = latLng(*p.Dropoff)
		}
		return m, deliverySubject(p.DeliveryID), time.Time{}, nil
	case domain.PersonalDataErased:
		return &eventsv1.DeliveryPersonalDataErased{
			DeliveryId: p.DeliveryID, ErasedAt: timestamppb.New(p.ErasedAt),
		}, deliverySubject(p.DeliveryID), p.ErasedAt, nil
	case domain.CourierAssigned:
		return &eventsv1.CourierAssigned{
				DeliveryId: p.DeliveryID, CourierId: p.CourierID, AssignedAt: timestamppb.New(p.AssignedAt),
			}, &eventsv1.Subject{DeliveryId: p.DeliveryID, CourierId: p.CourierID},
			p.AssignedAt, nil
	case domain.CourierUpserted:
		return &eventsv1.CourierUpserted{
			CourierId: p.CourierID, ExternalId: p.ExternalID, DisplayName: p.DisplayName,
			Vehicle: string(p.Vehicle), PhotoUrl: p.PhotoURL,
		}, &eventsv1.Subject{CourierId: p.CourierID}, time.Time{}, nil
	}
	return nil, nil, time.Time{}, fmt.Errorf("event %s: unknown payload %T", e.Type, e.Payload)
}

func deliverySubject(id string) *eventsv1.Subject { return &eventsv1.Subject{DeliveryId: id} }

func latLng(l domain.Location) *eventsv1.LatLng { return &eventsv1.LatLng{Lat: l.Lat, Lng: l.Lng} }

func protoEnv(env string) (eventsv1.Environment, error) {
	switch env {
	case "test":
		return eventsv1.Environment_ENVIRONMENT_TEST, nil
	case "live":
		return eventsv1.Environment_ENVIRONMENT_LIVE, nil
	}
	return eventsv1.Environment_ENVIRONMENT_UNSPECIFIED, fmt.Errorf("unknown environment %q", env)
}
