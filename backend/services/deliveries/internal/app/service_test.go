package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/raduolivas/dt/backend/services/deliveries/internal/app"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/domain"
)

var (
	scope = app.Scope{TenantID: "ten_1", Env: "test", ActorID: "ak_1"}
	t0    = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
)

func newSvc() (*app.Service, *fakeRepo, *fakeClock) {
	r := newFakeRepo()
	c := &fakeClock{now: t0}
	return &app.Service{Repo: r, Clock: c, IDs: &fakeIDs{}}, r, c
}

func input(ext string) domain.NewDeliveryInput {
	return domain.NewDeliveryInput{
		ExternalID: ext,
		Pickup:     domain.Location{Lat: -23.55, Lng: -46.63},
		PickupName: "Store",
		Dropoff:    domain.Location{Lat: -23.56, Lng: -46.65},
	}
}

func TestCreateUsesDefaultsWithoutSettings(t *testing.T) {
	s, r, _ := newSvc()
	d, err := s.Create(context.Background(), scope, input("a"))
	if err != nil {
		t.Fatal(err)
	}
	if d.Model != domain.ModelDirect || d.Policy != domain.PolicyAny || d.Status != domain.StatusConfirmed {
		t.Fatalf("got %+v", d)
	}
	if d.ID != "del_001" || d.TenantID != "ten_1" || d.Env != "test" {
		t.Fatalf("ids: %+v", d)
	}
	if len(r.events) != 1 || r.events[0].Type != domain.EventDeliveryCreated {
		t.Fatalf("events: %+v", r.events)
	}
}

