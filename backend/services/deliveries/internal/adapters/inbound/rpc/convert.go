package rpc

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	deliveriesv1 "github.com/raduolivas/dt/backend/gen/dt/deliveries/v1"
	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/app"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/domain"
)

func violation(field, code, msg string) error {
	return &domain.ValidationError{Field: field, Code: code, Message: msg}
}

// toScope converts the request scope; a missing tenant or environment is a
// validation error on "scope".
func toScope(s *deliveriesv1.Scope) (app.Scope, error) {
	if s.GetTenantId() == "" {
		return app.Scope{}, violation("scope", "required", "tenant_id is required")
	}
	var env string
	switch s.GetEnvironment() {
	case eventsv1.Environment_ENVIRONMENT_TEST:
		env = "test"
	case eventsv1.Environment_ENVIRONMENT_LIVE:
		env = "live"
	default:
		return app.Scope{}, violation("scope", "required", "environment must be test or live")
	}
	return app.Scope{TenantID: s.GetTenantId(), Env: env, ActorID: s.GetActorId()}, nil
}

func fromEnv(env string) eventsv1.Environment {
	switch env {
	case "test":
		return eventsv1.Environment_ENVIRONMENT_TEST
	case "live":
		return eventsv1.Environment_ENVIRONMENT_LIVE
	}
	return eventsv1.Environment_ENVIRONMENT_UNSPECIFIED
}

func toLocation(field string, l *eventsv1.LatLng) (domain.Location, error) {
	if l == nil {
		return domain.Location{}, violation(field, "required", "must be set")
	}
	return domain.Location{Lat: l.GetLat(), Lng: l.GetLng()}, nil
}

func fromLocation(l domain.Location) *eventsv1.LatLng {
	return &eventsv1.LatLng{Lat: l.Lat, Lng: l.Lng}
}

func toCreateInput(req *deliveriesv1.CreateDeliveryRequest) (domain.NewDeliveryInput, error) {
	pickup, err := toLocation("pickup.location", req.GetPickup().GetLocation())
	if err != nil {
		return domain.NewDeliveryInput{}, err
	}
	dropoff, err := toLocation("dropoff.location", req.GetDropoff().GetLocation())
	if err != nil {
		return domain.NewDeliveryInput{}, err
	}
	in := domain.NewDeliveryInput{
		ExternalID: req.GetExternalId(),
		Pickup:     pickup, PickupName: req.GetPickup().GetName(),
		Dropoff: dropoff, DropoffLabel: dropoffLabel(req.GetDropoff()),
		CustomerFirstName: firstName(req.GetCustomer()),
		ItemsSummary:      req.ItemsSummary, Notes: req.Notes,
		Metadata: req.GetMetadata(),
	}
	if t := req.GetReadyAt(); t != nil {
		v := t.AsTime()
		in.ReadyAt = &v
	}
	if m := req.GetDeliveryModel(); m != "" {
		model := domain.Model(m)
		in.Model = &model
	}
	if req.ItemCount != nil {
		n := int(*req.ItemCount)
		in.ItemCount = &n
	}
	return in, nil
}

func toPatch(req *deliveriesv1.UpdateDeliveryRequest) (domain.Patch, error) {
	var p domain.Patch
	for _, path := range req.GetUpdateMask().GetPaths() {
		switch path {
		case "dropoff":
			if req.GetDropoff() == nil {
				return p, violation("dropoff", "required", "cannot be cleared")
			}
			l := domain.Location{Lat: req.GetDropoff().GetLat(), Lng: req.GetDropoff().GetLng()}
			p.Dropoff = &l
		case "dropoff_label":
			v := req.DropoffLabel
			p.DropoffLabel = &v
		case "notes":
			v := req.Notes
			p.Notes = &v
		case "ready_at":
			var v *time.Time
			if t := req.GetReadyAt(); t != nil {
				x := t.AsTime()
				v = &x
			}
			p.ReadyAt = &v
		case "metadata":
			p.MetadataSet = true
			if len(req.GetMetadata()) > 0 {
				p.Metadata = req.GetMetadata()
			}
		default:
			return p, violation("update_mask", "invalid", "unknown path "+path)
		}
	}
	return p, nil
}

