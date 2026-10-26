package domain

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

var (
	allModels   = []Model{ModelDirect, ModelEndToEnd}
	allStatuses = []Status{StatusConfirmed, StatusCourierAssigned, StatusAtPickup, StatusPickedUp, StatusArriving, StatusDelivered, StatusCancelled, StatusFailed}
)

func intp(v int) *int              { return &v }
func f64p(v float64) *float64      { return &v }
func timep(v time.Time) *time.Time { return &v }

func TestVisibilityTable(t *testing.T) {
	visible := map[Model]map[Status]bool{
		ModelEndToEnd: {StatusCourierAssigned: true, StatusAtPickup: true, StatusPickedUp: true, StatusArriving: true},
		ModelDirect:   {StatusPickedUp: true, StatusArriving: true},
	}
	for _, m := range allModels {
		for _, s := range allStatuses {
			if got, want := CourierVisible(m, s), visible[m][s]; got != want {
				t.Errorf("CourierVisible(%s,%s)=%v want %v", m, s, got, want)
			}
		}
	}
}

func baseState(m Model, s Status) State {
	return State{
		DeliveryID: "del_01j9z3k8m2q4r6s8t0v2w4x6y8", TenantID: "ten_1", Env: "test", Model: m, Status: s,
		Pickup: LatLng{52.368, 4.903}, PickupName: "Food Hub", Dropoff: LatLng{52.374, 4.889},
		CreatedAt: t0, Version: 5, UpdatedAt: t0,
		Courier: &Courier{ID: "cou_01j9z3k8m2q4r6s8t0v2w4x6y8", ExternalID: "ext-9", DisplayName: "Sam", Vehicle: "e_bike"},
		LastFix: &Fix{T: t0, Pos: LatLng{52.371, 4.897}, SpeedMps: f64p(5)},
		Route: Route{
			ToPickup:  []LatLng{{52.371, 4.897}, {52.368, 4.903}},
			ToDropoff: []LatLng{{52.368, 4.903}, {52.374, 4.889}},
		},
	}
}

func TestVisibilityIsPerDelivery(t *testing.T) {
	direct := Snapshot(baseState(ModelDirect, StatusAtPickup), t0)
	if direct.Courier != nil {
		t.Fatal("direct at_pickup must hide the courier")
	}
	e2e := Snapshot(baseState(ModelEndToEnd, StatusCourierAssigned), t0)
	if e2e.Courier == nil || e2e.Courier.DisplayName != "Sam" {
		t.Fatalf("end_to_end courier_assigned must show the courier: %+v", e2e.Courier)
	}
}

func TestSnapshotDirectHasNoToPickup(t *testing.T) {
	for _, s := range allStatuses {
		snap := Snapshot(baseState(ModelDirect, s), t0)
		if snap.Route.ToPickup != nil {
			t.Errorf("%s: direct to_pickup must be nil", s)
		}
		b, _ := json.Marshal(snap)
		if !strings.Contains(string(b), `"to_pickup":null`) {
			t.Errorf("%s: want to_pickup null in %s", s, b)
		}
	}
	// End-to-end shows it before pickup only, and only while the courier is visible.
	if Snapshot(baseState(ModelEndToEnd, StatusCourierAssigned), t0).Route.ToPickup == nil {
		t.Error("e2e courier_assigned should have to_pickup")
	}
	if Snapshot(baseState(ModelEndToEnd, StatusConfirmed), t0).Route.ToPickup != nil {
		t.Error("e2e confirmed must not leak the courier-origin route")
	}
	if Snapshot(baseState(ModelEndToEnd, StatusPickedUp), t0).Route.ToPickup != nil {
		t.Error("to_pickup must be nil once picked up")
	}
}

func TestSnapshotShapes(t *testing.T) {
	st := baseState(ModelEndToEnd, StatusCourierAssigned)
	st.DefaultPrepMinutes = intp(10)
	st.Route.ToDropoffSec = 600
	snap := Snapshot(st, t0)
	if snap.EtaState != EtaEstimated || snap.EtaWindow == nil || snap.EtaSeconds == nil {
		t.Fatalf("eta: %+v", snap)
	}
	// Partner view keeps the courier where the customer cannot see it.
	pv := PartnerView(baseState(ModelDirect, StatusConfirmed), t0)
	if pv.Courier == nil || pv.Courier.ExternalID != "ext-9" || pv.Courier.ID != "cou_01j9z3k8m2q4r6s8t0v2w4x6y8" {
		t.Fatalf("partner courier: %+v", pv.Courier)
	}
	st.Courier = nil
	if PartnerView(st, t0).Courier != nil {
		t.Fatal("no courier assigned -> null")
	}
	// Pending: visible, no fix.
	st = baseState(ModelEndToEnd, StatusCourierAssigned)
	st.LastFix = nil
	c := Snapshot(st, t0).Courier
	if c.Position != nil || c.Signal != SignalPending || c.PositionAt != nil {
		t.Fatalf("pending courier: %+v", c)
	}
	// Empty route serialises as [] not null.
	st.Route.ToDropoff = nil
	b, _ := json.Marshal(Snapshot(st, t0))
	if !strings.Contains(string(b), `"to_dropoff":[]`) {
		t.Fatalf("to_dropoff: %s", b)
	}
}

