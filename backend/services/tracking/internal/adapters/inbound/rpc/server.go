// Package rpc serves the internal tracking Connect API
// (dt.tracking.v1.TrackingService). Every RPC needs the internal bearer
// token; the API is never routed publicly. Simulate answers Unimplemented
// when no simulator is mounted.
package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"

	"google.golang.org/protobuf/types/known/timestamppb"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	trackingv1 "github.com/raduolivas/dt/backend/gen/dt/tracking/v1"
	"github.com/raduolivas/dt/backend/gen/dt/tracking/v1/trackingv1connect"
	"github.com/raduolivas/dt/backend/pkg/ids"
	"github.com/raduolivas/dt/backend/pkg/rpcauth"
	"github.com/raduolivas/dt/backend/services/tracking/internal/app"
	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

// readMaxBytes bounds each request (50 fixes are a few KB).
const readMaxBytes = 64 << 10

// MaxFixes is the most fixes one IngestLocations call may carry.
const MaxFixes = 50

// Simulator starts sandbox simulations; it is satisfied by *app.Simulator.
type Simulator interface {
	Start(ctx context.Context, sc app.Scope, deliveryID string, speedFactor float64, gap *app.SignalGap) (string, error)
}

// Mount registers the TrackingService on mux, behind the token check. sim
// may be nil: Simulate then answers Unimplemented.
func Mount(mux *http.ServeMux, svc *app.Service, sim Simulator, token string, log *slog.Logger) error {
	if svc == nil || log == nil {
		return errors.New("rpc: service and logger are required")
	}
	auth, err := rpcauth.NewServerInterceptor(token)
	if err != nil {
		return err
	}
	srv := connect.NewServer(auth)
	trackingv1connect.RegisterTrackingServiceHandler(srv, &Server{svc: svc, sim: sim, log: log})
	connecthttp.Mount(mux, srv, connecthttp.WithReadMaxBytes(readMaxBytes))
	return nil
}

// Server implements trackingv1connect.TrackingServiceHandler. Log lines
// carry ids and counts only, never positions.
type Server struct {
	trackingv1connect.UnimplementedTrackingServiceHandler
	svc *app.Service
	sim Simulator // nil: Simulate is Unimplemented
	log *slog.Logger
}

var _ trackingv1connect.TrackingServiceHandler = (*Server)(nil)

func invalid(ctx context.Context, msg string) error {
	rpcauth.SetCode(ctx, "validation_failed")
	return connect.NewError(connect.CodeInvalidArgument, msg)
}

