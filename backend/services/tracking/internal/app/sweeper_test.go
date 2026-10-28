package app

import (
	"context"
	"sync"
	"testing"
	"time"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

func (f *fixture) sweep(t *testing.T) {
	t.Helper()
	if err := f.svc.SweepSignals(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) lastSnapshot(t *testing.T, id string) domain.TrackingSnapshot {
	t.Helper()
	snaps := f.pub.forDelivery(id)
	if len(snaps) == 0 {
		t.Fatalf("%s: nothing published", id)
	}
	return snaps[len(snaps)-1]
}

func (f *fixture) ingest(t *testing.T, courier string, fx ...domain.Fix) {
	t.Helper()
	if _, _, err := f.svc.Ingest(context.Background(), sc, courier, fx, "relay"); err != nil {
		t.Fatal(err)
	}
}

func TestActiveSetFollowsVisibleNonFinalDeliveries(t *testing.T) {
	f := newFixture(t)
	f.courier(t, "cou_1")
	f.delivery(t, "del_h", domain.ModelDirect, "cou_1", domain.StatusConfirmed) // hidden courier
	f.delivery(t, "del_v", domain.ModelDirect, "cou_1", domain.StatusPickedUp)  // visible, no fix
	if f.states.isActive("del_h") {
		t.Fatal("hidden courier's delivery is in dt:active")
	}
	since, ok := f.states.activeScore("del_v")
	if !ok || !since.Equal(f.clock.Now()) {
		t.Fatalf("visible delivery: in dt:active %v, score %v (want visible-since)", ok, since)
	}
	if s := f.states.activeScope("del_v"); s != sc {
		t.Fatalf("dt:active scope = %+v", s)
	}
	f.clock.Advance(2 * time.Second)
	fx := fixAt(f.clock.Now(), 52.369, 4.9)
	f.ingest(t, "cou_1", fx)
	if got, _ := f.states.activeScore("del_v"); !got.Equal(fx.T) {
		t.Fatalf("score after a fix = %v, want the fix time %v", got, fx.T)
	}
	f.status(t, "del_v", domain.StatusPickedUp, domain.StatusDelivered)
	if f.states.isActive("del_v") {
		t.Fatal("final delivery still in dt:active")
	}
}

func TestSweeperFlipsStaleAndLostWithoutFixes(t *testing.T) {
	f := newFixture(t)
	f.courier(t, "cou_1")
	f.delivery(t, "del_s", domain.ModelDirect, "cou_1", domain.StatusPickedUp)
	f.clock.Advance(time.Second)
	f.ingest(t, "cou_1", fixAt(f.clock.Now(), 52.369, 4.9))
	live := f.lastSnapshot(t, "del_s")
	if live.Courier == nil || live.Courier.Signal != domain.SignalLive {
		t.Fatalf("after a fix: %+v", live.Courier)
	}

	// Nothing changes within 30 s: no publish.
	f.clock.Advance(30 * time.Second)
	f.sweep(t)
	if got := f.lastSnapshot(t, "del_s"); got.Version != live.Version {
		t.Fatalf("published at 30 s: %+v", got.Courier)
	}

	f.clock.Advance(time.Second) // 31 s without a fix
	f.sweep(t)
	stale := f.lastSnapshot(t, "del_s")
	if stale.Courier == nil || stale.Courier.Signal != domain.SignalStale || stale.Version <= live.Version {
		t.Fatalf("at 31 s: signal %+v version %d (was %d)", stale.Courier, stale.Version, live.Version)
	}
	if stale.EtaQuality != "estimated" {
		t.Fatalf("stale eta_quality = %q", stale.EtaQuality)
	}
	n := len(f.pub.forDelivery("del_s"))
	f.sweep(t) // unchanged: nothing new
	if len(f.pub.forDelivery("del_s")) != n {
		t.Fatal("a sweep without a change published")
	}

	f.clock.Advance(5 * time.Minute) // > 5 min without a fix
	f.sweep(t)
	lost := f.lastSnapshot(t, "del_s")
	if lost.Courier == nil || lost.Courier.Signal != domain.SignalLost || lost.Version <= stale.Version {
		t.Fatalf("after 5 min: signal %+v version %d", lost.Courier, lost.Version)
	}
	if st := f.state(t, "del_s"); st.Signal != domain.SignalLost {
		t.Fatalf("stored signal = %q", st.Signal)
	}
}

func TestSweeperTurnsPendingIntoLost(t *testing.T) {
	f := newFixture(t)
	f.courier(t, "cou_1")
	f.delivery(t, "del_p", domain.ModelEndToEnd, "cou_1", domain.StatusCourierAssigned)
	if s := f.lastSnapshot(t, "del_p"); s.Courier == nil || s.Courier.Signal != domain.SignalPending {
		t.Fatalf("visible courier without a fix: %+v", s.Courier)
	}
	f.clock.Advance(5*time.Minute + time.Second)
	f.sweep(t)
	if s := f.lastSnapshot(t, "del_p"); s.Courier == nil || s.Courier.Signal != domain.SignalLost {
		t.Fatalf("5 min without a fix: %+v", s.Courier)
	}
	ev := f.events.ofType(EventSignalLost)
	if len(ev) != 1 || ev[0].msg.(*eventsv1.CourierSignalLost).GetLastFixAt() != nil {
		t.Fatalf("signal_lost events = %+v", ev)
	}
}

func TestSignalLostEventOncePerGap(t *testing.T) {
	f := newFixture(t)
	f.courier(t, "cou_1")
	f.delivery(t, "del_g", domain.ModelDirect, "cou_1", domain.StatusPickedUp)
	f.clock.Advance(time.Second)
	first := fixAt(f.clock.Now(), 52.369, 4.9)
	f.ingest(t, "cou_1", first)

	// Gap 1: many sweeps, one event.
	for range 80 {
		f.clock.Advance(5 * time.Second)
		f.sweep(t)
	}
	lost := f.events.ofType(EventSignalLost)
	if len(lost) != 1 {
		t.Fatalf("signal_lost events in one gap = %d, want 1", len(lost))
	}
	m := lost[0].msg.(*eventsv1.CourierSignalLost)
	if m.GetDeliveryId() != "del_g" || m.GetCourierId() != "cou_1" || !m.GetLastFixAt().AsTime().Equal(first.T) ||
		lost[0].subject.DeliveryID != "del_g" {
		t.Fatalf("signal_lost = %+v (subject %+v)", m, lost[0].subject)
	}
	if n := len(f.events.ofType(EventSignalRestored)); n != 0 {
		t.Fatalf("restored before any fix returned: %d", n)
	}

	// Fixes return: restored once, and the snapshot is live again.
	f.ingest(t, "cou_1", fixAt(f.clock.Now(), 52.3691, 4.9))
	f.sweep(t)
	rest := f.events.ofType(EventSignalRestored)
	if len(rest) != 1 || rest[0].msg.(*eventsv1.CourierSignalRestored).GetCourierId() != "cou_1" {
		t.Fatalf("signal_restored events = %+v", rest)
	}
	if s := f.lastSnapshot(t, "del_g"); s.Courier.Signal != domain.SignalLive {
		t.Fatalf("after the fix: %q", s.Courier.Signal)
	}

	// Gap 2: a new event.
	for range 80 {
		f.clock.Advance(5 * time.Second)
		f.sweep(t)
	}
	if n := len(f.events.ofType(EventSignalLost)); n != 2 {
		t.Fatalf("signal_lost events after two gaps = %d, want 2", n)
	}
}

func TestSignalLostEventRetriedAfterEnqueueFailure(t *testing.T) {
	f := newFixture(t)
	f.courier(t, "cou_1")
	f.delivery(t, "del_e", domain.ModelDirect, "cou_1", domain.StatusPickedUp)
	f.clock.Advance(5*time.Minute + time.Second)
	f.events.mu.Lock()
	f.events.fail = context.DeadlineExceeded
	f.events.mu.Unlock()
	f.sweep(t)
	if s := f.lastSnapshot(t, "del_e"); s.Courier.Signal != domain.SignalLost {
		t.Fatalf("signal = %q", s.Courier.Signal)
	}
	f.events.mu.Lock()
	f.events.fail = nil
	f.events.mu.Unlock()
	f.clock.Advance(5 * time.Second)
	f.sweep(t)
	f.sweep(t)
	if n := len(f.events.ofType(EventSignalLost)); n != 1 {
		t.Fatalf("signal_lost after a failed enqueue = %d, want 1", n)
	}
}

func TestSweeperDropsFinishedAndHiddenEntries(t *testing.T) {
	f := newFixture(t)
	f.courier(t, "cou_1")
	f.delivery(t, "del_f", domain.ModelDirect, "cou_1", domain.StatusPickedUp)
	// A stale entry (its removal failed, or the delivery vanished).
	_ = f.states.SetActive(context.Background(), sc, "del_gone", t0, true)
	f.states.mu.Lock()
	f.states.failActive = true // the Apply cannot remove it either
	f.states.mu.Unlock()
	f.status(t, "del_f", domain.StatusPickedUp, domain.StatusDelivered)
	f.states.mu.Lock()
	f.states.failActive = false
	f.states.mu.Unlock()
	f.clock.Advance(time.Minute)
	f.sweep(t)
	if f.states.isActive("del_f") || f.states.isActive("del_gone") {
		t.Fatal("sweeper kept finished or unknown deliveries")
	}
}

// memLease is an in-memory Lease with a TTL on the shared clock.
type memLease struct {
	mu     sync.Mutex
	clock  Clock
	holder string
	until  time.Time
}

func (l *memLease) Hold(_ context.Context, holder string, ttl time.Duration) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now()
	if l.holder != "" && l.holder != holder && now.Before(l.until) {
		return false, nil
	}
	l.holder, l.until = holder, now.Add(ttl)
	return true, nil
}

func (l *memLease) Release(_ context.Context, holder string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.holder == holder {
		l.holder = ""
	}
	return nil
}

func TestSweeperSingleLeader(t *testing.T) {
	f := newFixture(t)
	f.courier(t, "cou_1")
	f.delivery(t, "del_l", domain.ModelDirect, "cou_1", domain.StatusPickedUp)
	lease := &memLease{clock: f.clock}
	a := &Sweeper{Svc: f.svc, Lease: lease, ID: "a"}
	b := &Sweeper{Svc: f.svc, Lease: lease, ID: "b"}
	ctx := context.Background()

	f.clock.Advance(5*time.Minute + time.Second)
	sa, err := a.Tick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sb, err := b.Tick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !sa || sb {
		t.Fatalf("swept: a=%v b=%v, want only a (the lease holder)", sa, sb)
	}
	// The holder renews its lease on every tick.
	for range 5 {
		f.clock.Advance(SweepInterval)
		if ok, _ := a.Tick(ctx); !ok {
			t.Fatal("holder lost its lease while renewing")
		}
		if ok, _ := b.Tick(ctx); ok {
			t.Fatal("second instance swept while the first holds the lease")
		}
	}
	if n := len(f.events.ofType(EventSignalLost)); n != 1 {
		t.Fatalf("signal_lost events = %d", n)
	}
	// The holder stops: after the lease runs out the other one takes over.
	f.clock.Advance(LeaseTTL + time.Second)
	if ok, _ := b.Tick(ctx); !ok {
		t.Fatal("standby did not take over an expired lease")
	}
	// A released lease is free at once.
	if err := lease.Release(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := a.Tick(ctx); !ok {
		t.Fatal("released lease not taken")
	}
}
