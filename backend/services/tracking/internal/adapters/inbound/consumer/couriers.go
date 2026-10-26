package consumer

import (
	"context"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/services/tracking/internal/app"
	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

var vehicles = map[string]bool{"bicycle": true, "e_bike": true, "scooter": true, "car": true, "on_foot": true}

func courierAssigned(ctx context.Context, svc *app.Service, p app.Projection, sc app.Scope, v uint64, env *eventsv1.Envelope) error {
	var ev eventsv1.CourierAssigned
	if err := env.GetPayload().UnmarshalTo(&ev); err != nil {
		return permanent("courier.assigned payload")
	}
	if ev.GetDeliveryId() == "" || ev.GetCourierId() == "" {
		return permanent("courier.assigned without ids")
	}
	at := env.GetOccurredAt().AsTime()
	if ev.GetAssignedAt() != nil {
		at = ev.GetAssignedAt().AsTime()
	}
	return svc.OnCourierAssigned(ctx, p, sc, v, ev.GetDeliveryId(), ev.GetCourierId(), at)
}

func courierUpserted(ctx context.Context, svc *app.Service, p app.Projection, sc app.Scope, env *eventsv1.Envelope) error {
	var ev eventsv1.CourierUpserted
	if err := env.GetPayload().UnmarshalTo(&ev); err != nil {
		return permanent("courier.upserted payload")
	}
	if ev.GetCourierId() == "" || !vehicles[ev.GetVehicle()] {
		return permanent("courier.upserted without id or with an unknown vehicle")
	}
	c := domain.Courier{
		ID: ev.GetCourierId(), ExternalID: ev.GetExternalId(), DisplayName: ev.GetDisplayName(), Vehicle: ev.GetVehicle(),
	}
	if u := ev.GetPhotoUrl(); u != "" {
		c.PhotoURL = &u
	}
	return svc.OnCourierUpserted(ctx, p, sc, c)
}
