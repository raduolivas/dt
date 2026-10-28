package deliveriesrpc

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect/v2"

	deliveriesv1 "github.com/raduolivas/dt/backend/gen/dt/deliveries/v1"
	"github.com/raduolivas/dt/backend/gen/dt/deliveries/v1/deliveriesv1connect"
	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/rpcauth"
	"github.com/raduolivas/dt/backend/services/tracking/internal/app"
	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

type fakeDeliveries struct {
	deliveriesv1connect.DeliveriesServiceClient
	reqs []any
	fail func(context.Context) error
}

func (f *fakeDeliveries) rec(ctx context.Context, r any) error {
	f.reqs = append(f.reqs, r)
	if f.fail != nil {
		return f.fail(ctx)
	}
	return nil
}

func (f *fakeDeliveries) GetDelivery(ctx context.Context, r *deliveriesv1.GetDeliveryRequest) (*deliveriesv1.GetDeliveryResponse, error) {
	if err := f.rec(ctx, r); err != nil {
		return nil, err
	}
	return &deliveriesv1.GetDeliveryResponse{Delivery: &deliveriesv1.Delivery{
		Id: "del_1", DeliveryModel: "end_to_end", Status: "courier_assigned", PickupPolicy: "store",
		Pickup:  &deliveriesv1.Pickup{Location: &eventsv1.LatLng{Lat: 52.36, Lng: 4.9}, Name: "Hub"},
		Dropoff: &deliveriesv1.Dropoff{Location: &eventsv1.LatLng{Lat: 52.37, Lng: 4.89}},
		Courier: &deliveriesv1.CourierRef{Id: "cou_1", ExternalId: "x", DisplayName: "Sam", Vehicle: "car"},
	}}, nil
}

func (f *fakeDeliveries) UpsertCourier(ctx context.Context, r *deliveriesv1.UpsertCourierRequest) (*deliveriesv1.UpsertCourierResponse, error) {
	if err := f.rec(ctx, r); err != nil {
		return nil, err
	}
	return &deliveriesv1.UpsertCourierResponse{Courier: &deliveriesv1.Courier{Id: "cou_sim", ExternalId: r.GetExternalId(),
		DisplayName: r.GetDisplayName(), Vehicle: r.GetVehicle()}, Created: true}, nil
}

func (f *fakeDeliveries) AssignCourier(ctx context.Context, r *deliveriesv1.AssignCourierRequest) (*deliveriesv1.AssignCourierResponse, error) {
	return &deliveriesv1.AssignCourierResponse{}, f.rec(ctx, r)
}

func (f *fakeDeliveries) ReportEvent(ctx context.Context, r *deliveriesv1.ReportEventRequest) (*deliveriesv1.ReportEventResponse, error) {
	return &deliveriesv1.ReportEventResponse{Outcome: "applied"}, f.rec(ctx, r)
}

func failWith(code connect.Code, dtCode string) func(context.Context) error {
	return func(ctx context.Context) error {
		if info, ok := connect.CallInfoForClientContext(ctx); ok && dtCode != "" {
			info.ResponseTrailer().Set(rpcauth.CodeKey, dtCode)
		}
		return connect.NewError(code, "detail")
	}
}

var sc = app.Scope{TenantID: "ten_a", Env: "test"}

func TestClientMapsRequestsAndAnswers(t *testing.T) {
	ctx := context.Background()
	f := &fakeDeliveries{}
	c := Wrap(f)

	d, err := c.Get(ctx, sc, "del_1")
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != "del_1" || d.Model != domain.ModelEndToEnd || d.Status != domain.StatusCourierAssigned || d.PickupPolicy != "store" ||
		d.Pickup != (domain.LatLng{Lat: 52.36, Lng: 4.9}) || d.Dropoff != (domain.LatLng{Lat: 52.37, Lng: 4.89}) ||
		d.Courier == nil || *d.Courier != (domain.Courier{ID: "cou_1", ExternalID: "x", DisplayName: "Sam", Vehicle: "car"}) {
		t.Fatalf("delivery = %+v", d)
	}
	g := f.reqs[0].(*deliveriesv1.GetDeliveryRequest)
	if g.GetId() != "del_1" || g.GetScope().GetTenantId() != "ten_a" || g.GetScope().GetEnvironment() != eventsv1.Environment_ENVIRONMENT_TEST {
		t.Fatalf("get = %v", g)
	}

	cour, err := c.UpsertCourier(ctx, sc, app.CourierUpsert{ExternalID: "dt-simulator", DisplayName: "Sim Courier", Vehicle: "e_bike"})
	if err != nil || cour != (domain.Courier{ID: "cou_sim", ExternalID: "dt-simulator", DisplayName: "Sim Courier", Vehicle: "e_bike"}) {
		t.Fatalf("upsert = %+v, %v", cour, err)
	}
	if err := c.Assign(ctx, sc, "del_1", "cou_sim"); err != nil {
		t.Fatal(err)
	}
	if a := f.reqs[2].(*deliveriesv1.AssignCourierRequest); a.GetId() != "del_1" || a.GetCourierId() != "cou_sim" {
		t.Fatalf("assign = %v", a)
	}
	if err := c.Report(ctx, sc, "del_1", app.StatusReport{EventID: "sim_1:pickup_confirmed:partner", Type: "pickup_confirmed", Actor: "partner", By: "store"}); err != nil {
		t.Fatal(err)
	}
	r := f.reqs[3].(*deliveriesv1.ReportEventRequest)
	if r.GetId() != "del_1" || r.GetEventId() != "sim_1:pickup_confirmed:partner" || r.GetType() != "pickup_confirmed" ||
		r.GetBy() != "store" || r.GetActor() != "partner" {
		t.Fatalf("report = %v", r)
	}
}

func TestClientMapsErrors(t *testing.T) {
	ctx := context.Background()
	f := &fakeDeliveries{}
	c := Wrap(f)
	f.fail = failWith(connect.CodeNotFound, "not_found")
	if _, err := c.Get(ctx, sc, "del_1"); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("not found = %v", err)
	}
	f.fail = failWith(connect.CodeFailedPrecondition, "invalid_transition")
	if err := c.Report(ctx, sc, "del_1", app.StatusReport{EventID: "e", Type: "delivered", Actor: "courier"}); !errors.Is(err, app.ErrSimulationState) {
		t.Fatalf("invalid transition = %v", err)
	}
	for _, code := range []string{"validation_failed", "courier_not_found", "model_not_allowed"} {
		f.fail = failWith(connect.CodeFailedPrecondition, code)
		var rej *app.RejectedError
		if err := c.Assign(ctx, sc, "del_1", "cou_1"); !errors.As(err, &rej) || rej.Code != code {
			t.Fatalf("%s = %v, want a RejectedError", code, err)
		}
	}
	f.fail = failWith(connect.CodeUnavailable, "")
	if err := c.Assign(ctx, sc, "del_1", "cou_1"); err == nil || errors.Is(err, app.ErrNotFound) || errors.Is(err, app.ErrSimulationState) ||
		errors.As(err, new(*app.RejectedError)) {
		t.Fatalf("unavailable = %v", err)
	}
}

func TestNewChecksTheURL(t *testing.T) {
	if _, err := New("ftp://x", "a-token-of-at-least-thirty-two-bytes!!", nil); err == nil {
		t.Fatal("ftp accepted")
	}
	if _, err := New("http://deliveries:8081", "short", nil); err == nil {
		t.Fatal("short token accepted")
	}
	if _, err := New("http://deliveries:8081", "a-token-of-at-least-thirty-two-bytes!!", nil); err != nil {
		t.Fatal(err)
	}
}