func toScope(ctx context.Context, s *trackingv1.Scope) (app.Scope, error) {
	if s.GetTenantId() == "" {
		return app.Scope{}, invalid(ctx, "scope.tenant_id is required")
	}
	switch s.GetEnvironment() {
	case eventsv1.Environment_ENVIRONMENT_TEST:
		return app.Scope{TenantID: s.GetTenantId(), Env: "test"}, nil
	case eventsv1.Environment_ENVIRONMENT_LIVE:
		return app.Scope{TenantID: s.GetTenantId(), Env: "live"}, nil
	}
	return app.Scope{}, invalid(ctx, "scope.environment must be test or live")
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// IngestLocations attaches a courier's fixes to its active deliveries.
func (s *Server) IngestLocations(ctx context.Context, req *trackingv1.IngestRequest) (*trackingv1.IngestResponse, error) {
	sc, err := toScope(ctx, req.GetScope())
	if err != nil {
		return nil, err
	}
	if !ids.Valid(ids.Courier, req.GetCourierId()) {
		return nil, invalid(ctx, "courier_id must be a cou_ id")
	}
	switch req.GetSource() {
	case "relay", "simulator":
	default:
		return nil, invalid(ctx, "source must be relay or simulator")
	}
	if n := len(req.GetFixes()); n == 0 || n > MaxFixes {
		return nil, invalid(ctx, fmt.Sprintf("fixes must hold 1 to %d entries", MaxFixes))
	}
	fixes := make([]domain.Fix, 0, len(req.GetFixes()))
	for _, f := range req.GetFixes() {
		if f.GetT() == nil || !finite(f.GetLat()) || !finite(f.GetLng()) || !finite(f.GetAccuracyM()) ||
			(f.SpeedMps != nil && !finite(f.GetSpeedMps())) || (f.HeadingDeg != nil && !finite(f.GetHeadingDeg())) {
			return nil, invalid(ctx, "fixes: t is required and numbers must be finite")
		}
		fx := domain.Fix{T: f.GetT().AsTime(), Pos: domain.LatLng{Lat: f.GetLat(), Lng: f.GetLng()}, AccuracyM: f.GetAccuracyM()}
		if f.SpeedMps != nil {
			v := f.GetSpeedMps()
			fx.SpeedMps = &v
		}
		if f.HeadingDeg != nil {
			v := f.GetHeadingDeg()
			fx.HeadingDeg = &v
		}
		fixes = append(fixes, fx)
	}
	acc, drop, err := s.svc.Ingest(ctx, sc, req.GetCourierId(), fixes, req.GetSource())
	switch {
	case errors.Is(err, app.ErrCourierNotFound):
		rpcauth.SetCode(ctx, "not_found")
		return nil, connect.NewError(connect.CodeNotFound, "courier not found")
	case errors.Is(err, context.Canceled):
		return nil, connect.NewError(connect.CodeCanceled, "canceled").WithCause(err)
	case errors.Is(err, context.DeadlineExceeded):
		return nil, connect.NewError(connect.CodeDeadlineExceeded, "deadline exceeded").WithCause(err)
	case err != nil:
		s.log.ErrorContext(ctx, "rpc_internal_error", "rpc", "IngestLocations", "error", err)
		rpcauth.SetCode(ctx, "tracking_unavailable")
		return nil, connect.NewError(connect.CodeUnavailable, "tracking unavailable").WithCause(err)
	}
	return &trackingv1.IngestResponse{Accepted: int32(acc), Dropped: int32(drop)}, nil //nolint:gosec // at most MaxFixes
}

// Simulate starts a sandbox simulation of a test delivery (design §6). The
// speed factor (0 = 1) must be 1 to 20 and the gap values 0 to 3600; a live
// scope is refused (the gateway answers live keys 403 before this).
func (s *Server) Simulate(ctx context.Context, req *trackingv1.SimulateRequest) (*trackingv1.SimulateResponse, error) {
	if s.sim == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, "simulation is not available")
	}
	sc, err := toScope(ctx, req.GetScope())
	if err != nil {
		return nil, err
	}
	if sc.Env != "test" {
		return nil, invalid(ctx, "simulate runs in the test environment only")
	}
	if !ids.Valid(ids.Delivery, req.GetDeliveryId()) {
		return nil, invalid(ctx, "delivery_id must be a del_ id")
	}
	speed := req.GetSpeedFactor()
	if speed == 0 {
		speed = 1
	}
	if !(speed >= 1 && speed <= 20) {
		return nil, invalid(ctx, "speed_factor must be 1 to 20")
	}
	var gap *app.SignalGap
	if g := req.GetGap(); g != nil {
		if g.GetAfterS() < 0 || g.GetAfterS() > 3600 || g.GetDurationS() < 0 || g.GetDurationS() > 3600 {
			return nil, invalid(ctx, "gap values must be 0 to 3600")
		}
		gap = &app.SignalGap{AfterS: int(g.GetAfterS()), DurationS: int(g.GetDurationS())}
	}
	id, err := s.sim.Start(ctx, sc, req.GetDeliveryId(), speed, gap)
	var rej *app.RejectedError
	switch {
	case errors.Is(err, app.ErrSimulationState):
		rpcauth.SetCode(ctx, "simulation_state")
		return nil, connect.NewError(connect.CodeFailedPrecondition, "the delivery is final or already being simulated")
	case errors.As(err, &rej):
		// Deliveries refused a call (e.g. courier_not_found on the
		// assignment): the partner gets that answer, not a 503.
		rpcauth.SetCode(ctx, rej.Code)
		code := connect.CodeFailedPrecondition
		if rej.Code == "validation_failed" {
			code = connect.CodeInvalidArgument
		}
		return nil, connect.NewError(code, "deliveries refused the simulation")
	}
	if err != nil {
		return nil, s.fail(ctx, "Simulate", err)
	}
	return &trackingv1.SimulateResponse{SimulationId: id}, nil
}

