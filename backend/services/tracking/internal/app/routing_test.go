package app

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

// routeFixture is a Direct delivery picked up by cou_1 with a fake router.
func routeFixture(t *testing.T, id string) (*fixture, *fakeRouter) {
	t.Helper()
	f := newFixture(t)
	r := &fakeRouter{}
	f.svc.Router = r
	f.courier(t, "cou_1")
	f.delivery(t, id, domain.ModelDirect, "cou_1", domain.StatusPickedUp)
	return f, r
}

func (f *fixture) fix(t *testing.T, pos domain.LatLng) {
	t.Helper()
	acc, _, err := f.svc.Ingest(context.Background(), sc, "cou_1", []domain.Fix{{T: f.clock.Now(), Pos: pos, AccuracyM: 5}}, "relay")
	if err != nil || acc != 1 {
		t.Fatalf("Ingest = %d, %v", acc, err)
	}
}

// offset moves p by metres to the north (a small enough step for a flat earth).
func offset(p domain.LatLng, northM, eastM float64) domain.LatLng {
	return domain.LatLng{
		Lat: p.Lat + northM/111320,
		Lng: p.Lng + eastM/(111320*math.Cos(p.Lat*math.Pi/180)),
	}
}

func TestRouterFallbackMarksEstimated(t *testing.T) {
	f := newFixture(t)
	r := &fakeRouter{err: errors.New("osrm down")}
	f.svc.Router = r
	f.delivery(t, "del_fb", domain.ModelDirect, "", domain.StatusConfirmed)
	st := f.state(t, "del_fb")
	if !st.Route.Estimated || len(st.Route.ToDropoff) < 2 || st.Route.ToDropoffSec <= 0 {
		t.Fatalf("route = %+v, want an estimated straight line with seconds", st.Route)
	}
	// 25 km/h-ish straight line: the hub is about 1 km from the dropoff.
	d := domain.DistanceM(st.Pickup, st.Dropoff)
	want := int(d / (15 / 3.6)) // no courier: bicycle speed
	if math.Abs(float64(st.Route.ToDropoffSec-want)) > 2 {
		t.Fatalf("fallback seconds = %d, want about %d", st.Route.ToDropoffSec, want)
	}
	snaps := f.pub.forDelivery("del_fb")
	if q := snaps[len(snaps)-1].EtaQuality; q != "estimated" {
		t.Fatalf("eta_quality = %q", q)
	}
	// While it keeps failing, calls are limited to one per 10 s.
	n := r.count()
	f.clock.Advance(3 * time.Second)
	f.touch(t, "del_fb")
	if r.count() != n {
		t.Fatal("router retried within 10 s of a failure")
	}
	// Recovered: the next try replaces the fallback.
	r.err = nil
	f.clock.Advance(10 * time.Second)
	f.touch(t, "del_fb")
	if st = f.state(t, "del_fb"); st.Route.Estimated || r.count() != n+1 {
		t.Fatalf("estimated=%v calls=%d (was %d)", st.Route.Estimated, r.count(), n)
	}
}

