package sim

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/config"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/normalize"
)

var _ normalize.SimRegistry = (*Registry)(nil)

func newManager(t *testing.T, f *fakeGateway) *Manager {
	t.Helper()
	m, err := New(Config{GatewayURL: f.srv.URL, EdgeURL: f.srv.URL, Key: testKey, FixInterval: 40 * time.Millisecond})
	require.NoError(t, err)
	t.Cleanup(m.Close)
	return m
}

func eventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	require.Eventually(t, cond, 3*time.Second, 10*time.Millisecond, msg)
}

func TestSimRoutesAbsentWhenDisabled(t *testing.T) {
	// MC_SIMULATION off: FromConfig makes no manager and Mount adds no route, so the paths are plain 404s.
	m, err := FromConfig(config.Config{Simulation: false, SandboxKey: testKey}, nil, nil)
	require.NoError(t, err)
	assert.Nil(t, m)
	mux := http.NewServeMux()
	Mount(mux, m)
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/sim/runs"}, {"GET", "/api/sim/status"}, {"POST", "/api/sim/runs/del_x/break"}, {"POST", "/api/sim/runs/del_x/heal"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, strings.NewReader(`{"scenario":1}`)))
		assert.Equal(t, http.StatusNotFound, rec.Code, c.method+" "+c.path)
	}
}

func TestPhonePostsThroughGateway(t *testing.T) {
	f := newFakeGateway(t)
	m := newManager(t, f)
	run, err := m.Start(context.Background())
	require.NoError(t, err)
	assert.NotEmpty(t, run.DeliveryID)
	assert.True(t, strings.HasPrefix(run.OrderRef, "mc-sim-"))
	assert.Contains(t, run.TrackingURL, "/t/TRACKTOK")

	eventually(t, func() bool { return f.locCount() >= 3 }, "three location posts")
	for _, l := range f.locsSince(0) {
		assert.Equal(t, "Bearer "+courierToken, l.auth, "the courier JWT, never the partner key")
		assert.Equal(t, 1, l.fixes)
	}
	// pickup is confirmed early so the viewer hop is live, and one viewer is connected with its own token
	eventually(t, func() bool {
		s := f.snapshot()
		return len(s.events) == 1 && s.events[0] == "pickup_confirmed" && s.viewerOpened == 1
	}, "pickup and viewer")
	assert.Equal(t, "Bearer viewer-token-1", f.snapshot().viewerAuth)
}

func TestScenarioInvalidTokenSends401Path(t *testing.T) {
	f := newFakeGateway(t)
	m := newManager(t, f)
	run, err := m.Start(context.Background())
	require.NoError(t, err)
	eventually(t, func() bool { return f.locCount() >= 2 }, "healthy posts first")

	require.NoError(t, m.Break(run.DeliveryID, 2))
	n := f.locCount()
	eventually(t, func() bool { return f.locCount() >= n+3 }, "posts continue")
	for _, l := range f.locsSince(n + 1) {
		assert.NotEqual(t, "Bearer "+courierToken, l.auth, "corrupted JWT")
		assert.True(t, strings.HasPrefix(l.auth, "Bearer "), "still a bearer token, so the gateway answers 401 not 'missing'")
	}

	require.NoError(t, m.Heal(run.DeliveryID))
	n = f.locCount()
	eventually(t, func() bool { return f.locCount() >= n+4 }, "posts after heal")
	for _, l := range f.locsSince(n + 2) {
		assert.Equal(t, "Bearer "+courierToken, l.auth)
	}
}

func TestScenarioRateLimitRate(t *testing.T) {
	f := newFakeGateway(t)
	m := newManager(t, f)
	run, err := m.Start(context.Background())
	require.NoError(t, err)
	eventually(t, func() bool { return f.locCount() >= 3 }, "healthy posts first")

	require.NoError(t, m.Break(run.DeliveryID, 3))
	time.Sleep(60 * time.Millisecond) // a tick of the old speed may still be in flight
	n := f.locCount()
	time.Sleep(time.Second)
	got := f.locCount() - n
	assert.InDelta(t, 20, got, 6, "scenario 3 sends at 20 posts per second, regardless of the phone's normal interval")

	require.NoError(t, m.Heal(run.DeliveryID))
	time.Sleep(60 * time.Millisecond)
	n = f.locCount()
	time.Sleep(time.Second)
	assert.InDelta(t, 25, f.locCount()-n, 8, "back to the normal interval after heal")
}

func TestScenarioPauseAndReassign(t *testing.T) {
	f := newFakeGateway(t)
	m := newManager(t, f)
	run, err := m.StartHeld(context.Background())
	require.NoError(t, err)
	eventually(t, func() bool { return f.locCount() >= 2 }, "healthy posts first")

	require.NoError(t, m.Break(run.DeliveryID, 1))
	time.Sleep(100 * time.Millisecond)
	n := f.locCount()
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, n, f.locCount(), "scenario 1: the phone sends nothing")
	require.NoError(t, m.Heal(run.DeliveryID))
	eventually(t, func() bool { return f.locCount() > n }, "resumes after heal")

	before := f.snapshot()
	require.NoError(t, m.Break(run.DeliveryID, 4))
	after := f.snapshot()
	require.Len(t, after.couriers, len(before.couriers)+1, "a second courier is made")
	second := after.couriers[len(after.couriers)-1]
	assert.NotEqual(t, before.couriers[0], second)
	require.Len(t, after.assignments, len(before.assignments)+1)
	assert.Equal(t, "cou_"+second, after.assignments[len(after.assignments)-1], "assigned to the second courier")
	n = f.locCount()
	eventually(t, func() bool { return f.locCount() > n+1 }, "the phone keeps sending")

	require.NoError(t, m.Heal(run.DeliveryID))
	healed := f.snapshot()
	assert.Equal(t, "cou_"+before.couriers[0], healed.assignments[len(healed.assignments)-1], "heal assigns back to the first courier")
}

