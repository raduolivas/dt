package postgres_test

import (
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/app"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/domain"
)

func TestStatusChangedCarriesNoReason(t *testing.T) {
	t.Parallel()
	e := setup(t)
	const reason = "customer Ana moved"
	d := create(t, e, scopeA, fullInput("ord-evt"), domain.DefaultTenantSettings(), t0)
	mutate(t, e, scopeA, d.ID, func(d *domain.Delivery) ([]domain.Event, error) {
		return d.Cancel(reason, t0.Add(time.Minute))
	})
	var found bool
	for _, r := range outboxRows(t, e) {
		if r.env.GetType() != domain.EventStatusChanged {
			continue
		}
		found = true
		m := &eventsv1.DeliveryStatusChanged{}
		if err := r.env.GetPayload().UnmarshalTo(m); err != nil {
			t.Fatal(err)
		}
		if f := m.ProtoReflect().Descriptor().Fields().ByName("reason"); f != nil {
			t.Errorf("DeliveryStatusChanged still has field %q", f.Name())
		}
		if bytes.Contains(r.raw, []byte(reason)) {
			t.Error("status_changed event carries the reason")
		}
	}
	if !found {
		t.Fatal("no status_changed event")
	}
}

// versionedEvent is one outbox event of a delivery: its type and version.
type versionedEvent struct {
	typ     string
	version uint64
}

// deliveryEvents returns the outbox events about delivery id, in order.
func deliveryEvents(t *testing.T, e env, id string) []versionedEvent {
	t.Helper()
	var out []versionedEvent
	for _, r := range outboxRows(t, e) {
		if r.env.GetSubject().GetDeliveryId() == id {
			out = append(out, versionedEvent{r.env.GetType(), r.env.GetSubject().GetDeliveryVersion()})
		}
	}
	return out
}

func storedVersion(t *testing.T, e env, id string) int64 {
	t.Helper()
	d, err := e.repo.Get(context.Background(), scopeA, id)
	if err != nil {
		t.Fatal(err)
	}
	return d.Version
}