func TestSignalThresholds(t *testing.T) {
	st := baseState(ModelEndToEnd, StatusPickedUp)
	cases := []struct {
		age  time.Duration
		want Signal
	}{
		{0, SignalLive}, {30 * time.Second, SignalLive}, {31 * time.Second, SignalStale},
		{300 * time.Second, SignalStale}, {301 * time.Second, SignalLost},
	}
	for _, c := range cases {
		if got := SignalAt(st, t0.Add(c.age)); got != c.want {
			t.Errorf("age %v: got %s want %s", c.age, got, c.want)
		}
	}
	st.LastFix = nil
	st.CourierVisibleSince = timep(t0)
	if got := SignalAt(st, t0.Add(299*time.Second)); got != SignalPending {
		t.Errorf("299s no fix: %s", got)
	}
	if got := SignalAt(st, t0.Add(301*time.Second)); got != SignalLost {
		t.Errorf("301s no fix: %s", got)
	}
}

func TestOlderFixNeverMovesLivePosition(t *testing.T) {
	st := baseState(ModelDirect, StatusPickedUp)
	pos := st.LastFix.Pos
	if st.ApplyFix(Fix{T: t0.Add(-time.Second), Pos: LatLng{1, 1}}, t0) {
		t.Fatal("older fix must return false")
	}
	if st.ApplyFix(Fix{T: t0, Pos: LatLng{1, 1}}, t0) {
		t.Fatal("equal-time fix must return false")
	}
	if st.LastFix.Pos != pos {
		t.Fatal("position moved")
	}
	if !st.ApplyFix(Fix{T: t0.Add(time.Second), Pos: LatLng{52.372, 4.896}}, t0) || st.LastFix.Pos.Lat != 52.372 {
		t.Fatal("newer fix must apply")
	}
	var empty State
	if !empty.ApplyFix(Fix{T: t0}, t0) {
		t.Fatal("first fix applies")
	}
}

func TestCheckFix(t *testing.T) {
	prev := &Fix{T: t0, Pos: LatLng{52.0, 4.0}}
	recv := t0.Add(8 * time.Second)
	near := LatLng{52.0001, 4.0}
	cases := []struct {
		name string
		prev *Fix
		f    Fix
		recv time.Time
		want FixAction
	}{
		{"normal", prev, Fix{T: recv, Pos: near}, recv, Accept},
		{"normal no prev", nil, Fix{T: recv, Pos: near}, recv, Accept},
		{"future 6s", nil, Fix{T: recv.Add(6 * time.Second), Pos: near}, recv, Drop},
		{"future 5s ok", nil, Fix{T: recv.Add(5 * time.Second), Pos: near}, recv, Accept},
		{"old 2m01", nil, Fix{T: recv.Add(-121 * time.Second), Pos: near}, recv, TrailOnly},
		{"reported 250 km/h", nil, Fix{T: recv, Pos: near, SpeedMps: f64p(250 / 3.6)}, recv, Drop},
		{"jump 1.2km in 8s", prev, Fix{T: recv, Pos: LatLng{52.0108, 4.0}}, recv, Drop},
		{"fast over long time", prev, Fix{T: t0.Add(time.Minute), Pos: LatLng{52.0108, 4.0}}, t0.Add(time.Minute), Accept},
		{"implied 250 km/h over 60s", prev, Fix{T: t0.Add(time.Minute), Pos: LatLng{52.14, 4.0}}, t0.Add(time.Minute), Drop},
		{"out of order", prev, Fix{T: t0.Add(-time.Second), Pos: near}, t0, TrailOnly},
	}
	for _, c := range cases {
		v, err := CheckFix(c.prev, c.f, c.recv)
		if err != nil || v.Action != c.want {
			t.Errorf("%s: got %v (%q) err %v want %v", c.name, v.Action, v.Reason, err, c.want)
		}
		if c.want != Accept && v.Reason == "" {
			t.Errorf("%s: missing reason", c.name)
		}
	}
	if v, err := CheckFix(nil, Fix{T: t0, Pos: LatLng{91, 0}}, t0); err == nil || v.Action != Drop {
		t.Error("invalid coordinates must error and drop")
	}
	if _, err := CheckFix(nil, Fix{T: t0, Pos: LatLng{math.NaN(), 0}}, t0); err == nil {
		t.Error("NaN must error")
	}
}

