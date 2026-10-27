package rpc_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	trackingv1 "github.com/raduolivas/dt/backend/gen/dt/tracking/v1"
	"github.com/raduolivas/dt/backend/gen/dt/tracking/v1/trackingv1connect"
	"github.com/raduolivas/dt/backend/pkg/rpcauth"
	"github.com/raduolivas/dt/backend/services/tracking/internal/adapters/inbound/rpc"
	"github.com/raduolivas/dt/backend/services/tracking/internal/app"
)

type simCall struct {
	sc    app.Scope
	id    string
	speed float64
	gap   *app.SignalGap
}

type fakeSim struct {
	mu    sync.Mutex
	calls []simCall
	err   error
}

func (f *fakeSim) Start(_ context.Context, sc app.Scope, id string, speed float64, gap *app.SignalGap) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, simCall{sc, id, speed, gap})
	if f.err != nil {
		return "", f.err
	}
	return "sim_01j9z3k8m2q4r6s8t0v2w4x6y8", nil
}

func serveSim(t *testing.T, sim rpc.Simulator) trackingv1connect.TrackingServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	if err := rpc.Mount(mux, &app.Service{}, sim, testToken, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return trackingv1connect.NewTrackingServiceClient(connect.NewClient(connecthttp.NewTransport(bearer{srv.Client()}, srv.URL)))
}

func simReq(env eventsv1.Environment) *trackingv1.SimulateRequest {
	return &trackingv1.SimulateRequest{
		Scope:      &trackingv1.Scope{TenantId: "ten_a", Environment: env, ActorId: "ak_1"},
		DeliveryId: delA, SpeedFactor: 10, Gap: &trackingv1.SignalGap{AfterS: 30, DurationS: 90},
	}
}

// simulate calls Simulate and returns the dt-code trailer of a failure.
func simulate(c trackingv1connect.TrackingServiceClient, r *trackingv1.SimulateRequest) (*trackingv1.SimulateResponse, string, error) {
	ctx, info := connect.NewClientContext(context.Background())
	res, err := c.Simulate(ctx, r)
	return res, rpcauth.CodeFrom(info), err
}

func TestSimulateStartsASimulation(t *testing.T) {
	sim := &fakeSim{}
	c := serveSim(t, sim)
	res, _, err := simulate(c, simReq(eventsv1.Environment_ENVIRONMENT_TEST))
	if err != nil {
		t.Fatal(err)
	}
	if res.GetSimulationId() != "sim_01j9z3k8m2q4r6s8t0v2w4x6y8" {
		t.Fatalf("response = %v", res)
	}
	got := sim.calls[0]
	if got.sc != (app.Scope{TenantID: "ten_a", Env: "test"}) || got.id != delA || got.speed != 10 ||
		got.gap == nil || *got.gap != (app.SignalGap{AfterS: 30, DurationS: 90}) {
		t.Fatalf("Start(%+v)", got)
	}
	// Defaults: speed 0 is 1, no gap.
	r := simReq(eventsv1.Environment_ENVIRONMENT_TEST)
	r.SpeedFactor, r.Gap = 0, nil
	if _, _, err := simulate(c, r); err != nil {
		t.Fatal(err)
	}
	if got := sim.calls[1]; got.speed != 1 || got.gap != nil {
		t.Fatalf("defaults: Start(%+v)", got)
	}
}

func TestSimulateRPCErrors(t *testing.T) {
	sim := &fakeSim{}
	c := serveSim(t, sim)
	for name, mod := range map[string]func(*trackingv1.SimulateRequest){
		"live":       func(r *trackingv1.SimulateRequest) { r.Scope.Environment = eventsv1.Environment_ENVIRONMENT_LIVE },
		"bad id":     func(r *trackingv1.SimulateRequest) { r.DeliveryId = "cou_x" },
		"speed":      func(r *trackingv1.SimulateRequest) { r.SpeedFactor = 21 },
		"slow":       func(r *trackingv1.SimulateRequest) { r.SpeedFactor = 0.5 },
		"gap after":  func(r *trackingv1.SimulateRequest) { r.Gap.AfterS = 3601 },
		"gap length": func(r *trackingv1.SimulateRequest) { r.Gap.DurationS = -1 },
		"no scope":   func(r *trackingv1.SimulateRequest) { r.Scope = nil },
	} {
		r := simReq(eventsv1.Environment_ENVIRONMENT_TEST)
		mod(r)
		if _, code, err := simulate(c, r); connect.CodeOf(err) != connect.CodeInvalidArgument || code != "validation_failed" {
			t.Fatalf("%s: err %v code %q", name, err, code)
		}
	}
	if len(sim.calls) != 0 {
		t.Fatalf("invalid requests reached the simulator: %+v", sim.calls)
	}

	for _, c2 := range []struct {
		err  error
		code connect.Code
		dt   string
	}{
		{app.ErrSimulationState, connect.CodeFailedPrecondition, "simulation_state"},
		{app.ErrNotFound, connect.CodeNotFound, "not_found"},
		{errors.New("deliveries down"), connect.CodeUnavailable, "tracking_unavailable"},
	} {
		sim.err = c2.err
		if _, code, err := simulate(c, simReq(eventsv1.Environment_ENVIRONMENT_TEST)); connect.CodeOf(err) != c2.code || code != c2.dt {
			t.Fatalf("%v: err %v code %q", c2.err, err, code)
		}
	}

	// Without a simulator Simulate is not served.
	none := serveSim(t, nil)
	if _, _, err := simulate(none, simReq(eventsv1.Environment_ENVIRONMENT_TEST)); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("no simulator: %v", err)
	}
}