func TestCreateEventVersionIsOne(t *testing.T) {
	t.Parallel()
	e := setup(t)
	d := create(t, e, scopeA, fullInput("ord-v1"), domain.DefaultTenantSettings(), t0)
	got := deliveryEvents(t, e, d.ID)
	if want := []versionedEvent{{domain.EventDeliveryCreated, 1}}; !slices.Equal(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
	if v := storedVersion(t, e, d.ID); v != 1 || d.Version != 1 {
		t.Errorf("version stored %d, in memory %d; want 1", v, d.Version)
	}
	// courier.upserted has no delivery and carries 0.
	upsertCourier(t, e, scopeA, "cour-v")
	for _, r := range outboxRows(t, e) {
		if r.env.GetType() == domain.EventCourierUpserted && r.env.GetSubject().GetDeliveryVersion() != 0 {
			t.Errorf("courier.upserted delivery_version = %d, want 0", r.env.GetSubject().GetDeliveryVersion())
		}
	}
}

func TestMutateEventsShareVersion(t *testing.T) {
	t.Parallel()
	e := setup(t)
	d := create(t, e, scopeA, fullInput("ord-v2"), domain.DefaultTenantSettings(), t0)
	// The courier's confirmation satisfies the "any" policy: one change, two events.
	got := mutate(t, e, scopeA, d.ID, report(domain.Report{
		EventID: "cour-1", Type: domain.ReportPickupConfirmed, Actor: domain.ActorCourier,
		OccurredAt: t0.Add(time.Minute), ReceivedAt: t0.Add(time.Minute),
	}))
	want := []versionedEvent{
		{domain.EventDeliveryCreated, 1}, {domain.EventPickupConfirmed, 2}, {domain.EventStatusChanged, 2},
	}
	if evts := deliveryEvents(t, e, d.ID); !slices.Equal(evts, want) {
		t.Errorf("events = %v, want %v", evts, want)
	}
	if v := storedVersion(t, e, d.ID); v != 2 || got.Version != 2 {
		t.Errorf("version stored %d, returned %d; want 2", v, got.Version)
	}
}

func TestVersionIncreasesPerChange(t *testing.T) {
	t.Parallel()
	e := setup(t)
	d := create(t, e, scopeA, fullInput("ord-v3"), domain.DefaultTenantSettings(), t0)
	step := func(id string, typ domain.ReportType, at time.Duration) {
		t.Helper()
		mutate(t, e, scopeA, d.ID, report(domain.Report{
			EventID: id, Type: typ, Actor: domain.ActorPartner, OccurredAt: t0.Add(at), ReceivedAt: t0.Add(at),
		}))
	}
	step("p-1", domain.ReportAtPickup, time.Minute)
	step("p-2", domain.ReportPickupConfirmed, 2*time.Minute)
	step("p-3", domain.ReportArriving, 3*time.Minute)
	// A stale report (same state) changes nothing and emits no event.
	step("p-4", domain.ReportArriving, 4*time.Minute)
	want := []versionedEvent{
		{domain.EventDeliveryCreated, 1},
		{domain.EventStatusChanged, 2},
		{domain.EventPickupConfirmed, 3}, {domain.EventStatusChanged, 3},
		{domain.EventStatusChanged, 4},
	}
	if got := deliveryEvents(t, e, d.ID); !slices.Equal(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
	if v := storedVersion(t, e, d.ID); v != 4 {
		t.Errorf("stored version %d, want 4", v)
	}
}

func TestPickupWarningBumpsVersion(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	cas := domain.DefaultTenantSettings()
	cas.PickupPolicy = domain.PolicyCourierAndStore
	d := create(t, e, scopeA, fullInput("ord-v4"), cas, t0)
	mutate(t, e, scopeA, d.ID, report(domain.Report{
		EventID: "store-1", Type: domain.ReportPickupConfirmed, Actor: domain.ActorPartner, By: domain.ByStore,
		OccurredAt: t0, ReceivedAt: t0,
	}))
	due := t0.Add(domain.PickupPendingAfter)
	w := app.PickupWatch{DeliveryID: d.ID, TenantID: scopeA.TenantID, Env: scopeA.Env, Since: t0}
	if err := e.repo.WarnPickupPending(ctx, w, due); err != nil {
		t.Fatal(err)
	}
	// A second warning attempt emits nothing and leaves the version.
	if err := e.repo.WarnPickupPending(ctx, w, due); err != nil {
		t.Fatal(err)
	}
	want := []versionedEvent{
		{domain.EventDeliveryCreated, 1}, {domain.EventPickupConfirmed, 2}, {domain.EventPickupConfirmationPending, 3},
	}
	if got := deliveryEvents(t, e, d.ID); !slices.Equal(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
	if v := storedVersion(t, e, d.ID); v != 3 {
		t.Errorf("stored version %d, want 3", v)
	}
	// The next change follows the warning's version.
	mutate(t, e, scopeA, d.ID, report(domain.Report{
		EventID: "cour-1", Type: domain.ReportPickupConfirmed, Actor: domain.ActorCourier,
		OccurredAt: due, ReceivedAt: due,
	}))
	if v := storedVersion(t, e, d.ID); v != 4 {
		t.Errorf("stored version after the warning and a change %d, want 4", v)
	}
}

func TestReadyAtPersistsAndRidesEvents(t *testing.T) {
	t.Parallel()
	e := setup(t)
	in := fullInput("ord-ready")
	in.ReadyAt = ptr(t0.Add(15 * time.Minute))
	d := create(t, e, scopeA, in, domain.DefaultTenantSettings(), t0)
	got, err := e.repo.Get(context.Background(), scopeA, d.ID)
	if err != nil || got.ReadyAt == nil || !got.ReadyAt.Equal(*in.ReadyAt) {
		t.Fatalf("get: %v %v", got, err)
	}
	nv := t0.Add(30 * time.Minute)
	mutate(t, e, scopeA, d.ID, func(d *domain.Delivery) ([]domain.Event, error) {
		return d.Update(domain.Patch{ReadyAt: ptr(&nv)}, t0.Add(time.Minute))
	})
	mutate(t, e, scopeA, d.ID, func(d *domain.Delivery) ([]domain.Event, error) {
		return d.Update(domain.Patch{ReadyAt: ptr[*time.Time](nil)}, t0.Add(2*time.Minute))
	})
	got, err = e.repo.Get(context.Background(), scopeA, d.ID)
	if err != nil || got.ReadyAt != nil {
		t.Fatalf("after clear: %v %v", got, err)
	}
	var created, updated []*eventsv1.Envelope
	for _, r := range outboxRows(t, e) {
		if r.env.GetSubject().GetDeliveryId() != d.ID {
			continue
		}
		switch r.env.GetType() {
		case domain.EventDeliveryCreated:
			created = append(created, r.env)
		case domain.EventDeliveryUpdated:
			updated = append(updated, r.env)
		}
	}
	c := &eventsv1.DeliveryCreated{}
	if len(created) != 1 || created[0].GetPayload().UnmarshalTo(c) != nil || !c.GetReadyAt().AsTime().Equal(*in.ReadyAt) {
		t.Fatalf("created = %v", created)
	}
	if len(updated) != 2 {
		t.Fatalf("updates = %d", len(updated))
	}
	u1, u2 := &eventsv1.DeliveryUpdated{}, &eventsv1.DeliveryUpdated{}
	_ = updated[0].GetPayload().UnmarshalTo(u1)
	_ = updated[1].GetPayload().UnmarshalTo(u2)
	if !slices.Equal(u1.GetChangedFields(), []string{"ready_at"}) || !u1.GetReadyAt().AsTime().Equal(nv) ||
		!slices.Equal(u2.GetChangedFields(), []string{"ready_at"}) || u2.ReadyAt != nil {
		t.Fatalf("u1 = %v, u2 = %v", u1, u2)
	}
}