// GetSnapshot returns the customer's viewer snapshot as the JSON the public
// API and the streams carry (domain.Snapshot). A delivery unknown in the
// scope is NotFound, whoever else owns it.
func (s *Server) GetSnapshot(ctx context.Context, req *trackingv1.GetSnapshotRequest) (*trackingv1.GetSnapshotResponse, error) {
	sc, err := toScope(ctx, req.GetScope())
	if err != nil {
		return nil, err
	}
	if !ids.Valid(ids.Delivery, req.GetDeliveryId()) {
		return nil, invalid(ctx, "delivery_id must be a del_ id")
	}
	snap, err := s.svc.ViewerSnapshot(ctx, sc, req.GetDeliveryId())
	if err == nil {
		var b []byte
		if b, err = json.Marshal(snap); err == nil {
			return &trackingv1.GetSnapshotResponse{SnapshotJson: b}, nil
		}
	}
	return nil, s.fail(ctx, "GetSnapshot", err)
}

// GetPartnerView returns the partner's live view: the delivery state without
// the visibility filter. A delivery unknown in the scope is NotFound.
func (s *Server) GetPartnerView(ctx context.Context, req *trackingv1.GetPartnerViewRequest) (*trackingv1.PartnerView, error) {
	sc, err := toScope(ctx, req.GetScope())
	if err != nil {
		return nil, err
	}
	if !ids.Valid(ids.Delivery, req.GetDeliveryId()) {
		return nil, invalid(ctx, "delivery_id must be a del_ id")
	}
	v, err := s.svc.PartnerView(ctx, sc, req.GetDeliveryId())
	if err != nil {
		return nil, s.fail(ctx, "GetPartnerView", err)
	}
	return partnerViewProto(v), nil
}

// ListTrail returns a page of the delivery's fixes, oldest first.
func (s *Server) ListTrail(ctx context.Context, req *trackingv1.ListTrailRequest) (*trackingv1.TrailPage, error) {
	sc, err := toScope(ctx, req.GetScope())
	if err != nil {
		return nil, err
	}
	if !ids.Valid(ids.Delivery, req.GetDeliveryId()) {
		return nil, invalid(ctx, "delivery_id must be a del_ id")
	}
	pg, err := s.svc.ListTrail(ctx, sc, req.GetDeliveryId(), req.GetStartingAfter(), int(req.GetLimit()))
	if err != nil {
		return nil, s.fail(ctx, "ListTrail", err)
	}
	out := &trackingv1.TrailPage{HasMore: pg.HasMore, RawAvailable: pg.RawAvailable, NextCursor: pg.NextCursor,
		Fixes: make([]*trackingv1.Fix, len(pg.Fixes)), Simplified: latLngs(pg.Simplified)}
	for i, f := range pg.Fixes {
		out.Fixes[i] = &trackingv1.Fix{T: timestamppb.New(f.T), Lat: f.Pos.Lat, Lng: f.Pos.Lng,
			AccuracyM: f.AccuracyM, SpeedMps: f.SpeedMps, HeadingDeg: f.HeadingDeg, Source: f.Source}
	}
	return out, nil
}

// ListProofPoints returns the delivery's proof points, oldest first.
func (s *Server) ListProofPoints(ctx context.Context, req *trackingv1.ListProofPointsRequest) (*trackingv1.ProofPoints, error) {
	sc, err := toScope(ctx, req.GetScope())
	if err != nil {
		return nil, err
	}
	if !ids.Valid(ids.Delivery, req.GetDeliveryId()) {
		return nil, invalid(ctx, "delivery_id must be a del_ id")
	}
	pts, err := s.svc.ListProofPoints(ctx, sc, req.GetDeliveryId())
	if err != nil {
		return nil, s.fail(ctx, "ListProofPoints", err)
	}
	out := &trackingv1.ProofPoints{Points: make([]*trackingv1.ProofPoint, len(pts))}
	for i, p := range pts {
		pp := &trackingv1.ProofPoint{Status: p.Status, At: timestamppb.New(p.At), Actor: p.Actor}
		if p.Fix != nil {
			acc := p.Fix.AccuracyM
			pp.Position = &eventsv1.LatLng{Lat: p.Fix.Pos.Lat, Lng: p.Fix.Pos.Lng}
			pp.PositionAt = timestamppb.New(p.Fix.T)
			pp.AccuracyM = &acc
		}
		out.Points[i] = pp
	}
	return out, nil
}

