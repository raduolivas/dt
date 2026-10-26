// Package rpc serves the internal tracking Connect API
// (dt.tracking.v1.TrackingService). Every RPC needs the internal bearer
// token; the API is never routed publicly. Only IngestLocations is
// implemented so far; the others answer Unimplemented.
package rpc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"

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

// Mount registers the TrackingService on mux, behind the token check.
func Mount(mux *http.ServeMux, svc *app.Service, token string, log *slog.Logger) error {
	if svc == nil || log == nil {
		return errors.New("rpc: service and logger are required")
	}
	auth, err := rpcauth.NewServerInterceptor(token)
	if err != nil {
		return err
	}
	srv := connect.NewServer(auth)
	trackingv1connect.RegisterTrackingServiceHandler(srv, &Server{svc: svc, log: log})
	connecthttp.Mount(mux, srv, connecthttp.WithReadMaxBytes(readMaxBytes))
	return nil
}

// Server implements trackingv1connect.TrackingServiceHandler. Log lines
// carry ids and counts only, never positions.
type Server struct {
	trackingv1connect.UnimplementedTrackingServiceHandler
	svc *app.Service
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
