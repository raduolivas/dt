package app

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

const (
	simDel     = "del_sim"
	simCourier = "cou_sim"
)

// simDeliveries is the deliveries service of the simulator tests: a small
// state machine over one delivery that projects every change into the
// tracking fixture, as the deliveries consumer does in production.
type simDeliveries struct {
	f *fixture

	mu        sync.Mutex
	d         SimDelivery
	confirmed map[string]bool
	version   uint64
	statuses  []domain.Status // applied status changes, in order
	reports   []StatusReport
	upserts   []CourierUpsert
	failGet   error
	// at records the clock when each status was applied.
	at map[domain.Status]time.Time
}

func newSimDeliveries(f *fixture, d SimDelivery) *simDeliveries {
	return &simDeliveries{f: f, d: d, confirmed: map[string]bool{}, version: 1000, at: map[domain.Status]time.Time{}}
}

func (s *simDeliveries) Get(_ context.Context, sc2 Scope, id string) (SimDelivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failGet != nil {
		return SimDelivery{}, s.failGet
	}
	if sc2 != sc || id != s.d.ID {
		return SimDelivery{}, ErrNotFound
	}
	return s.d, nil
}

func (s *simDeliveries) UpsertCourier(ctx context.Context, _ Scope, c CourierUpsert) (domain.Courier, error) {
	s.mu.Lock()
	s.upserts = append(s.upserts, c)
	s.mu.Unlock()
	cour := domain.Courier{ID: simCourier, ExternalID: c.ExternalID, DisplayName: c.DisplayName, Vehicle: c.Vehicle}
	if err := s.f.svc.OnCourierUpserted(ctx, s.f.read, sc, cour); err != nil {
		return domain.Courier{}, err
	}
	s.f.states.mu.Lock()
	s.f.states.scope[simCourier] = sc
	s.f.states.mu.Unlock()
	return cour, nil
}

func (s *simDeliveries) Assign(ctx context.Context, _ Scope, id, courierID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, _, _ := s.f.read.Courier(ctx, courierID)
	s.d.Courier = &c
	s.version++
	if err := s.f.svc.OnCourierAssigned(ctx, s.f.read, sc, s.version, id, courierID, s.f.clock.Now()); err != nil {
		return err
	}
	if s.d.Model == domain.ModelEndToEnd && s.d.Status == domain.StatusConfirmed {
		return s.move(ctx, domain.StatusCourierAssigned, "partner")
	}
	return nil
}

func (s *simDeliveries) move(ctx context.Context, to domain.Status, actor string) error {
	from := s.d.Status
	s.d.Status = to
	s.statuses = append(s.statuses, to)
	s.at[to] = s.f.clock.Now()
	s.version++
	return s.f.svc.OnStatusChanged(ctx, s.f.read, sc, s.version, StatusChanged{
		ID: s.d.ID, From: from, To: to, Actor: actor, At: s.f.clock.Now()})
}

func policySatisfied(policy string, by map[string]bool) bool {
	switch policy {
	case "courier":
		return by["courier"]
	case "store":
		return by["store"]
	case "courier_and_store":
		return by["courier"] && by["store"]
	}
	return len(by) > 0
}

func (s *simDeliveries) Report(ctx context.Context, _ Scope, _ string, r StatusReport) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reports = append(s.reports, r)
	switch r.Type {
	case "at_pickup":
		return s.move(ctx, domain.StatusAtPickup, r.Actor)
	case "pickup_confirmed":
		by := r.By
		if r.Actor == "courier" {
			by = "courier"
		}
		s.confirmed[by] = true
		if s.d.Status.BeforePickup() && policySatisfied(s.d.PickupPolicy, s.confirmed) {
			return s.move(ctx, domain.StatusPickedUp, r.Actor)
		}
		return nil
	case "arriving":
		if s.d.Status != domain.StatusPickedUp {
			return nil
		}
		return s.move(ctx, domain.StatusArriving, r.Actor)
	case "delivered":
		return s.move(ctx, domain.StatusDelivered, r.Actor)
	}
	return errors.New("unexpected report " + r.Type)
}

func (s *simDeliveries) snapshot() ([]domain.Status, []StatusReport, []CourierUpsert) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.statuses), slices.Clone(s.reports), slices.Clone(s.upserts)
}

func (s *simDeliveries) statusAt(st domain.Status) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.at[st]
	return t, ok
}

// simHarness runs a Simulator on fake time: every Sleep first delivers the
// tracking.arrival_detected events enqueued so far to deliveries (as the
// deliveries-tracking consumer would), then advances the clock, then runs
// the signal sweeper and samples the delivery's viewer signal.
type simHarness struct {
	f       *fixture
	dels    *simDeliveries
	sim     *Simulator
	arrived int // arrival events delivered so far

	mu      sync.Mutex
	signals []domain.Signal
}