// touch runs an Apply that changes nothing but the version.
func (f *fixture) touch(t *testing.T, id string) {
	t.Helper()
	if err := f.svc.Apply(context.Background(), sc, id, func(*domain.State) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestRouteBuiltOnCreateAndKeepsLastRouteOnFailure(t *testing.T) {
	f, r := routeFixture(t, "del_keep")
	st := f.state(t, "del_keep")
	if st.Route.Estimated || len(st.Route.ToDropoff) == 0 || r.count() == 0 {
		t.Fatalf("route = %+v calls=%d", st.Route, r.count())
	}
	p0 := f.state(t, "del_keep").Pickup
	f.clock.Advance(time.Minute)
	f.fix(t, offset(p0, 0, 0))
	st = f.state(t, "del_keep")
	if st.Route.DropoffStage != domain.RouteFromCourier {
		t.Fatalf("stage = %q", st.Route.DropoffStage)
	}
	// Off the route while the router is down: keep the path, flag estimate.
	path := st.Route.ToDropoff
	r.err = errors.New("down")
	f.clock.Advance(time.Minute)
	f.fix(t, offset(domain.LatLng{Lat: (path[0].Lat + path[len(path)-1].Lat) / 2, Lng: (path[0].Lng + path[len(path)-1].Lng) / 2}, 200, 0))
	st = f.state(t, "del_keep")
	if !st.Route.Estimated || len(st.Route.ToDropoff) != len(path) || st.Route.ToDropoffSec <= 0 {
		t.Fatalf("route = %+v", st.Route)
	}
}

func TestRerouteOnlyWhenOffRoute(t *testing.T) {
	f, r := routeFixture(t, "del_off")
	st := f.state(t, "del_off")
	start := offset(st.Pickup, 0, 0)
	f.clock.Advance(time.Minute)
	f.fix(t, start) // the stage change: one route from the courier
	line := f.state(t, "del_off").Route.ToDropoff
	a, b := line[0], line[len(line)-1]
	mid := func(t float64) domain.LatLng {
		return domain.LatLng{Lat: a.Lat + (b.Lat-a.Lat)*t, Lng: a.Lng + (b.Lng-a.Lng)*t}
	}
	// A perpendicular of the given length at fraction t of the route.
	side := func(t, m float64) domain.LatLng {
		p := mid(t)
		dN := (b.Lat - a.Lat) * 111320
		dE := (b.Lng - a.Lng) * 111320 * math.Cos(p.Lat*math.Pi/180)
		l := math.Hypot(dN, dE)
		return offset(p, m*-dE/l, m*dN/l)
	}
	n := r.count()
	f.clock.Advance(time.Minute)
	f.fix(t, side(0.3, 49))
	if r.count() != n {
		t.Fatalf("rerouted at 49 m: %d calls", r.count()-n)
	}
	f.clock.Advance(time.Minute)
	f.fix(t, side(0.5, 51))
	if r.count() != n+1 {
		t.Fatalf("calls at 51 m = %d, want 1", r.count()-n)
	}
}

func TestEtaRecomputeThrottled(t *testing.T) {
	f, r := routeFixture(t, "del_thr")
	st := f.state(t, "del_thr")
	f.clock.Advance(time.Minute)
	f.fix(t, st.Pickup)
	built := f.state(t, "del_thr")
	line := built.Route.ToDropoff
	a, b := line[0], line[len(line)-1]
	along := func(m float64) domain.LatLng {
		fr := m / domain.DistanceM(a, b)
		return domain.LatLng{Lat: a.Lat + (b.Lat-a.Lat)*fr, Lng: a.Lng + (b.Lng-a.Lng)*fr}
	}
	n := r.count()
	f.clock.Advance(3 * time.Second)
	f.fix(t, along(30))
	if got := f.state(t, "del_thr").Route.ToDropoffSec; got != built.Route.ToDropoffSec {
		t.Fatalf("seconds changed after 3 s: %d -> %d", built.Route.ToDropoffSec, got)
	}
	f.clock.Advance(7 * time.Second)
	f.fix(t, along(70))
	if got := f.state(t, "del_thr").Route.ToDropoffSec; got >= built.Route.ToDropoffSec {
		t.Fatalf("seconds not recomputed after 10 s: %d -> %d", built.Route.ToDropoffSec, got)
	}
	if r.count() != n {
		t.Fatalf("recompute called the router %d times", r.count()-n)
	}
}

func TestRouteProfiles(t *testing.T) {
	for v, want := range map[string]string{"car": "car", "scooter": "car", "bicycle": "bike", "e_bike": "bike", "on_foot": "bike", "": "bike"} {
		if got := profileFor(&domain.Courier{Vehicle: v}); got != want {
			t.Errorf("profile(%q) = %q want %q", v, got, want)
		}
	}
	if profileFor(nil) != "bike" {
		t.Error("no courier: bike")
	}
}

func TestEtaChangedEventRules(t *testing.T) {
	sec := func(v int) *int { return &v }
	last := t0
	cases := []struct {
		name  string
		since time.Duration // now - last event
		eta   int           // new ETA in seconds from now
		want  bool
	}{
		{"change of 120 s after a minute", 60 * time.Second, 600 + 60 - 60 + 120 - 60, true},
		{"change of 119 s", 90 * time.Second, 600 - 90 + 119, false},
		{"big change within a minute", 59 * time.Second, 600 - 59 + 300, false},
		{"earlier by 2 minutes", 120 * time.Second, 600 - 120 - 120, true},
		{"same arrival time", 300 * time.Second, 600 - 300, false},
	}
	for _, c := range cases {
		st := &domain.State{LastEtaEventSec: sec(600), LastEtaEventAt: last}
		if got := etaEventDue(st, c.eta, last.Add(c.since)); got != c.want {
			t.Errorf("%s: due = %v want %v", c.name, got, c.want)
		}
	}
	if !etaEventDue(&domain.State{}, 100, t0) {
		t.Error("first ETA must be published")
	}
}

func TestEtaChangedFiresOnceRouteIsKnown(t *testing.T) {
	f, _ := routeFixture(t, "del_ev")
	// Picked up with a route from the store: the ETA is known, one event.
	if n := len(f.events.ofType(EventEtaChanged)); n != 1 {
		t.Fatalf("eta events = %d, want 1", n)
	}
	f.clock.Advance(61 * time.Second)
	f.fix(t, f.state(t, "del_ev").Pickup)
	// Same arrival time within the rules: still one.
	if n := len(f.events.ofType(EventEtaChanged)); n > 2 {
		t.Fatalf("eta events = %d", n)
	}
}
