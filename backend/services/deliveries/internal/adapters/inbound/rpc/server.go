// Package rpc serves the internal deliveries Connect API
// (dt.deliveries.v1.DeliveriesService) over the app use cases. Every RPC
// needs the internal bearer token; the API is never routed publicly.
package rpc

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	deliveriesv1 "github.com/raduolivas/dt/backend/gen/dt/deliveries/v1"
	"github.com/raduolivas/dt/backend/gen/dt/deliveries/v1/deliveriesv1connect"
	"github.com/raduolivas/dt/backend/pkg/rpcauth"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/app"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/domain"
)

// readMaxBytes bounds each request message (metadata maps are the largest
// part, at most 50 keys of 500 chars).
const readMaxBytes = 256 << 10

// Mount registers the DeliveriesService on mux, behind the token check.
func Mount(mux *http.ServeMux, svc *app.Service, token string, log *slog.Logger) error {
	if svc == nil || log == nil {
		return errors.New("rpc: service and logger are required")
	}
	auth, err := rpcauth.NewServerInterceptor(token)
	if err != nil {
		return err
	}
	srv := connect.NewServer(auth)
	deliveriesv1connect.RegisterDeliveriesServiceHandler(srv, &Server{svc: svc, log: log})
	connecthttp.Mount(mux, srv, connecthttp.WithReadMaxBytes(readMaxBytes))
	return nil
}

// Server implements deliveriesv1connect.DeliveriesServiceHandler. Log lines
// carry ids and states only, never personal fields.
type Server struct {
	svc *app.Service
	log *slog.Logger
}

var _ deliveriesv1connect.DeliveriesServiceHandler = (*Server)(nil)

func (s *Server) fail(ctx context.Context, err error) error { return ToConnect(ctx, s.log, err) }

func (s *Server) logDelivery(ctx context.Context, msg string, sc app.Scope, d *domain.Delivery) {
	s.log.InfoContext(ctx, msg, slog.String("tenant_id", sc.TenantID), slog.String("environment", sc.Env),
		slog.String("delivery_id", d.ID), slog.String("status", string(d.Status)))
}

func (s *Server) CreateDelivery(ctx context.Context, req *deliveriesv1.CreateDeliveryRequest) (*deliveriesv1.CreateDeliveryResponse, error) {
	sc, err := toScope(req.GetScope())
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	in, err := toCreateInput(req)
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	d, err := s.svc.Create(ctx, sc, in)
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	s.logDelivery(ctx, "delivery_created", sc, d)
	return &deliveriesv1.CreateDeliveryResponse{Delivery: fromDelivery(d)}, nil
}

func (s *Server) GetDelivery(ctx context.Context, req *deliveriesv1.GetDeliveryRequest) (*deliveriesv1.GetDeliveryResponse, error) {
	sc, err := toScope(req.GetScope())
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	d, err := s.svc.Get(ctx, sc, req.GetId())
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	return &deliveriesv1.GetDeliveryResponse{Delivery: fromDelivery(d)}, nil
}

func (s *Server) ListDeliveries(ctx context.Context, req *deliveriesv1.ListDeliveriesRequest) (*deliveriesv1.ListDeliveriesResponse, error) {
	sc, err := toScope(req.GetScope())
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	ds, more, err := s.svc.List(ctx, sc, toFilter(req))
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	out := make([]*deliveriesv1.Delivery, len(ds))
	for i, d := range ds {
		out[i] = fromDelivery(d)
	}
	return &deliveriesv1.ListDeliveriesResponse{Data: out, HasMore: more}, nil
}

func (s *Server) UpdateDelivery(ctx context.Context, req *deliveriesv1.UpdateDeliveryRequest) (*deliveriesv1.UpdateDeliveryResponse, error) {
	sc, err := toScope(req.GetScope())
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	p, err := toPatch(req)
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	d, err := s.svc.Update(ctx, sc, req.GetId(), p)
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	s.logDelivery(ctx, "delivery_updated", sc, d)
	return &deliveriesv1.UpdateDeliveryResponse{Delivery: fromDelivery(d)}, nil
}

func (s *Server) AssignCourier(ctx context.Context, req *deliveriesv1.AssignCourierRequest) (*deliveriesv1.AssignCourierResponse, error) {
	sc, err := toScope(req.GetScope())
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	d, err := s.svc.Assign(ctx, sc, req.GetId(), req.GetCourierId())
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	s.logDelivery(ctx, "courier_assigned", sc, d)
	return &deliveriesv1.AssignCourierResponse{Delivery: fromDelivery(d)}, nil
}

func (s *Server) ReportEvent(ctx context.Context, req *deliveriesv1.ReportEventRequest) (*deliveriesv1.ReportEventResponse, error) {
	sc, err := toScope(req.GetScope())
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	rep, err := toReport(req)
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	d, outcome, err := s.svc.Report(ctx, sc, req.GetId(), rep)
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	s.log.InfoContext(ctx, "delivery_event_reported", slog.String("tenant_id", sc.TenantID),
		slog.String("environment", sc.Env), slog.String("delivery_id", d.ID),
		slog.String("type", req.GetType()), slog.String("outcome", string(outcome)))
	return &deliveriesv1.ReportEventResponse{Delivery: fromDelivery(d), Outcome: string(outcome)}, nil
}

func (s *Server) CancelDelivery(ctx context.Context, req *deliveriesv1.CancelDeliveryRequest) (*deliveriesv1.CancelDeliveryResponse, error) {
	sc, err := toScope(req.GetScope())
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	d, err := s.svc.Cancel(ctx, sc, req.GetId(), req.GetReason())
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	s.logDelivery(ctx, "delivery_cancelled", sc, d)
	return &deliveriesv1.CancelDeliveryResponse{Delivery: fromDelivery(d)}, nil
}

func (s *Server) ErasePersonalData(ctx context.Context, req *deliveriesv1.ErasePersonalDataRequest) (*deliveriesv1.ErasePersonalDataResponse, error) {
	sc, err := toScope(req.GetScope())
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	d, err := s.svc.ErasePersonalData(ctx, sc, req.GetId())
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	s.logDelivery(ctx, "delivery_personal_data_erased", sc, d)
	return &deliveriesv1.ErasePersonalDataResponse{Delivery: fromDelivery(d)}, nil
}

func (s *Server) UpsertCourier(ctx context.Context, req *deliveriesv1.UpsertCourierRequest) (*deliveriesv1.UpsertCourierResponse, error) {
	sc, err := toScope(req.GetScope())
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	c, created, err := s.svc.UpsertCourier(ctx, sc, toCourierInput(req))
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	s.log.InfoContext(ctx, "courier_upserted", slog.String("tenant_id", sc.TenantID),
		slog.String("environment", sc.Env), slog.String("courier_id", c.ID), slog.Bool("created", created))
	return &deliveriesv1.UpsertCourierResponse{Courier: fromCourier(c), Created: created}, nil
}
