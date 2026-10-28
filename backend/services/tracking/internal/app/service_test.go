package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

var (
	t0 = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	sc = Scope{TenantID: "ten_a", Env: "test"}
)

type fixture struct {
	svc    *Service
	states *fakeStates
	read   *fakeRead
	trail  *fakeTrail
	pub    *fakePub
	events *fakeEvents
	clock  *fakeClock
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{states: newFakeStates(), read: newFakeRead(), trail: &fakeTrail{}, pub: &fakePub{},
		events: &fakeEvents{}, clock: &fakeClock{t: t0}}
	f.svc = &Service{States: f.states, Reads: f.read, Trail: f.trail, Pub: f.pub, Events: f.events,
		Sampler: &fakeSampler{}, Clock: f.clock}
	return f
}

func (f *fixture) courier(t *testing.T, id string) {
	t.Helper()
	if err := f.svc.OnCourierUpserted(context.Background(), f.read, sc,
		domain.Courier{ID: id, ExternalID: "ext-" + id, DisplayName: "Sam", Vehicle: "e_bike"}); err != nil {
		t.Fatal(err)
	}
	f.states.mu.Lock()
	f.states.scope[id] = sc
	f.states.mu.Unlock()
}

var nextVersion uint64 = 1

// delivery creates a delivery of model m, assigns courier (if any) and moves
// it to status.
func (f *fixture) delivery(t *testing.T, id string, m domain.Model, courier string, status domain.Status) {
	t.Helper()
	ctx := context.Background()
	nextVersion++
	if err := f.svc.OnDeliveryCreated(ctx, f.read, sc, nextVersion, DeliveryCreated{
		ID: id, Model: m, Status: domain.StatusConfirmed, Pickup: domain.LatLng{Lat: 52.368, Lng: 4.903},
		PickupName: "Hub", Dropoff: domain.LatLng{Lat: 52.374, Lng: 4.889}, CreatedAt: f.clock.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if courier != "" {
		nextVersion++
		if err := f.svc.OnCourierAssigned(ctx, f.read, sc, nextVersion, id, courier, f.clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	from := domain.StatusConfirmed
	path := map[domain.Model][]domain.Status{
		domain.ModelEndToEnd: {domain.StatusCourierAssigned, domain.StatusAtPickup, domain.StatusPickedUp, domain.StatusArriving, domain.StatusDelivered},
		domain.ModelDirect:   {domain.StatusPickedUp, domain.StatusArriving, domain.StatusDelivered},
	}[m]
	for _, to := range path {
		if from == status {
			break
		}
		f.status(t, id, from, to)
		from = to
	}
}

func (f *fixture) status(t *testing.T, id string, from, to domain.Status) {
	t.Helper()
	nextVersion++
	if err := f.svc.OnStatusChanged(context.Background(), f.read, sc, nextVersion, StatusChanged{
		ID: id, From: from, To: to, Actor: "partner", At: f.clock.Now(),
	}); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) state(t *testing.T, id string) domain.State {
	t.Helper()
	st, ok, err := f.states.Get(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("state %s: ok=%v err=%v", id, ok, err)
	}
	return st
}

func fixAt(t time.Time, lat, lng float64) domain.Fix {
	return domain.Fix{T: t, Pos: domain.LatLng{Lat: lat, Lng: lng}, AccuracyM: 5}
}

func TestFixFansOutToEachActiveDelivery(t *testing.T) {
	f := newFixture(t)
	f.courier(t, "cou_1")
	f.delivery(t, "del_a", domain.ModelEndToEnd, "cou_1", domain.StatusPickedUp)
	f.delivery(t, "del_b", domain.ModelEndToEnd, "cou_1", domain.StatusCourierAssigned)
	f.delivery(t, "del_c", domain.ModelEndToEnd, "", domain.StatusConfirmed) // not this courier's
	f.clock.Advance(time.Second)
	now := f.clock.Now()
	acc, drop, err := f.svc.Ingest(context.Background(), sc, "cou_1",
		[]domain.Fix{fixAt(now.Add(-time.Second), 52.369, 4.9), fixAt(now, 52.3691, 4.9001)}, "relay")
	if err != nil || acc != 2 || drop != 0 {
		t.Fatalf("Ingest = %d,%d,%v want 2,0,nil", acc, drop, err)
	}
	for _, id := range []string{"del_a", "del_b"} {
		st := f.state(t, id)
		if st.LastFix == nil || !st.LastFix.T.Equal(now) || st.LastFix.Source != "relay" {
			t.Fatalf("%s last fix = %+v", id, st.LastFix)
		}
		if rows := f.trail.forDelivery(id); len(rows) != 2 || rows[0].CourierID != "cou_1" || rows[0].TenantID != "ten_a" {
			t.Fatalf("%s trail = %+v", id, rows)
		}
	}
	if f.trail.forDelivery("del_c") != nil {
		t.Fatal("unrelated delivery got fixes")
	}
	if n := len(f.events.ofType(EventLocationReported)); n != 1 {
		t.Fatalf("location_reported events = %d want 1", n)
	}
	// Sampled: a second batch within the minute reports nothing.
	f.clock.Advance(time.Second)
	if _, _, err := f.svc.Ingest(context.Background(), sc, "cou_1", []domain.Fix{fixAt(f.clock.Now(), 52.3692, 4.9002)}, "relay"); err != nil {
		t.Fatal(err)
	}
	if n := len(f.events.ofType(EventLocationReported)); n != 1 {
		t.Fatalf("location_reported events = %d want 1 (sampled)", n)
	}
}

func TestReassignmentDropsOldCourierFixes(t *testing.T) {
	f := newFixture(t)
	f.courier(t, "cou_old")
	f.courier(t, "cou_new")
	f.delivery(t, "del_r", domain.ModelEndToEnd, "cou_old", domain.StatusCourierAssigned)
	f.clock.Advance(time.Second)
	if acc, _, err := f.svc.Ingest(context.Background(), sc, "cou_old", []domain.Fix{fixAt(f.clock.Now(), 52.369, 4.9)}, "relay"); err != nil || acc != 1 {
		t.Fatalf("old courier before reassignment: %d %v", acc, err)
	}
	nextVersion++
	if err := f.svc.OnCourierAssigned(context.Background(), f.read, sc, nextVersion, "del_r", "cou_new", f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	st := f.state(t, "del_r")
	if st.LastFix != nil || st.Courier == nil || st.Courier.ID != "cou_new" {
		t.Fatalf("after reassignment: last fix %+v courier %+v", st.LastFix, st.Courier)
	}
	f.clock.Advance(time.Second)
	acc, drop, err := f.svc.Ingest(context.Background(), sc, "cou_old", []domain.Fix{fixAt(f.clock.Now(), 52.3691, 4.9)}, "relay")
	if err != nil || acc != 0 || drop != 1 {
		t.Fatalf("old courier after reassignment: %d,%d,%v want 0,1", acc, drop, err)
	}
	// Even if the old courier's cached set is stale, the state check drops it.
	_ = f.states.SetCourierActive(context.Background(), "cou_old", "del_r", true)
	acc, _, err = f.svc.Ingest(context.Background(), sc, "cou_old", []domain.Fix{fixAt(f.clock.Now(), 52.3692, 4.9)}, "relay")
	if err != nil || acc != 0 {
		t.Fatalf("stale active set: accepted %d err %v", acc, err)
	}
	if st := f.state(t, "del_r"); st.LastFix != nil {
		t.Fatalf("old courier moved the delivery: %+v", st.LastFix)
	}
	if acc, _, _ := f.svc.Ingest(context.Background(), sc, "cou_new", []domain.Fix{fixAt(f.clock.Now(), 52.37, 4.9)}, "relay"); acc != 1 {
		t.Fatalf("new courier accepted %d", acc)
	}
}

func TestDelayedBatchKeepsSignalStale(t *testing.T) {
	f := newFixture(t)
	f.courier(t, "cou_1")
	f.delivery(t, "del_s", domain.ModelEndToEnd, "cou_1", domain.StatusPickedUp)
	f.clock.Advance(5 * time.Minute)
	now := f.clock.Now()
	// A batch buffered on the phone: newest fix 60 s old.
	fixes := []domain.Fix{fixAt(now.Add(-90*time.Second), 52.369, 4.9), fixAt(now.Add(-60*time.Second), 52.3691, 4.9)}
	if acc, _, err := f.svc.Ingest(context.Background(), sc, "cou_1", fixes, "relay"); err != nil || acc != 2 {
		t.Fatalf("Ingest: %d %v", acc, err)
	}
	snaps := f.pub.forDelivery("del_s")
	last := snaps[len(snaps)-1]
	if last.Courier == nil || last.Courier.Signal != domain.SignalStale {
		t.Fatalf("signal = %+v, want stale", last.Courier)
	}
}

func TestFixWithoutActiveDeliveryIsDropped(t *testing.T) {
	f := newFixture(t)
	f.courier(t, "cou_idle")
	fixes := []domain.Fix{fixAt(t0, 52.369, 4.9), fixAt(t0.Add(time.Second), 52.3691, 4.9)}
	acc, drop, err := f.svc.Ingest(context.Background(), sc, "cou_idle", fixes, "relay")
	if err != nil || acc != 0 || drop != 2 {
		t.Fatalf("Ingest = %d,%d,%v want 0,2,nil", acc, drop, err)
	}
	// Finished deliveries do not count.
	f.delivery(t, "del_done", domain.ModelDirect, "cou_idle", domain.StatusDelivered)
	acc, drop, err = f.svc.Ingest(context.Background(), sc, "cou_idle", fixes, "relay")
	if err != nil || acc != 0 || drop != 2 {
		t.Fatalf("after delivered: %d,%d,%v want 0,2,nil", acc, drop, err)
	}
	if len(f.trail.rows) != 0 {
		t.Fatalf("trail rows = %d", len(f.trail.rows))
	}
}

func TestIngestRejectsOtherTenantCourier(t *testing.T) {
	f := newFixture(t)
	f.courier(t, "cou_1")
	_, _, err := f.svc.Ingest(context.Background(), Scope{TenantID: "ten_b", Env: "test"}, "cou_1",
		[]domain.Fix{fixAt(t0, 52.369, 4.9)}, "relay")
	if !errors.Is(err, ErrCourierNotFound) {
		t.Fatalf("err = %v want ErrCourierNotFound", err)
	}
}

func TestStateRebuildKeepsVersionMonotonic(t *testing.T) {
	f := newFixture(t)
	f.courier(t, "cou_1")
	f.delivery(t, "del_v", domain.ModelEndToEnd, "cou_1", domain.StatusPickedUp)
	f.clock.Advance(time.Second)
	if _, _, err := f.svc.Ingest(context.Background(), sc, "cou_1", []domain.Fix{fixAt(f.clock.Now(), 52.369, 4.9)}, "relay"); err != nil {
		t.Fatal(err)
	}
	snaps := f.pub.forDelivery("del_v")
	lastPublished := snaps[len(snaps)-1].Version
	f.states.flush() // Redis lost everything
	f.clock.Advance(time.Millisecond)
	if err := f.svc.Apply(context.Background(), sc, "del_v", func(*domain.State) error { return ErrNoChange }); err != nil {
		t.Fatal(err)
	}
	snaps = f.pub.forDelivery("del_v")
	if v := snaps[len(snaps)-1].Version; v <= lastPublished {
		t.Fatalf("version after rebuild %d <= last published %d", v, lastPublished)
	}
	if st := f.state(t, "del_v"); st.Courier == nil || st.Status != domain.StatusPickedUp {
		t.Fatalf("rebuilt state: %+v", st)
	}
}

func TestCASConflictRetries(t *testing.T) {
	f := newFixture(t)
	f.courier(t, "cou_1")
	f.delivery(t, "del_c", domain.ModelEndToEnd, "cou_1", domain.StatusPickedUp)
	// Another writer stores a newer state between our load and our put.
	f.states.beforeP = func() {
		st, _, _ := f.states.Get(context.Background(), "del_c")
		st.Version++
		st.PickupName = "changed by the other writer"
		f.states.put(st)
	}
	calls := 0
	err := f.svc.Apply(context.Background(), sc, "del_c", func(st *domain.State) error {
		calls++
		st.ReadyAt = &t0
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("mutate ran %d times, want 2 (one retry)", calls)
	}
	st := f.state(t, "del_c")
	if st.PickupName != "changed by the other writer" || st.ReadyAt == nil {
		t.Fatalf("lost an update: %+v", st)
	}
	// Persistent conflict: gives up after the retries.
	n := 0
	f.states.beforeP = nil
	conflicting := &alwaysConflict{fakeStates: f.states, n: &n}
	f.svc.States = conflicting
	err = f.svc.Apply(context.Background(), sc, "del_c", func(*domain.State) error { return nil })
	if !errors.Is(err, ErrConflict) || n != casAttempts {
		t.Fatalf("err=%v attempts=%d", err, n)
	}
}

type alwaysConflict struct {
	*fakeStates
	n *int
}

func (a *alwaysConflict) Put(context.Context, domain.State, int64) (bool, error) {
	*a.n++
	return false, nil
}

func TestHiddenCourierFixDoesNotChangeViewerSnapshot(t *testing.T) {
	f := newFixture(t)
	f.courier(t, "cou_1")
	// Direct before pickup: the courier is hidden from the customer.
	f.delivery(t, "del_h", domain.ModelDirect, "cou_1", domain.StatusConfirmed)
	before := f.state(t, "del_h")
	nPub := len(f.pub.forDelivery("del_h"))
	f.clock.Advance(10 * time.Second)
	if acc, _, err := f.svc.Ingest(context.Background(), sc, "cou_1", []domain.Fix{fixAt(f.clock.Now(), 52.369, 4.9)}, "relay"); err != nil || acc != 1 {
		t.Fatalf("Ingest: %d %v", acc, err)
	}
	after := f.state(t, "del_h")
	if after.LastFix == nil {
		t.Fatal("the partner view must still get the fix")
	}
	if after.ViewerVersion != before.ViewerVersion || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("viewer version/updated_at moved: %d->%d %v->%v", before.ViewerVersion, after.ViewerVersion, before.UpdatedAt, after.UpdatedAt)
	}
	b1, _ := json.Marshal(domain.Snapshot(before, f.clock.Now()))
	b2, _ := json.Marshal(domain.Snapshot(after, f.clock.Now()))
	if string(b1) != string(b2) {
		t.Fatalf("viewer snapshot changed:\n%s\n%s", b1, b2)
	}
	if n := len(f.pub.forDelivery("del_h")); n != nPub {
		t.Fatalf("published %d snapshots on a hidden fix", n-nPub)
	}
	// Once visible, fixes publish again.
	f.status(t, "del_h", domain.StatusConfirmed, domain.StatusPickedUp)
	f.clock.Advance(time.Second)
	n0 := len(f.pub.forDelivery("del_h"))
	if _, _, err := f.svc.Ingest(context.Background(), sc, "cou_1", []domain.Fix{fixAt(f.clock.Now(), 52.3692, 4.9)}, "relay"); err != nil {
		t.Fatal(err)
	}
	if n := len(f.pub.forDelivery("del_h")); n != n0+1 {
		t.Fatalf("visible fix published %d snapshots", n-n0)
	}
}

func TestProofPointWrittenOnDelivered(t *testing.T) {
	f := newFixture(t)
	f.courier(t, "cou_1")
	f.delivery(t, "del_p", domain.ModelDirect, "cou_1", domain.StatusArriving)
	f.clock.Advance(10 * time.Second)
	fixT := f.clock.Now()
	if _, _, err := f.svc.Ingest(context.Background(), sc, "cou_1", []domain.Fix{fixAt(fixT, 52.3739, 4.889)}, "relay"); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(30 * time.Second)
	nextVersion++
	if err := f.svc.OnStatusChanged(context.Background(), f.read, sc, nextVersion, StatusChanged{
		ID: "del_p", From: domain.StatusArriving, To: domain.StatusDelivered, Actor: "courier", At: f.clock.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	var got *ProofPoint
	for i, p := range f.read.proofs {
		if p.DeliveryID == "del_p" && p.Status == "delivered" {
			got = &f.read.proofs[i]
		}
	}
	if got == nil || got.Actor != "courier" || got.Fix == nil || !got.Fix.T.Equal(fixT) || got.Fix.Pos.Lat != 52.3739 {
		t.Fatalf("proof point = %+v", got)
	}
	rec := f.read.recs["del_p"]
	if rec.FinalAt == nil {
		t.Fatal("final_at not recorded")
	}
	// No longer active for the courier.
	ids, _ := f.states.ActiveDeliveriesForCourier(context.Background(), sc.TenantID, sc.Env, "cou_1")
	if len(ids) != 0 {
		t.Fatalf("active after delivered: %v", ids)
	}
	// No fix within 2 minutes: position null.
	f.delivery(t, "del_q", domain.ModelDirect, "cou_1", domain.StatusPickedUp)
	f.clock.Advance(3 * time.Minute)
	f.status(t, "del_q", domain.StatusPickedUp, domain.StatusFailed)
	for _, p := range f.read.proofs {
		if p.DeliveryID == "del_q" && p.Status == "failed" && p.Fix != nil {
			t.Fatalf("stale fix used: %+v", p.Fix)
		}
	}
}

func TestVisibleSinceSetOnBecomingVisible(t *testing.T) {
	f := newFixture(t)
	f.courier(t, "cou_1")
	f.delivery(t, "del_vs", domain.ModelDirect, "cou_1", domain.StatusConfirmed)
	if st := f.state(t, "del_vs"); st.CourierVisibleSince != nil {
		t.Fatal("hidden courier has visible-since")
	}
	f.clock.Advance(time.Minute)
	f.status(t, "del_vs", domain.StatusConfirmed, domain.StatusPickedUp)
	st := f.state(t, "del_vs")
	if st.CourierVisibleSince == nil || !st.CourierVisibleSince.Equal(f.clock.Now()) {
		t.Fatalf("visible since = %v", st.CourierVisibleSince)
	}
	snaps := f.pub.forDelivery("del_vs")
	if c := snaps[len(snaps)-1].Courier; c == nil || c.Signal != domain.SignalPending {
		t.Fatalf("courier = %+v want pending", c)
	}
}

func TestArrivalEventOnceNearDropoff(t *testing.T) {
	f := newFixture(t)
	f.courier(t, "cou_1")
	f.delivery(t, "del_arr", domain.ModelDirect, "cou_1", domain.StatusPickedUp)
	if n := len(f.events.ofType(EventArrivalDetected)); n != 0 {
		t.Fatalf("arrival without route or fix: %d", n)
	}
	for i := range 2 {
		f.clock.Advance(time.Second)
		if _, _, err := f.svc.Ingest(context.Background(), sc, "cou_1", []domain.Fix{fixAt(f.clock.Now(), 52.3739+float64(i)*0.00001, 4.889)}, "relay"); err != nil {
			t.Fatal(err)
		}
	}
	got := f.events.ofType(EventArrivalDetected)
	if len(got) != 1 || got[0].subject.DeliveryID != "del_arr" {
		t.Fatalf("arrival events = %+v", got)
	}
	if ev := got[0].msg.(*eventsv1.ArrivalDetected); ev.GetDistanceM() > 200 {
		t.Fatalf("distance %v", ev.GetDistanceM())
	}
}

func TestTenantPrepRefreshesActiveDeliveries(t *testing.T) {
	f := newFixture(t)
	f.delivery(t, "del_prep", domain.ModelDirect, "", domain.StatusConfirmed)
	if snaps := f.pub.forDelivery("del_prep"); snaps[len(snaps)-1].EtaState != domain.EtaCalculating {
		t.Fatalf("eta_state = %s", snaps[len(snaps)-1].EtaState)
	}
	sp := &fakeSettings{fakeRead: f.read}
	prep := 15
	if err := f.svc.OnTenantSettings(context.Background(), sp, "ten_a", &prep, t0); err != nil {
		t.Fatal(err)
	}
	snaps := f.pub.forDelivery("del_prep")
	if s := snaps[len(snaps)-1]; s.EtaState != domain.EtaEstimated || s.EtaSeconds == nil {
		t.Fatalf("after prep: %+v", s)
	}
}

type fakeSettings struct{ *fakeRead }

func (f *fakeSettings) SetTenantPrep(_ context.Context, tenant string, p *int, _ time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prep[tenant] = p
	return true, nil
}

func (f *fakeSettings) Bind(context.Context, Scope) (Projection, error) { return f.fakeRead, nil }

func (f *fakeSettings) ActiveDeliveries(_ context.Context, s Scope) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for id, r := range f.recs {
		if r.TenantID == s.TenantID && r.Env == s.Env && r.FinalAt == nil {
			out = append(out, id)
		}
	}
	return out, nil
}

func TestEnqueueFailureStillPublishesSnapshot(t *testing.T) {
	f := newFixture(t)
	f.courier(t, "cou_1")
	f.delivery(t, "del_e", domain.ModelDirect, "cou_1", domain.StatusPickedUp)
	f.events.fail = errors.New("postgres down")
	n0 := len(f.pub.forDelivery("del_e"))
	f.clock.Advance(time.Second)
	// Within 200 m of the dropoff: the arrival event is due but cannot be enqueued.
	acc, _, err := f.svc.Ingest(context.Background(), sc, "cou_1", []domain.Fix{fixAt(f.clock.Now(), 52.3739, 4.889)}, "relay")
	if err != nil || acc != 1 {
		t.Fatalf("Ingest = %d, %v", acc, err)
	}
	st := f.state(t, "del_e")
	if st.LastFix == nil || !st.LastFix.T.Equal(f.clock.Now()) {
		t.Fatalf("live position did not move: %+v", st.LastFix)
	}
	if st.ArrivalSent {
		t.Fatal("ArrivalSent kept although the event was not enqueued")
	}
	if n := len(f.pub.forDelivery("del_e")); n != n0+1 {
		t.Fatalf("published %d snapshots, want 1", n-n0)
	}
	// Postgres back: the next change enqueues the arrival, once.
	f.events.fail = nil
	for range 2 {
		f.clock.Advance(time.Second)
		if _, _, err := f.svc.Ingest(context.Background(), sc, "cou_1", []domain.Fix{fixAt(f.clock.Now(), 52.37391, 4.889)}, "relay"); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(f.events.ofType(EventArrivalDetected)); n != 1 {
		t.Fatalf("arrival events = %d, want 1", n)
	}
	if !f.state(t, "del_e").ArrivalSent {
		t.Fatal("ArrivalSent not set after the event was enqueued")
	}
}

func TestApplyErrorOnOneDeliveryKeepsOthers(t *testing.T) {
	f := newFixture(t)
	f.courier(t, "cou_1")
	f.delivery(t, "del_ok", domain.ModelEndToEnd, "cou_1", domain.StatusPickedUp)
	f.delivery(t, "del_bad", domain.ModelEndToEnd, "cou_1", domain.StatusPickedUp)
	f.states.failPut = map[string]error{"del_bad": errors.New("redis hiccup")}
	f.clock.Advance(time.Second)
	acc, drop, err := f.svc.Ingest(context.Background(), sc, "cou_1", []domain.Fix{fixAt(f.clock.Now(), 52.369, 4.9)}, "relay")
	if err != nil || acc != 1 || drop != 0 {
		t.Fatalf("Ingest = %d,%d,%v want 1,0,nil", acc, drop, err)
	}
	if st := f.state(t, "del_ok"); st.LastFix == nil {
		t.Fatal("the healthy delivery lost its fix")
	}
	if rows := f.trail.forDelivery("del_ok"); len(rows) != 1 {
		t.Fatalf("healthy delivery trail rows = %d", len(rows))
	}
	// Every delivery failing is an error the caller sees.
	f.states.failPut["del_ok"] = errors.New("redis down")
	f.clock.Advance(time.Second)
	if _, _, err := f.svc.Ingest(context.Background(), sc, "cou_1", []domain.Fix{fixAt(f.clock.Now(), 52.3691, 4.9)}, "relay"); err == nil {
		t.Fatal("all deliveries failed but Ingest returned nil")
	}
}

func TestProofPointIgnoresPreviousCourierFixes(t *testing.T) {
	f := newFixture(t)
	f.courier(t, "cou_old")
	f.courier(t, "cou_new")
	f.delivery(t, "del_pp", domain.ModelEndToEnd, "cou_old", domain.StatusCourierAssigned)
	f.clock.Advance(time.Second)
	if _, _, err := f.svc.Ingest(context.Background(), sc, "cou_old", []domain.Fix{fixAt(f.clock.Now(), 52.368, 4.903)}, "relay"); err != nil {
		t.Fatal(err)
	}
	// The old courier's fix reached the stored trail.
	f.read.addTrail("del_pp", "cou_old", fixAt(f.clock.Now(), 52.368, 4.903))
	f.clock.Advance(10 * time.Second)
	nextVersion++
	if err := f.svc.OnCourierAssigned(context.Background(), f.read, sc, nextVersion, "del_pp", "cou_new", f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	// Within 2 minutes of the old fix, the new courier reaches the pickup
	// without having sent a fix: the proof point has no position.
	f.clock.Advance(10 * time.Second)
	f.status(t, "del_pp", domain.StatusCourierAssigned, domain.StatusAtPickup)
	for _, p := range f.read.proofs {
		if p.DeliveryID == "del_pp" && p.Status == "at_pickup" && p.Fix != nil {
			t.Fatalf("proof point used the previous courier's fix: %+v", p.Fix)
		}
	}
	// The new courier's own fix is used.
	f.read.addTrail("del_pp", "cou_new", fixAt(f.clock.Now(), 52.3681, 4.9031))
	f.clock.Advance(5 * time.Second)
	f.status(t, "del_pp", domain.StatusAtPickup, domain.StatusPickedUp)
	var got *ProofPoint
	for i, p := range f.read.proofs {
		if p.DeliveryID == "del_pp" && p.Status == "pickup_confirmed" {
			got = &f.read.proofs[i]
		}
	}
	if got == nil || got.Fix == nil || got.Fix.Pos.Lat != 52.3681 {
		t.Fatalf("pickup_confirmed proof = %+v", got)
	}
}

func TestEnqueueTimeoutDoesNotDelayPublish(t *testing.T) {
	f := newFixture(t)
	f.courier(t, "cou_1")
	f.delivery(t, "del_to", domain.ModelDirect, "cou_1", domain.StatusPickedUp)
	f.events.hang = true
	n0 := len(f.pub.forDelivery("del_to"))
	f.clock.Advance(time.Second)
	start := time.Now()
	// Near the dropoff: the arrival event is due, and Postgres hangs.
	acc, _, err := f.svc.Ingest(context.Background(), sc, "cou_1", []domain.Fix{fixAt(f.clock.Now(), 52.3739, 4.889)}, "relay")
	if err != nil || acc != 1 {
		t.Fatalf("Ingest = %d, %v", acc, err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Apply took %v with a hung outbox", d)
	}
	if st := f.state(t, "del_to"); st.ArrivalSent || st.LastFix == nil || !st.LastFix.T.Equal(f.clock.Now()) {
		t.Fatalf("state = arrivalSent %v lastFix %+v", st.ArrivalSent, st.LastFix)
	}
	if n := len(f.pub.forDelivery("del_to")); n != n0+1 {
		t.Fatalf("published %d snapshots, want 1", n-n0)
	}
}

type failingPub struct{ n int }

func (p *failingPub) Snapshot(context.Context, string, domain.TrackingSnapshot) error {
	p.n++
	return errors.New("redis pubsub down")
}

func TestPublishFailureAfterPutIsNotAnError(t *testing.T) {
	f := newFixture(t)
	f.courier(t, "cou_1")
	f.delivery(t, "del_pf", domain.ModelDirect, "cou_1", domain.StatusPickedUp)
	pub := &failingPub{}
	f.svc.Pub = pub
	f.clock.Advance(time.Second)
	acc, _, err := f.svc.Ingest(context.Background(), sc, "cou_1", []domain.Fix{fixAt(f.clock.Now(), 52.372, 4.89)}, "relay")
	if err != nil || acc != 1 {
		t.Fatalf("Ingest = %d, %v", acc, err)
	}
	if pub.n == 0 {
		t.Fatal("publish was not attempted")
	}
	if rows := f.trail.forDelivery("del_pf"); len(rows) != 1 {
		t.Fatalf("trail rows = %d, want 1", len(rows))
	}
	// And Apply itself returns nil.
	f.clock.Advance(time.Second)
	if err := f.svc.Apply(context.Background(), sc, "del_pf", func(st *domain.State) error {
		st.PickupName = "Other hub"
		return nil
	}); err != nil {
		t.Fatalf("Apply = %v", err)
	}
}