func TestComputeETA(t *testing.T) {
	ready := t0.Add(10 * time.Minute)
	mk := func(m Model, s Status, mod func(*State)) State {
		st := baseState(m, s)
		st.Route.ToDropoffSec = 600
		st.Route.ToPickupSec = 120
		if mod != nil {
			mod(&st)
		}
		return st
	}
	cases := []struct {
		name  string
		st    State
		eta   *int
		win   *[2]int
		state EtaState
	}{
		{"ready_at, e2e courier earlier", mk(ModelEndToEnd, StatusCourierAssigned, func(s *State) { s.ReadyAt = &ready }), intp(1200), &[2]int{900, 1500}, EtaEstimated},
		{"ready_at, e2e courier later than ready", mk(ModelEndToEnd, StatusCourierAssigned, func(s *State) { s.ReadyAt = timep(t0.Add(time.Minute)); s.Route.ToPickupSec = 400 }), intp(1000), &[2]int{700, 1300}, EtaEstimated},
		{"direct ignores courier", mk(ModelDirect, StatusConfirmed, func(s *State) { s.ReadyAt = &ready; s.Route.ToPickupSec = 5000 }), intp(1200), &[2]int{900, 1500}, EtaEstimated},
		{"default prep", mk(ModelDirect, StatusConfirmed, func(s *State) { s.DefaultPrepMinutes = intp(15) }), intp(1500), &[2]int{1200, 1800}, EtaEstimated},
		{"ready in the past", mk(ModelDirect, StatusAtPickup, func(s *State) { s.ReadyAt = timep(t0.Add(-time.Hour)) }), intp(600), &[2]int{300, 900}, EtaEstimated},
		{"window clamps at 0", mk(ModelDirect, StatusAtPickup, func(s *State) { s.ReadyAt = &t0; s.Route.ToDropoffSec = 100 }), intp(100), &[2]int{0, 400}, EtaEstimated},
		{"unknown", mk(ModelDirect, StatusConfirmed, nil), nil, nil, EtaCalculating},
		{"picked_up", mk(ModelEndToEnd, StatusPickedUp, func(s *State) { s.Route.ToDropoffSec = 333 }), intp(333), nil, EtaLive},
		{"arriving", mk(ModelDirect, StatusArriving, func(s *State) { s.Route.ToDropoffSec = 40 }), intp(40), nil, EtaLive},
		{"delivered", mk(ModelDirect, StatusDelivered, func(s *State) { s.ReadyAt = &ready }), nil, nil, EtaLive},
		{"cancelled", mk(ModelDirect, StatusCancelled, nil), nil, nil, EtaLive},
	}
	for _, c := range cases {
		eta, win, state := ComputeETA(c.st, t0)
		if state != c.state || !eqInt(eta, c.eta) || (win == nil) != (c.win == nil) || (win != nil && *win != *c.win) {
			t.Errorf("%s: got %v %v %s want %v %v %s", c.name, deref(eta), win, state, deref(c.eta), c.win, c.state)
		}
	}
}

