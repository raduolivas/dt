// Package deliveries reads couriers from the deliveries service's internal
// RPC.
package deliveries

import (
	"context"
	"fmt"

	"connectrpc.com/connect/v2"

	deliveriesv1 "github.com/raduolivas/dt/backend/gen/dt/deliveries/v1"
	"github.com/raduolivas/dt/backend/gen/dt/deliveries/v1/deliveriesv1connect"
	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

// Couriers is app.CourierLookup over DeliveriesService.GetCourier.
type Couriers struct {
	c deliveriesv1connect.DeliveriesServiceClient
}

var _ app.CourierLookup = (*Couriers)(nil)

// NewCouriers returns a CourierLookup over c.
func NewCouriers(c deliveriesv1connect.DeliveriesServiceClient) *Couriers { return &Couriers{c: c} }

// CourierExists looks courierID up in p's tenant and environment, with p's
// credential as the actor. NotFound (courier_not_found: absent, or in
// another tenant or environment) is app.ErrCourierNotFound.
func (c *Couriers) CourierExists(ctx context.Context, p domain.Principal, courierID string) error {
	env := eventsv1.Environment_ENVIRONMENT_TEST
	if p.Env == domain.EnvLive {
		env = eventsv1.Environment_ENVIRONMENT_LIVE
	}
	_, err := c.c.GetCourier(ctx, &deliveriesv1.GetCourierRequest{
		Scope: &deliveriesv1.Scope{TenantId: p.TenantID, Environment: env, ActorId: p.CredentialID},
		Id:    courierID,
	})
	if connect.CodeOf(err) == connect.CodeNotFound {
		return app.ErrCourierNotFound
	}
	if err != nil {
		return fmt.Errorf("deliveries GetCourier: %w", err)
	}
	return nil
}