func toFilter(req *deliveriesv1.ListDeliveriesRequest) app.ListFilter {
	f := app.ListFilter{Limit: int(req.GetLimit()), StartingAfter: req.GetStartingAfter()}
	if s := req.GetStatus(); s != "" {
		st := domain.Status(s)
		f.Status = &st
	}
	if m := req.GetDeliveryModel(); m != "" {
		model := domain.Model(m)
		f.Model = &model
	}
	if t := req.GetCreatedGte(); t != nil {
		v := t.AsTime()
		f.CreatedGTE = &v
	}
	if t := req.GetCreatedLt(); t != nil {
		v := t.AsTime()
		f.CreatedLT = &v
	}
	if e := req.GetExternalId(); e != "" {
		f.ExternalID = &e
	}
	return f
}

// toReport maps a report. The actor is the partner unless an internal caller
// (the tracking simulator) says "courier"; the gateway never sets it. A
// courier's confirmation is always by the courier.
func toReport(req *deliveriesv1.ReportEventRequest) (domain.Report, error) {
	r := domain.Report{
		EventID: req.GetEventId(), Type: domain.ReportType(req.GetType()),
		Actor: domain.ActorPartner, By: domain.ConfirmedBy(req.GetBy()), Reason: req.GetReason(),
	}
	switch req.GetActor() {
	case "", "partner":
	case "courier":
		r.Actor, r.By = domain.ActorCourier, domain.ByCourier
	default:
		return r, violation("actor", "invalid", "must be partner or courier")
	}
	if t := req.GetOccurredAt(); t != nil {
		r.OccurredAt = t.AsTime()
	}
	return r, nil
}

func toCourierInput(req *deliveriesv1.UpsertCourierRequest) domain.CourierInput {
	return domain.CourierInput{
		ExternalID: req.GetExternalId(), DisplayName: req.GetDisplayName(),
		PhotoURL: req.GetPhotoUrl(), Vehicle: domain.Vehicle(req.GetVehicle()),
	}
}

func ts(t time.Time) *timestamppb.Timestamp { return timestamppb.New(t) }

// fromDelivery includes the personal fields: the gateway returns them to the
// owning partner. They must never reach a log line.
func fromDelivery(d *domain.Delivery) *deliveriesv1.Delivery {
	out := &deliveriesv1.Delivery{
		Id: d.ID, ExternalId: d.ExternalID, Environment: fromEnv(d.Env),
		DeliveryModel: string(d.Model), Status: string(d.Status),
		Pickup:       &deliveriesv1.Pickup{Location: fromLocation(d.Pickup), Name: d.PickupName},
		Dropoff:      &deliveriesv1.Dropoff{Location: fromLocation(d.Dropoff), Label: d.DropoffLabel},
		Customer:     &deliveriesv1.Customer{FirstName: d.CustomerFirstName},
		ItemsSummary: d.ItemsSummary, Notes: d.Notes,
		Metadata:           d.Metadata,
		PickupPolicy:       string(d.Policy),
		PersonalDataErased: d.ErasedAt != nil,
		CreatedAt:          ts(d.CreatedAt), UpdatedAt: ts(d.UpdatedAt),
	}
	if d.ReadyAt != nil {
		out.ReadyAt = ts(*d.ReadyAt)
	}
	if d.ItemCount != nil {
		n := int32(min(*d.ItemCount, 999)) //nolint:gosec // bounded by the domain (0-999)
		out.ItemCount = &n
	}
	if c := d.Courier; c != nil {
		out.Courier = &deliveriesv1.CourierRef{
			Id: c.ID, ExternalId: c.ExternalID, DisplayName: c.DisplayName, Vehicle: string(c.Vehicle),
		}
	}
	for _, h := range d.History {
		if h.Type == string(domain.ReportPickupConfirmed) {
			out.PickupConfirmations = append(out.PickupConfirmations,
				&deliveriesv1.PickupConfirmation{By: string(h.By), At: ts(h.OccurredAt)})
		}
	}
	return out
}

func fromCourier(c domain.Courier) *deliveriesv1.Courier {
	out := &deliveriesv1.Courier{
		Id: c.ID, ExternalId: c.ExternalID, DisplayName: c.DisplayName, Vehicle: string(c.Vehicle),
		CreatedAt: ts(c.CreatedAt), UpdatedAt: ts(c.UpdatedAt),
	}
	if c.PhotoURL != "" {
		out.PhotoUrl = &c.PhotoURL
	}
	return out
}

// firstName and dropoffLabel read optional fields through a possibly nil
// message.
func firstName(c *deliveriesv1.Customer) *string {
	if c == nil {
		return nil
	}
	return c.FirstName
}

func dropoffLabel(d *deliveriesv1.Dropoff) *string {
	if d == nil {
		return nil
	}
	return d.Label
}
