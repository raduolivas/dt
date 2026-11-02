package deliveries_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect/v2"

	deliveriesv1 "github.com/raduolivas/dt/backend/gen/dt/deliveries/v1"
	"github.com/raduolivas/dt/backend/gen/dt/deliveries/v1/deliveriesv1connect"
	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/services/gateway/internal/adapters/outbound/deliveries"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

type fakeClient struct {
	deliveriesv1connect.DeliveriesServiceClient
	got *deliveriesv1.GetCourierRequest
	err error
}

func (f *fakeClient) GetCourier(_ context.Context, req *deliveriesv1.GetCourierRequest) (*deliveriesv1.GetCourierResponse, error) {
	f.got = req
	if f.err != nil {
		return nil, f.err
	}
	return &deliveriesv1.GetCourierResponse{Courier: &deliveriesv1.Courier{Id: req.GetId()}}, nil
}

func TestCourierExists(t *testing.T) {
	p := domain.Principal{TenantID: "ten_a", Env: domain.EnvLive, CredentialID: "ak_1"}
	f := &fakeClient{}
	c := deliveries.NewCouriers(f)
	if err := c.CourierExists(context.Background(), p, "cou_1"); err != nil {
		t.Fatal(err)
	}
	s := f.got.GetScope()
	if f.got.GetId() != "cou_1" || s.GetTenantId() != "ten_a" || s.GetEnvironment() != eventsv1.Environment_ENVIRONMENT_LIVE ||
		s.GetActorId() != "ak_1" {
		t.Fatalf("request = %v", f.got)
	}
	f.err = connect.NewError(connect.CodeNotFound, "courier_not_found")
	if err := c.CourierExists(context.Background(), p, "cou_1"); !errors.Is(err, app.ErrCourierNotFound) {
		t.Fatalf("not found: %v", err)
	}
	f.err = connect.NewError(connect.CodeUnavailable, "down")
	if err := c.CourierExists(context.Background(), p, "cou_1"); err == nil || errors.Is(err, app.ErrCourierNotFound) {
		t.Fatalf("unavailable: %v", err)
	}
}