func eqInt(a, b *int) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
func deref(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestReadyTime(t *testing.T) {
	st := baseState(ModelDirect, StatusConfirmed)
	if _, ok := ReadyTime(st); ok {
		t.Fatal("unknown")
	}
	st.DefaultPrepMinutes = intp(20)
	if r, ok := ReadyTime(st); !ok || !r.Equal(t0.Add(20*time.Minute)) {
		t.Fatal("default prep")
	}
	st.ReadyAt = timep(t0.Add(time.Hour))
	if r, _ := ReadyTime(st); !r.Equal(t0.Add(time.Hour)) {
		t.Fatal("ready_at wins")
	}
}

func TestArrivalDue(t *testing.T) {
	mk := func(distLatDeg float64, eta *int, sent bool) State {
		st := baseState(ModelDirect, StatusPickedUp)
		st.LastFix = &Fix{T: t0, Pos: LatLng{st.Dropoff.Lat + distLatDeg, st.Dropoff.Lng}}
		st.EtaSeconds = eta
		st.ArrivalSent = sent
		return st
	}
	// 0.0018 deg lat ~ 200.2 m; 0.00179 ~ 199 m
	if !ArrivalDue(mk(0.00179, intp(300), false), t0) {
		t.Error("199 m should be due")
	}
	if ArrivalDue(mk(0.00181, intp(91), false), t0) {
		t.Error("201 m, eta 91 should not be due")
	}
	if !ArrivalDue(mk(0.00181, intp(90), false), t0) {
		t.Error("eta 90 should be due")
	}
	if ArrivalDue(mk(0.0001, intp(10), true), t0) {
		t.Error("already sent")
	}
	st := mk(0.0001, intp(10), false)
	st.Status = StatusArriving
	if ArrivalDue(st, t0) {
		t.Error("only from picked_up")
	}
	st = mk(0.01, nil, false)
	if ArrivalDue(st, t0) {
		t.Error("far and unknown eta")
	}
}

func TestProofPosition(t *testing.T) {
	trail := []Fix{
		{T: t0.Add(-90 * time.Second), Pos: LatLng{1, 1}},
		{T: t0.Add(-10 * time.Second), Pos: LatLng{2, 2}},
		{T: t0.Add(5 * time.Second), Pos: LatLng{3, 3}},
		{T: t0.Add(-30 * time.Second), Pos: LatLng{4, 4}},
	}
	if p := ProofPosition(trail, t0); p == nil || p.Pos.Lat != 2 {
		t.Fatalf("latest <= at: %+v", p)
	}
	if p := ProofPosition(trail, t0.Add(100*time.Second)); p == nil || p.Pos.Lat != 3 {
		t.Fatalf("later at: %+v", p)
	}
	if p := ProofPosition(trail, t0.Add(10*time.Minute)); p != nil {
		t.Fatalf("none within 2 min: %+v", p)
	}
	if p := ProofPosition(trail, t0.Add(-5*time.Minute)); p != nil {
		t.Fatalf("all after at: %+v", p)
	}
	if ProofPosition(nil, t0) != nil {
		t.Fatal("empty trail")
	}
}

func TestSimplifyKeepsEndpointsAndTolerance(t *testing.T) {
	var pts []LatLng
	for i := 0; i <= 100; i++ {
		pts = append(pts, LatLng{52.0 + float64(i)*0.0001, 4.0 + 0.000001*float64(i%2)})
	}
	pts = append(pts, LatLng{52.02, 4.01}, LatLng{52.03, 4.01})
	out := Simplify(pts, 5)
	if out[0] != pts[0] || out[len(out)-1] != pts[len(pts)-1] {
		t.Fatal("endpoints")
	}
	if len(out) >= len(pts) || len(out) > 4 {
		t.Fatalf("expected strong reduction, got %d", len(out))
	}
	assertWithin(t, pts, out, 5.5)
	if got := Simplify(pts[:2], 5); len(got) != 2 {
		t.Fatal("two points kept")
	}
	if got := Simplify(nil, 5); len(got) != 0 {
		t.Fatal("nil")
	}
}

// assertWithin checks that every original point lies within tol metres of the polyline out.
func assertWithin(t *testing.T, pts, out []LatLng, tol float64) {
	t.Helper()
	for _, p := range pts {
		best := math.Inf(1)
		for i := 0; i+1 < len(out); i++ {
			d, _ := segmentDistance(project(p, p), project(out[i], p), project(out[i+1], p))
			best = math.Min(best, d)
		}
		if best > tol {
			t.Fatalf("point %v is %.2f m from the simplified line (tol %.1f)", p, best, tol)
		}
	}
}

func TestNextVersionBasics(t *testing.T) {
	if NextVersion(0, t0) != t0.UnixMilli() {
		t.Fatal("clock ahead")
	}
	if NextVersion(t0.UnixMilli(), t0) != t0.UnixMilli()+1 {
		t.Fatal("clock equal")
	}
}

func TestDistanceAndProgress(t *testing.T) {
	st := baseState(ModelDirect, StatusPickedUp)
	st.LastFix = &Fix{T: t0, Pos: st.Route.ToDropoff[0]}
	s := Snapshot(st, t0)
	if s.Progress == nil || *s.Progress > 0.01 || s.DistanceRemainingM == nil || *s.DistanceRemainingM < 500 {
		t.Fatalf("at start: %v %v", s.Progress, s.DistanceRemainingM)
	}
	st.LastFix = &Fix{T: t0, Pos: st.Dropoff}
	s = Snapshot(st, t0)
	if *s.Progress < 0.99 || *s.DistanceRemainingM > 1 {
		t.Fatalf("at end: %v %v", *s.Progress, *s.DistanceRemainingM)
	}
	if s := Snapshot(baseState(ModelDirect, StatusConfirmed), t0); s.Progress != nil || s.DistanceRemainingM != nil {
		t.Fatal("null before pickup")
	}
	if s := Snapshot(baseState(ModelDirect, StatusDelivered), t0); *s.Progress != 1 || *s.DistanceRemainingM != 0 {
		t.Fatal("delivered")
	}
}

func TestEtaQuality(t *testing.T) {
	st := baseState(ModelDirect, StatusPickedUp)
	if q := Snapshot(st, t0).EtaQuality; q != "osrm" {
		t.Fatal(q)
	}
	if q := Snapshot(st, t0.Add(time.Minute)).EtaQuality; q != "estimated" {
		t.Fatal("stale signal -> estimated", q)
	}
	st.Route.Estimated = true
	if q := Snapshot(st, t0).EtaQuality; q != "estimated" {
		t.Fatal(q)
	}
}
