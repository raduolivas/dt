package app_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/services/accounts/internal/app"
	"github.com/raduolivas/dt/backend/services/accounts/internal/domain"
)

func settingsEvents(e *env) []published {
	var out []published
	for _, ev := range e.repo.events {
		if ev.Type == "tenant.settings_set" {
			out = append(out, ev)
		}
	}
	return out
}

func TestCreateTenantEmitsDefaultSettings(t *testing.T) {
	repo := newFakeRepo()
	svc := &app.Service{Repo: repo, Keys: &fakeKeys{}, Clock: &fixedClock{now}}
	ten, err := svc.CreateTenant(context.Background(), "Acme")
	if err != nil {
		t.Fatal(err)
	}
	e := &env{repo: repo, svc: svc, ten: ten}
	evs := settingsEvents(e)
	if len(evs) != 1 || len(e.repo.events) != 1 {
		t.Fatalf("events = %v", e.eventTypes())
	}
	ev := evs[0]
	if ev.TenantID != e.ten.ID || ev.Env != "" {
		t.Fatalf("event = %+v", ev)
	}
	p := ev.Payload.(*eventsv1.TenantSettingsSet)
	if p.GetTenantId() != e.ten.ID || !reflect.DeepEqual(p.GetAllowedModels(), []string{"direct", "end_to_end"}) ||
		p.GetDefaultModel() != "direct" || p.GetPickupConfirmation() != "any" || !p.GetUpdatedAt().AsTime().Equal(now) {
		t.Fatalf("payload = %v", p)
	}
	want := domain.DefaultTenantSettings()
	want.UpdatedAt = now
	if !reflect.DeepEqual(e.ten.Settings, want) || !reflect.DeepEqual(e.repo.tenants[e.ten.ID].Settings, want) {
		t.Fatalf("settings = %+v, stored %+v", e.ten.Settings, e.repo.tenants[e.ten.ID].Settings)
	}
}

func TestSetTenantSettingsRoles(t *testing.T) {
	e := setup(t, false)
	s := domain.TenantSettings{AllowedModels: []string{"direct"}, DefaultModel: "direct", PickupConfirmation: "store"}
	tests := []struct {
		name  string
		actor domain.Actor
		want  error
	}{
		{"developer", dev, domain.ErrForbiddenRole},
		{"support", support, domain.ErrForbiddenRole},
		{"unknown", domain.Actor{Role: "bogus"}, domain.ErrForbiddenRole},
		{"owner", owner, nil},
		{"admin", domain.Actor{Role: domain.RoleAdmin}, nil},
		{"system", domain.Actor{Type: "system", ID: "dtctl", Role: "system"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := len(e.repo.events)
			_, err := e.svc.SetTenantSettings(context.Background(), tc.actor, e.ten.ID, s)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if tc.want != nil && len(e.repo.events) != before {
				t.Fatal("refused call must not publish")
			}
		})
	}
}

func TestSetTenantSettingsEmitsEvent(t *testing.T) {
	e := setup(t, false)
	later := now.Add(time.Hour)
	e.clk.t = later
	got, err := e.svc.SetTenantSettings(context.Background(), owner, e.ten.ID, domain.TenantSettings{
		AllowedModels: []string{"end_to_end"}, DefaultModel: "end_to_end", PickupConfirmation: "courier_and_store",
		UpdatedAt: now.Add(-time.Hour), // caller's stamp is ignored
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.UpdatedAt.Equal(later) || got.PickupConfirmation != "courier_and_store" {
		t.Fatalf("got %+v", got)
	}
	if !reflect.DeepEqual(e.repo.tenants[e.ten.ID].Settings, got) {
		t.Fatalf("stored %+v, want %+v", e.repo.tenants[e.ten.ID].Settings, got)
	}
	evs := settingsEvents(e)
	if len(evs) != 1 {
		t.Fatalf("settings events = %d", len(evs))
	}
	p := evs[0].Payload.(*eventsv1.TenantSettingsSet)
	if evs[0].TenantID != e.ten.ID || p.GetTenantId() != e.ten.ID ||
		!reflect.DeepEqual(p.GetAllowedModels(), []string{"end_to_end"}) || p.GetDefaultModel() != "end_to_end" ||
		p.GetPickupConfirmation() != "courier_and_store" || !p.GetUpdatedAt().AsTime().Equal(later) {
		t.Fatalf("payload = %v", p)
	}
}

func TestSetTenantSettingsErrors(t *testing.T) {
	e := setup(t, false)
	bad := domain.TenantSettings{AllowedModels: []string{"direct"}, DefaultModel: "direct", PickupConfirmation: "nobody"}
	if _, err := e.svc.SetTenantSettings(context.Background(), owner, e.ten.ID, bad); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("invalid: %v", err)
	}
	ok := domain.DefaultTenantSettings()
	if _, err := e.svc.SetTenantSettings(context.Background(), owner, "ten_missing", ok); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing tenant: %v", err)
	}
	if n := len(settingsEvents(e)); n != 0 {
		t.Fatalf("settings events = %d, want none", n)
	}
}