func TestSettingsChangeDoesNotAffectExistingDelivery(t *testing.T) {
	s, r, _ := newSvc()
	d, err := s.Create(context.Background(), scope, input("a"))
	if err != nil {
		t.Fatal(err)
	}
	r.settings = domain.TenantSettings{
		AllowedModels: []domain.Model{domain.ModelEndToEnd},
		DefaultModel:  domain.ModelEndToEnd,
		PickupPolicy:  domain.PolicyCourierAndStore,
	}
	got, err := s.Get(context.Background(), scope, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Policy != domain.PolicyAny || got.Model != domain.ModelDirect {
		t.Fatalf("policy changed: %+v", got)
	}
	d2, err := s.Create(context.Background(), scope, input("b"))
	if err != nil {
		t.Fatal(err)
	}
	if d2.Policy != domain.PolicyCourierAndStore {
		t.Fatalf("new delivery policy %q", d2.Policy)
	}
}

func TestCreateDuplicateExternalID(t *testing.T) {
	s, _, _ := newSvc()
	if _, err := s.Create(context.Background(), scope, input("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(context.Background(), scope, input("a")); !errors.Is(err, app.ErrExternalIDTaken) {
		t.Fatalf("got %v", err)
	}
}

func TestCreateValidationErrorPassesThrough(t *testing.T) {
	s, _, _ := newSvc()
	in := input("")
	_, err := s.Create(context.Background(), scope, in)
	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("got %v", err)
	}
}

func TestAssignUnknownCourier(t *testing.T) {
	s, _, _ := newSvc()
	d, _ := s.Create(context.Background(), scope, input("a"))
	if _, err := s.Assign(context.Background(), scope, d.ID, "cou_x"); !errors.Is(err, app.ErrCourierNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestAssignUnknownDelivery(t *testing.T) {
	s, r, _ := newSvc()
	r.couriers["cou_1"] = domain.Courier{ID: "cou_1"}
	if _, err := s.Assign(context.Background(), scope, "del_x", "cou_1"); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestAssignCancelUpdateErase(t *testing.T) {
	ctx := context.Background()
	s, r, _ := newSvc()
	c, created, err := s.UpsertCourier(ctx, scope, domain.CourierInput{ExternalID: "x1", DisplayName: "Ana", Vehicle: "car"})
	if err != nil || !created || c.ID != "cou_001" {
		t.Fatalf("%+v %v %v", c, created, err)
	}
	c2, created, err := s.UpsertCourier(ctx, scope, domain.CourierInput{ExternalID: "x1", DisplayName: "Ana B", Vehicle: "car"})
	if err != nil || created || c2.ID != c.ID {
		t.Fatalf("%+v %v %v", c2, created, err)
	}
	if _, _, err := s.UpsertCourier(ctx, scope, domain.CourierInput{ExternalID: "x1"}); err == nil {
		t.Fatal("expected validation error")
	}
	r.couriers[c.ID] = c2

	d, _ := s.Create(ctx, scope, input("a"))
	d, err = s.Assign(ctx, scope, d.ID, c.ID)
	if err != nil || d.Courier == nil || d.Courier.ID != c.ID {
		t.Fatalf("%+v %v", d, err)
	}
	notes := "ring twice"
	n := &notes
	d, err = s.Update(ctx, scope, d.ID, domain.Patch{Notes: &n})
	if err != nil || d.Notes == nil || *d.Notes != notes {
		t.Fatalf("%+v %v", d, err)
	}
	d, err = s.Cancel(ctx, scope, d.ID, "customer asked")
	if err != nil || d.Status != domain.StatusCancelled {
		t.Fatalf("%+v %v", d, err)
	}
	d, err = s.ErasePersonalData(ctx, scope, d.ID)
	if err != nil || d.ErasedAt == nil {
		t.Fatalf("%+v %v", d, err)
	}
	if _, err := s.Cancel(ctx, scope, d.ID, "again"); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("got %v", err)
	}
}

func TestReportPassesReceivedAtFromClock(t *testing.T) {
	ctx := context.Background()
	s, r, c := newSvc()
	d, _ := s.Create(ctx, scope, input("a"))
	c.now = t0.Add(time.Minute)
	got, out, err := s.Report(ctx, scope, d.ID, domain.Report{EventID: "e1", Type: domain.ReportAtPickup, Actor: domain.ActorPartner})
	if err != nil || out != domain.OutcomeApplied || got.Status != domain.StatusAtPickup {
		t.Fatalf("%+v %v %v", got, out, err)
	}
	h := r.deliveries[d.ID].History
	last := h[len(h)-1]
	if !last.ReceivedAt.Equal(c.now) || !last.OccurredAt.Equal(c.now) {
		t.Fatalf("history %+v", last)
	}
	_, out, err = s.Report(ctx, scope, d.ID, domain.Report{EventID: "e1", Type: domain.ReportAtPickup, Actor: domain.ActorPartner})
	if err != nil || out != domain.OutcomeDuplicate {
		t.Fatalf("%v %v", out, err)
	}
}

func TestMutateErrorWritesNothing(t *testing.T) {
	ctx := context.Background()
	s, r, _ := newSvc()
	d, _ := s.Create(ctx, scope, input("a"))
	before := len(r.events)
	_, _, err := s.Report(ctx, scope, d.ID, domain.Report{EventID: "e1", Type: domain.ReportDelivered, Actor: domain.ActorPartner})
	if !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("got %v", err)
	}
	if len(r.events) != before || r.deliveries[d.ID].Status != domain.StatusConfirmed || len(r.deliveries[d.ID].History) != len(d.History) {
		t.Fatal("state written despite error")
	}
}

func TestListClampsLimit(t *testing.T) {
	s, r, _ := newSvc()
	for _, tc := range []struct{ in, want int }{{0, 20}, {-3, 20}, {500, 100}, {7, 7}} {
		if _, _, err := s.List(context.Background(), scope, app.ListFilter{Limit: tc.in}); err != nil {
			t.Fatal(err)
		}
		if r.lastList.Limit != tc.want {
			t.Errorf("limit %d -> %d, want %d", tc.in, r.lastList.Limit, tc.want)
		}
	}
}