func latLngs(pts []domain.LatLng) []*eventsv1.LatLng {
	out := make([]*eventsv1.LatLng, len(pts))
	for i, p := range pts {
		out[i] = &eventsv1.LatLng{Lat: p.Lat, Lng: p.Lng}
	}
	return out
}

func pairLatLngs(ps []domain.Pair) []*eventsv1.LatLng {
	out := make([]*eventsv1.LatLng, len(ps))
	for i, p := range ps {
		out[i] = &eventsv1.LatLng{Lat: p[0], Lng: p[1]}
	}
	return out
}

func partnerViewProto(v domain.PartnerViewDTO) *trackingv1.PartnerView {
	out := &trackingv1.PartnerView{
		DeliveryId: v.DeliveryID, DeliveryModel: string(v.DeliveryModel), Status: string(v.Status),
		Pickup:     &eventsv1.LatLng{Lat: v.Pickup.Location.Lat, Lng: v.Pickup.Location.Lng},
		PickupName: v.Pickup.Name,
		Dropoff:    &eventsv1.LatLng{Lat: v.Dropoff.Location.Lat, Lng: v.Dropoff.Location.Lng}, DropoffLabel: v.Dropoff.Label,
		Route: &trackingv1.RouteLegs{ToPickup: pairLatLngs(v.Route.ToPickup), HasToPickup: v.Route.ToPickup != nil,
			ToDropoff: pairLatLngs(v.Route.ToDropoff)},
		EtaState: string(v.EtaState), EtaQuality: v.EtaQuality,
		DistanceRemainingM: v.DistanceRemainingM, Progress: v.Progress,
		UpdatedAt: timestamppb.New(v.UpdatedAt), Version: v.Version,
	}
	if v.EtaSeconds != nil {
		e := int32(min(*v.EtaSeconds, math.MaxInt32)) //nolint:gosec // clamped
		out.EtaSeconds = &e
	}
	if w := v.EtaWindow; w != nil {
		out.EtaWindow = &trackingv1.EtaWindow{MinSeconds: int32(min(w.MinSeconds, math.MaxInt32)), //nolint:gosec // clamped
			MaxSeconds: int32(min(w.MaxSeconds, math.MaxInt32))} //nolint:gosec // clamped
	}
	for _, e := range v.Timeline {
		out.Timeline = append(out.Timeline, &trackingv1.TimelineEntry{Status: string(e.Status), At: timestamppb.New(e.At)})
	}
	if c := v.Courier; c != nil {
		pc := &trackingv1.PartnerCourier{Id: c.ID, ExternalId: c.ExternalID, DisplayName: c.DisplayName,
			Vehicle: c.Vehicle, PhotoUrl: c.PhotoURL, HeadingDeg: c.HeadingDeg, SpeedKmh: c.SpeedKmh, Signal: string(c.Signal)}
		if c.Position != nil {
			pc.Position = &eventsv1.LatLng{Lat: c.Position.Lat, Lng: c.Position.Lng}
		}
		if c.PositionAt != nil {
			pc.PositionAt = timestamppb.New(*c.PositionAt)
		}
		out.Courier = pc
	}
	return out
}

// fail maps a use-case error: not found, an invalid limit or cursor, a
// cancelled or expired call, or (logged) tracking_unavailable.
func (s *Server) fail(ctx context.Context, rpcName string, err error) error {
	switch {
	case errors.Is(err, app.ErrInvalidArgument):
		return invalid(ctx, err.Error())
	case errors.Is(err, app.ErrNotFound):
		rpcauth.SetCode(ctx, "not_found")
		return connect.NewError(connect.CodeNotFound, "delivery not found")
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, "canceled").WithCause(err)
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, "deadline exceeded").WithCause(err)
	}
	s.log.ErrorContext(ctx, "rpc_internal_error", "rpc", rpcName, "error", err)
	rpcauth.SetCode(ctx, "tracking_unavailable")
	return connect.NewError(connect.CodeUnavailable, "tracking unavailable").WithCause(err)
}
