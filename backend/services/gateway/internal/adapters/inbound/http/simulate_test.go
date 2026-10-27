package httpapi_test

import (
	"net/http"
	"testing"

	"connectrpc.com/connect/v2"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	trackingv1 "github.com/raduolivas/dt/backend/gen/dt/tracking/v1"
	"github.com/raduolivas/dt/backend/pkg/apikey"
	httpapi "github.com/raduolivas/dt/backend/services/gateway/internal/adapters/inbound/http"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

func simulateTarget() string { return "/v1/test/deliveries/" + delID + "/simulate" }

func TestSimulateForwardsToTracking(t *testing.T) {
	t.Parallel()
	g := newTrackGW(t, nil)
	req := g.req(http.MethodPost, simulateTarget(), `{"speed_factor":10,"signal_gap":{"after_s":30,"duration_s":90}}`)
	rec := g.do(req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	checkResponse(t, req, rec)
	if rec.Body.String() != `{"simulation_id":"sim_01j9z3k8m2q4r6s8t0v2w4x6y8"}`+"\n" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	in, ok := g.track.last().(*trackingv1.SimulateRequest)
	if !ok {
		t.Fatalf("request = %T", g.track.last())
	}
	sc := in.GetScope()
	if sc.GetTenantId() != tenantID || sc.GetEnvironment() != eventsv1.Environment_ENVIRONMENT_TEST || sc.GetActorId() != actor {
		t.Fatalf("scope = %v", sc)
	}
	if in.GetDeliveryId() != delID || in.GetSpeedFactor() != 10 || in.GetGap().GetAfterS() != 30 || in.GetGap().GetDurationS() != 90 {
		t.Fatalf("request = %v", in)
	}
	// A write: audited like any other.
	if rows := g.audit.all(); len(rows) != 1 || rows[0].Action != "deliveries.simulate" || rows[0].Status != http.StatusAccepted {
		t.Fatalf("audit = %+v", rows)
	}

	// No body: the defaults (speed 1, no gap).
	rec = g.do(g.req(http.MethodPost, simulateTarget(), ""))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("no body = %d: %s", rec.Code, rec.Body.String())
	}
	in = g.track.last().(*trackingv1.SimulateRequest)
	if in.GetSpeedFactor() != 0 || in.Gap != nil {
		t.Fatalf("defaults = %v", in)
	}
}

func TestSimulateLiveKeyForbidden(t *testing.T) {
	t.Parallel()
	live := mint(t, apikey.Live, actor, domain.AllScopes...)
	ft := newFakeTracking()
	e := newEdge(t, []testKey{live}, withRoutes(httpapi.Routes(httpapi.Deps{Deliveries: newFake(), Tracking: ft})...),
		withMiddleware(func(m *httpapi.Middleware) {
			m.Idempotency = httpapi.Idempotency(&matrixStore{seen: map[app.IdempotencyScope][]byte{}}, fixedClock{}, m.Log)
		}))
	r := post(simulateTarget(), "Bearer "+live.raw, `{"speed_factor":10}`)
	r.Header.Set("Idempotency-Key", "sim-live")
	wantProblem(t, e.do(r), http.StatusForbidden, "environment_not_allowed")
	if ft.count() != 0 {
		t.Fatalf("tracking called %d times for a live key", ft.count())
	}
}

func TestSimulateErrors(t *testing.T) {
	t.Parallel()
	g := newTrackGW(t, nil)
	for name, body := range map[string]string{
		"speed too high": `{"speed_factor":21}`,
		"speed too low":  `{"speed_factor":0.5}`,
		"gap too long":   `{"signal_gap":{"after_s":0,"duration_s":3601}}`,
		"gap incomplete": `{"signal_gap":{"after_s":10}}`,
		"extra field":    `{"speed":2}`,
	} {
		wantProblem(t, g.do(g.req(http.MethodPost, simulateTarget(), body)), http.StatusBadRequest, "validation_failed")
		if g.track.count() != 0 {
			t.Fatalf("%s: tracking called", name)
		}
	}
	// Running already, or a final delivery.
	g.track.failing = rpcFail(connect.CodeFailedPrecondition, "simulation_state")
	wantProblem(t, g.do(g.req(http.MethodPost, simulateTarget(), `{}`)), http.StatusConflict, "simulation_state")
	g.track.failing = rpcFail(connect.CodeNotFound, "not_found")
	wantProblem(t, g.do(g.req(http.MethodPost, simulateTarget(), `{}`)), http.StatusNotFound, "not_found")
	g.track.failing = rpcFail(connect.CodeUnavailable, "tracking_unavailable")
	wantProblem(t, g.do(g.req(http.MethodPost, simulateTarget(), `{}`)), http.StatusServiceUnavailable, "tracking_unavailable")
}
