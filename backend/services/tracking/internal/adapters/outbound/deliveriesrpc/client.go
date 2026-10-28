// Package deliveriesrpc is the simulator's client of the deliveries internal
// RPC (dt.deliveries.v1.DeliveriesService): the same calls the partner path
// makes through the gateway, over mTLS and the internal bearer token.
package deliveriesrpc

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	deliveriesv1 "github.com/raduolivas/dt/backend/gen/dt/deliveries/v1"
	"github.com/raduolivas/dt/backend/gen/dt/deliveries/v1/deliveriesv1connect"
	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/rpcauth"
	"github.com/raduolivas/dt/backend/services/tracking/internal/app"
	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

// CallTimeout bounds every call to deliveries.
const CallTimeout = 5 * time.Second

// Client implements app.Deliveries.
type Client struct {
	c deliveriesv1connect.DeliveriesServiceClient
}

var _ app.Deliveries = (*Client)(nil)

// New builds the client for baseURL. With a TLS config the URL must be https
// and the client presents its certificate (mTLS); without one it must be
// plain http (local development). Errors never echo the token.
func New(baseURL, token string, tlsCfg *tls.Config) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("DELIVERIES_RPC_URL must be an http(s) URL")
	}
	if tlsCfg != nil && u.Scheme != "https" {
		return nil, errors.New("DELIVERIES_RPC_URL must be https when INTERNAL_RPC_TLS_* is set")
	}
	auth, err := rpcauth.NewClientInterceptor(token)
	if err != nil {
		return nil, fmt.Errorf("INTERNAL_RPC_TOKEN: %w", err)
	}
	hc := &http.Client{Timeout: CallTimeout}
	if tlsCfg != nil {
		hc.Transport = &http.Transport{TLSClientConfig: tlsCfg, ForceAttemptHTTP2: true,
			MaxIdleConnsPerHost: 4, IdleConnTimeout: 90 * time.Second}
	}
	return Wrap(deliveriesv1connect.NewDeliveriesServiceClient(connect.NewClient(connecthttp.NewTransport(hc, baseURL), auth))), nil
}

// Wrap adapts a generated client.
func Wrap(c deliveriesv1connect.DeliveriesServiceClient) *Client { return &Client{c: c} }

func scope(s app.Scope) *deliveriesv1.Scope {
	env := eventsv1.Environment_ENVIRONMENT_TEST
	if s.Env == "live" {
		env = eventsv1.Environment_ENVIRONMENT_LIVE
	}
	return &deliveriesv1.Scope{TenantId: s.TenantID, Environment: env}
}

// rejections are the deliveries problem codes of a refused call (a
// 4xx-class answer), passed on to the partner as they are.
var rejections = map[string]bool{
	"validation_failed": true, "external_id_conflict": true, "dropoff_locked": true,
	"delivery_active": true, "model_not_allowed": true, "courier_not_found": true,
}

// call runs fn with a client call context (the auth interceptor and the
// dt-code trailer live on it) and maps a failure: not_found is
// app.ErrNotFound, invalid_transition is app.ErrSimulationState (the
// delivery moved on under the simulation), another refusal is an
// *app.RejectedError with its code, and anything else (transport, an
// unavailable service) is returned wrapped.
func call[T any](ctx context.Context, op string, fn func(context.Context) (T, error)) (T, error) {
	ctx, info := connect.NewClientContext(ctx)
	res, err := fn(ctx)
	if err == nil {
		return res, nil
	}
	switch code := rpcauth.CodeFrom(info); {
	case code == "not_found" || (code == "" && connect.CodeOf(err) == connect.CodeNotFound):
		err = fmt.Errorf("deliveries %s: %w", op, app.ErrNotFound)
	case code == "invalid_transition":
		err = fmt.Errorf("deliveries %s: %w", op, app.ErrSimulationState)
	case rejections[code]:
		err = &app.RejectedError{Code: code, Err: fmt.Errorf("deliveries %s: %w", op, err)}
	default:
		err = fmt.Errorf("deliveries %s: %w", op, err)
	}
	return res, err
}

// Get implements app.Deliveries.
func (c *Client) Get(ctx context.Context, s app.Scope, id string) (app.SimDelivery, error) {
	res, err := call(ctx, "get", func(ctx context.Context) (*deliveriesv1.GetDeliveryResponse, error) {
		return c.c.GetDelivery(ctx, &deliveriesv1.GetDeliveryRequest{Scope: scope(s), Id: id})
	})
	if err != nil {
		return app.SimDelivery{}, err
	}
	d := res.GetDelivery()
	out := app.SimDelivery{
		ID: d.GetId(), Model: domain.Model(d.GetDeliveryModel()), Status: domain.Status(d.GetStatus()),
		PickupPolicy: d.GetPickupPolicy(),
		Pickup:       domain.LatLng{Lat: d.GetPickup().GetLocation().GetLat(), Lng: d.GetPickup().GetLocation().GetLng()},
		Dropoff:      domain.LatLng{Lat: d.GetDropoff().GetLocation().GetLat(), Lng: d.GetDropoff().GetLocation().GetLng()},
	}
	if cr := d.GetCourier(); cr != nil {
		out.Courier = &domain.Courier{ID: cr.GetId(), ExternalID: cr.GetExternalId(), DisplayName: cr.GetDisplayName(), Vehicle: cr.GetVehicle()}
	}
	return out, nil
}

// UpsertCourier implements app.Deliveries.
func (c *Client) UpsertCourier(ctx context.Context, s app.Scope, in app.CourierUpsert) (domain.Courier, error) {
	res, err := call(ctx, "upsert courier", func(ctx context.Context) (*deliveriesv1.UpsertCourierResponse, error) {
		return c.c.UpsertCourier(ctx, &deliveriesv1.UpsertCourierRequest{Scope: scope(s), ExternalId: in.ExternalID,
			DisplayName: in.DisplayName, Vehicle: in.Vehicle})
	})
	if err != nil {
		return domain.Courier{}, err
	}
	cr := res.GetCourier()
	return domain.Courier{ID: cr.GetId(), ExternalID: cr.GetExternalId(), DisplayName: cr.GetDisplayName(),
		Vehicle: cr.GetVehicle(), PhotoURL: cr.PhotoUrl}, nil
}

// Assign implements app.Deliveries.
func (c *Client) Assign(ctx context.Context, s app.Scope, deliveryID, courierID string) error {
	_, err := call(ctx, "assign", func(ctx context.Context) (*deliveriesv1.AssignCourierResponse, error) {
		return c.c.AssignCourier(ctx, &deliveriesv1.AssignCourierRequest{Scope: scope(s), Id: deliveryID, CourierId: courierID})
	})
	return err
}

// Report implements app.Deliveries.
func (c *Client) Report(ctx context.Context, s app.Scope, deliveryID string, r app.StatusReport) error {
	_, err := call(ctx, "report "+r.Type, func(ctx context.Context) (*deliveriesv1.ReportEventResponse, error) {
		return c.c.ReportEvent(ctx, &deliveriesv1.ReportEventRequest{Scope: scope(s), Id: deliveryID,
			EventId: r.EventID, Type: r.Type, By: r.By, Actor: r.Actor})
	})
	return err
}