func newSimHarness(t *testing.T, m domain.Model, policy string, status domain.Status, withCourier bool) *simHarness {
	t.Helper()
	f := newFixture(t)
	f.svc.Router = &fakeRouter{mps: 5}
	return newSimHarnessOn(t, f, m, policy, status, withCourier)
}

func newSimHarnessOn(t *testing.T, f *fixture, m domain.Model, policy string, status domain.Status, withCourier bool) *simHarness {
	t.Helper()
	courier := ""
	if withCourier {
		f.courier(t, simCourier)
		courier = simCourier
	}
	f.delivery(t, simDel, m, courier, status)
	st, _, _ := f.states.Get(context.Background(), simDel)
	d := SimDelivery{ID: simDel, Model: m, Status: status, PickupPolicy: policy, Pickup: st.Pickup, Dropoff: st.Dropoff}
	if withCourier {
		c, _, _ := f.read.Courier(context.Background(), simCourier)
		d.Courier = &c
	}
	h := &simHarness{f: f, dels: newSimDeliveries(f, d)}
	h.sim = &Simulator{Svc: f.svc, Deliveries: h.dels, Sleep: h.sleep, IDs: func() string { return "sim_test" }}
	return h
}

func (h *simHarness) sleep(ctx context.Context, d time.Duration) error {
	for range h.f.events.ofType(EventArrivalDetected)[h.arrived:] {
		h.arrived++
		_ = h.dels.Report(ctx, sc, simDel, StatusReport{EventID: "evt_arrival", Type: "arriving", Actor: "system"})
	}
	h.f.clock.Advance(d)
	_ = h.f.svc.SweepSignals(ctx)
	if st, ok, _ := h.f.states.Get(ctx, simDel); ok {
		h.mu.Lock()
		h.signals = append(h.signals, domain.ViewerSignal(st, h.f.clock.Now()))
		h.mu.Unlock()
	}
	return ctx.Err()
}

func (h *simHarness) run(t *testing.T, speed float64, gap *SignalGap) {
	t.Helper()
	id, err := h.sim.Start(context.Background(), sc, simDel, speed, gap)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if id != "sim_test" {
		t.Fatalf("simulation id = %q", id)
	}
	h.sim.Wait()
}

func (h *simHarness) simFixes() []domain.Fix {
	var out []domain.Fix
	for _, r := range h.f.trail.forDelivery(simDel) {
		if r.Fix.Source != "simulator" {
			continue
		}
		out = append(out, r.Fix)
	}
	return out
}

func TestSimulatorEndToEndFullRun(t *testing.T) {
	h := newSimHarness(t, domain.ModelEndToEnd, "any", domain.StatusConfirmed, false)
	start := h.f.clock.Now()
	h.run(t, 10, nil)

	statuses, reports, upserts := h.dels.snapshot()
	want := []domain.Status{domain.StatusCourierAssigned, domain.StatusAtPickup, domain.StatusPickedUp,
		domain.StatusArriving, domain.StatusDelivered}
	if !slices.Equal(statuses, want) {
		t.Fatalf("statuses = %v, want %v", statuses, want)
	}
	// The simulated courier was created and assigned.
	if len(upserts) != 1 || upserts[0] != (CourierUpsert{ExternalID: "dt-simulator", DisplayName: "Sim Courier", Vehicle: "e_bike"}) {
		t.Fatalf("upserts = %+v", upserts)
	}
	// Courier reports for at_pickup, the pickup (policy any) and delivered;
	// arriving came from tracking's arrival (actor system).
	for _, r := range reports {
		wantActor := "courier"
		if r.Type == "arriving" {
			wantActor = "system"
		}
		if r.Actor != wantActor || r.EventID == "" {
			t.Fatalf("report %+v", r)
		}
	}
	if n := len(h.f.events.ofType(EventArrivalDetected)); n != 1 {
		t.Fatalf("arrival events = %d, want 1", n)
	}

	fixes := h.simFixes()
	if len(fixes) < 10 {
		t.Fatalf("simulator fixes = %d", len(fixes))
	}
	st, _, _ := h.f.states.Get(context.Background(), simDel)
	// It started on the road about 1.5 km south-west of the store...
	if d := domain.DistanceM(fixes[0].Pos, st.Pickup); d < 1400 || d > 1600 {
		t.Fatalf("first fix %.0f m from the store, want about 1500", d)
	}
	if fixes[0].Pos.Lat >= st.Pickup.Lat || fixes[0].Pos.Lng >= st.Pickup.Lng {
		t.Fatalf("first fix %v is not south-west of the store %v", fixes[0].Pos, st.Pickup)
	}
	// ...reported at_pickup at the store and delivered at the dropoff.
	atPickup, _ := h.dels.statusAt(domain.StatusAtPickup)
	if p := lastFixBefore(fixes, atPickup); p == nil || domain.DistanceM(p.Pos, st.Pickup) > 20 {
		t.Fatalf("at_pickup away from the store: %v", p)
	}
	delivered, _ := h.dels.statusAt(domain.StatusDelivered)
	if p := lastFixBefore(fixes, delivered); p == nil || domain.DistanceM(p.Pos, st.Dropoff) > 20 {
		t.Fatalf("delivered away from the dropoff: %v", p)
	}
	// One fix per 2 s of simulated time: 0.2 s apart at 10x.
	if gap := fixes[1].T.Sub(fixes[0].T); gap != 200*time.Millisecond {
		t.Fatalf("fix spacing = %v, want 200ms", gap)
	}
	// 1.5 km + ~1.2 km at 5 m/s is about 9 simulated minutes: under a
	// minute of (fake) real time at 10x.
	if took := h.f.clock.Now().Sub(start); took > 2*time.Minute {
		t.Fatalf("run took %v of real time", took)
	}
}