// DT refuses a reassignment from picked_up on (409), so scenario 4 needs a run whose pickup is held.
func TestReassignOnlyBeforePickup(t *testing.T) {
	f := newFakeGateway(t)
	m := newManager(t, f)
	picked, err := m.Start(context.Background())
	require.NoError(t, err)
	eventually(t, func() bool { return len(f.snapshot().events) == 1 }, "pickup confirmed")
	assert.True(t, m.Status()[0].PickedUp)
	assert.ErrorIs(t, m.Break(picked.DeliveryID, 4), ErrTooLate)

	held, err := m.StartHeld(context.Background())
	require.NoError(t, err)
	eventually(t, func() bool { return f.locCount() >= 6 }, "both phones send")
	assert.Len(t, f.snapshot().events, 1, "the held run confirms no pickup")
	require.NoError(t, m.Break(held.DeliveryID, 4))
	require.NoError(t, m.Heal(held.DeliveryID))
	eventually(t, func() bool { return len(f.snapshot().events) == 2 }, "heal releases the held pickup")
}

func TestBreakValidation(t *testing.T) {
	f := newFakeGateway(t)
	m := newManager(t, f)
	run, err := m.Start(context.Background())
	require.NoError(t, err)
	assert.ErrorIs(t, m.Break("del_unknown", 1), ErrUnknownRun)
	assert.ErrorIs(t, m.Break(run.DeliveryID, 5), ErrBadScenario, "5 and 6 are Docker-side: make mc-break")
	assert.ErrorIs(t, m.Break(run.DeliveryID, 0), ErrBadScenario)
	assert.ErrorIs(t, m.Heal("del_unknown"), ErrUnknownRun)
}

func TestSimOnlyUsesTestKey(t *testing.T) {
	for _, key := range []string{"", "dt_sk_live_" + strings.Repeat("a", 55), "dt_pk_test_" + strings.Repeat("a", 55)} {
		_, err := New(Config{GatewayURL: "http://x", EdgeURL: "http://x", Key: key})
		require.Error(t, err, key)
		if len(key) > 8 {
			assert.NotContains(t, err.Error(), key[8:], "the error never echoes the key")
		}
	}
	_, err := FromConfig(config.Config{Simulation: true, Env: "local", SandboxKey: "dt_sk_live_" + strings.Repeat("b", 55)}, nil, nil)
	require.Error(t, err)

	// the key goes to partner calls only; the courier and the viewer use their own tokens
	f := newFakeGateway(t)
	m := newManager(t, f)
	_, err = m.Start(context.Background())
	require.NoError(t, err)
	eventually(t, func() bool { return f.locCount() >= 2 && f.snapshot().viewerOpened == 1 }, "run is going")
	s := f.snapshot()
	require.NotEmpty(t, s.partnerAuths)
	for _, a := range s.partnerAuths {
		assert.Equal(t, "Bearer "+testKey, a)
	}
	for _, a := range s.courierAuths {
		assert.NotContains(t, a, "dt_sk_")
	}
	assert.NotContains(t, s.viewerAuth, "dt_sk_")
}

func TestRegistryMarksSimulated(t *testing.T) {
	f := newFakeGateway(t)
	m := newManager(t, f)
	reg := m.Registry()
	assert.False(t, reg.IsSimulated("del_b"))
	assert.False(t, reg.IsSimulated(""))
	run, err := m.Start(context.Background())
	require.NoError(t, err)
	assert.True(t, reg.IsSimulated(run.DeliveryID))
	assert.False(t, reg.IsSimulated("del_other"))
	var nilReg *Registry
	assert.False(t, nilReg.IsSimulated(run.DeliveryID), "a nil registry marks nothing")
}

func TestHTTPRoutes(t *testing.T) {
	f := newFakeGateway(t)
	m := newManager(t, f)
	mux := http.NewServeMux()
	Mount(mux, m)
	do := func(method, path, body string, hdr ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	rec := do("POST", "/api/sim/runs", "")
	require.Equal(t, 201, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"delivery_id":"del_`)
	assert.Contains(t, rec.Body.String(), `"order_ref":"mc-sim-`)
	assert.Contains(t, rec.Body.String(), `"tracking_url":"`)
	assert.NotContains(t, rec.Body.String(), "dt_sk_")
	id := m.Status()[0].DeliveryID

	st := do("GET", "/api/sim/status", "")
	assert.Equal(t, 200, st.Code)
	assert.Contains(t, st.Body.String(), `"enabled":true`)
	assert.Contains(t, st.Body.String(), id)

	assert.Equal(t, 204, do("POST", "/api/sim/runs/"+id+"/break", `{"scenario":1}`).Code)
	assert.Equal(t, 1, m.Status()[0].Scenario)
	assert.Equal(t, 400, do("POST", "/api/sim/runs/"+id+"/break", `{"scenario":5}`).Code)
	assert.Equal(t, 404, do("POST", "/api/sim/runs/del_nope/break", `{"scenario":1}`).Code)
	assert.Equal(t, 204, do("POST", "/api/sim/runs/"+id+"/heal", "").Code)
	assert.Equal(t, 0, m.Status()[0].Scenario)
	// a page on another origin cannot drive the simulator
	assert.Equal(t, 403, do("POST", "/api/sim/runs", "", "Origin", "http://evil.example").Code)
}