func lastFixBefore(fixes []domain.Fix, t time.Time) *domain.Fix {
	var out *domain.Fix
	for i := range fixes {
		if fixes[i].T.After(t) {
			break
		}
		out = &fixes[i]
	}
	return out
}

func TestSimulatorDirectWaitsForPickup(t *testing.T) {
	f := newFixture(t)
	f.svc.Router = &fakeRouter{mps: 5}
	prep := 10
	f.read.prep[sc.TenantID] = &prep // ready 10 minutes after creation
	h := newSimHarnessOn(t, f, domain.ModelDirect, "courier", domain.StatusConfirmed, true)
	created := f.clock.Now()
	h.run(t, 5, nil)

	statuses, reports, upserts := h.dels.snapshot()
	want := []domain.Status{domain.StatusPickedUp, domain.StatusArriving, domain.StatusDelivered}
	if !slices.Equal(statuses, want) {
		t.Fatalf("statuses = %v, want %v", statuses, want)
	}
	if len(upserts) != 0 {
		t.Fatalf("an assigned courier was replaced: %+v", upserts)
	}
	for _, r := range reports {
		if r.Type == "at_pickup" {
			t.Fatalf("direct reported at_pickup: %+v", reports)
		}
	}
	// It waited the 10-minute preparation, scaled by 5: 2 minutes.
	picked, _ := h.dels.statusAt(domain.StatusPickedUp)
	if w := picked.Sub(created); w < 2*time.Minute || w > 2*time.Minute+5*time.Second {
		t.Fatalf("picked up after %v, want about 2m", w)
	}
	// The courier waited at the store: every fix before pickup is there.
	st, _, _ := f.states.Get(context.Background(), simDel)
	fixes := h.simFixes()
	for _, fx := range fixes {
		if fx.T.After(picked) {
			break
		}
		if d := domain.DistanceM(fx.Pos, st.Pickup); d > 1 {
			t.Fatalf("fix before pickup %.0f m from the store", d)
		}
	}
	if len(fixes) == 0 || !fixes[0].T.Before(picked) {
		t.Fatal("no fixes while waiting at the store")
	}
}

func TestSimulatorHonoursPickupPolicy(t *testing.T) {
	cases := []struct {
		policy string
		want   []StatusReport // the pickup confirmations, in order
	}{
		{"any", []StatusReport{{Type: "pickup_confirmed", Actor: "courier"}}},
		{"courier", []StatusReport{{Type: "pickup_confirmed", Actor: "courier"}}},
		{"store", []StatusReport{{Type: "pickup_confirmed", Actor: "partner", By: "store"}}},
		{"courier_and_store", []StatusReport{
			{Type: "pickup_confirmed", Actor: "courier"},
			{Type: "pickup_confirmed", Actor: "partner", By: "store"},
		}},
	}
	for _, c := range cases {
		t.Run(c.policy, func(t *testing.T) {
			h := newSimHarness(t, domain.ModelEndToEnd, c.policy, domain.StatusConfirmed, false)
			h.run(t, 20, nil)
			statuses, reports, _ := h.dels.snapshot()
			if statuses[len(statuses)-1] != domain.StatusDelivered || !slices.Contains(statuses, domain.StatusPickedUp) {
				t.Fatalf("statuses = %v", statuses)
			}
			var got []StatusReport
			ids := map[string]bool{}
			for _, r := range reports {
				if ids[r.EventID] {
					t.Fatalf("event id %q reused", r.EventID)
				}
				ids[r.EventID] = true
				if r.Type == "pickup_confirmed" {
					r.EventID = ""
					got = append(got, r)
				}
			}
			if !slices.Equal(got, c.want) {
				t.Fatalf("pickup confirmations = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestSimulatorSignalGapProducesStaleThenLost(t *testing.T) {
	h := newSimHarness(t, domain.ModelEndToEnd, "any", domain.StatusCourierAssigned, true)
	// 60 s of simulated time in, a 6-minute gap: past stale (30 s) and lost
	// (5 min) on the real clock, whatever the speed factor.
	h.run(t, 10, &SignalGap{AfterS: 60, DurationS: 360})

	h.mu.Lock()
	signals := slices.Compact(slices.Clone(h.signals))
	h.mu.Unlock()
	i := slices.Index(signals, domain.SignalStale)
	if i < 0 || i+2 >= len(signals) || signals[i-1] != domain.SignalLive || signals[i+1] != domain.SignalLost ||
		signals[i+2] != domain.SignalLive {
		t.Fatalf("signals = %v, want live, stale, lost, then live again", signals)
	}
	if n := len(h.f.events.ofType(EventSignalLost)); n != 1 {
		t.Fatalf("signal lost events = %d", n)
	}
	if n := len(h.f.events.ofType(EventSignalRestored)); n != 1 {
		t.Fatalf("signal restored events = %d", n)
	}
	// No fix during the gap, and the run still finished.
	fixes := h.simFixes()
	longest := time.Duration(0)
	for i := 1; i < len(fixes); i++ {
		longest = max(longest, fixes[i].T.Sub(fixes[i-1].T))
	}
	if longest < 6*time.Minute || longest > 6*time.Minute+time.Second {
		t.Fatalf("longest gap between fixes = %v, want 6m", longest)
	}
	statuses, _, _ := h.dels.snapshot()
	if statuses[len(statuses)-1] != domain.StatusDelivered {
		t.Fatalf("statuses = %v", statuses)
	}
	// Fixes after the gap were accepted: the courier kept a plausible
	// speed through it.
	st, _, _ := h.f.states.Get(context.Background(), simDel)
	if st.LastFix == nil || domain.DistanceM(st.LastFix.Pos, st.Dropoff) > 20 {
		t.Fatalf("last accepted fix %v is not at the dropoff", st.LastFix)
	}
}

func TestSimulateTwice409(t *testing.T) {
	h := newSimHarness(t, domain.ModelEndToEnd, "any", domain.StatusCourierAssigned, true)
	// Hold the first run in its first sleep.
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	inner := h.sim.Sleep
	h.sim.Sleep = func(ctx context.Context, d time.Duration) error {
		select {
		case entered <- struct{}{}:
			<-release
		default:
		}
		return inner(ctx, d)
	}
	ctx := context.Background()
	if _, err := h.sim.Start(ctx, sc, simDel, 20, nil); err != nil {
		t.Fatal(err)
	}
	<-entered
	if _, err := h.sim.Start(ctx, sc, simDel, 20, nil); !errors.Is(err, ErrSimulationState) {
		t.Fatalf("second Start = %v, want ErrSimulationState", err)
	}
	close(release)
	h.sim.Wait()

	// Delivered now: a final delivery cannot be simulated either.
	if _, err := h.sim.Start(ctx, sc, simDel, 20, nil); !errors.Is(err, ErrSimulationState) {
		t.Fatalf("Start on a delivered delivery = %v, want ErrSimulationState", err)
	}
}

func TestSimulateValidatesAndScopes(t *testing.T) {
	h := newSimHarness(t, domain.ModelEndToEnd, "any", domain.StatusCourierAssigned, true)
	ctx := context.Background()
	for _, c := range []struct {
		speed float64
		gap   *SignalGap
	}{
		{0.5, nil}, {21, nil}, {1, &SignalGap{AfterS: -1}}, {1, &SignalGap{AfterS: 3601}},
		{1, &SignalGap{DurationS: -1}}, {1, &SignalGap{DurationS: 3601}},
	} {
		if _, err := h.sim.Start(ctx, sc, simDel, c.speed, c.gap); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("Start(%v, %+v) = %v, want ErrInvalidArgument", c.speed, c.gap, err)
		}
	}
	if _, err := h.sim.Start(ctx, Scope{TenantID: "ten_other", Env: "test"}, simDel, 1, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other tenant = %v, want ErrNotFound", err)
	}
	if _, err := h.sim.Start(ctx, Scope{TenantID: sc.TenantID, Env: "live"}, simDel, 1, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("live scope = %v, want ErrInvalidArgument (sandbox only)", err)
	}
	h.dels.failGet = errors.New("deliveries down")
	if _, err := h.sim.Start(ctx, sc, simDel, 1, nil); err == nil || errors.Is(err, ErrSimulationState) {
		t.Fatalf("deliveries down = %v", err)
	}
}
