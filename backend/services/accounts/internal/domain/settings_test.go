package domain

import (
	"errors"
	"testing"
)

func TestTenantSettingsValidate(t *testing.T) {
	mk := func(f func(*TenantSettings)) TenantSettings {
		s := DefaultTenantSettings()
		f(&s)
		return s
	}
	tests := []struct {
		name string
		s    TenantSettings
		bad  bool
	}{
		{"defaults", DefaultTenantSettings(), false},
		{"direct only", mk(func(s *TenantSettings) { s.AllowedModels = []string{"direct"} }), false},
		{"end to end only", mk(func(s *TenantSettings) { s.AllowedModels, s.DefaultModel = []string{"end_to_end"}, "end_to_end" }), false},
		{"all policies", mk(func(s *TenantSettings) { s.PickupConfirmation = "courier_and_store" }), false},
		{"empty models", mk(func(s *TenantSettings) { s.AllowedModels = nil }), true},
		{"unknown model", mk(func(s *TenantSettings) { s.AllowedModels = []string{"direct", "drone"} }), true},
		{"duplicate model", mk(func(s *TenantSettings) { s.AllowedModels = []string{"direct", "direct"} }), true},
		{"default not allowed", mk(func(s *TenantSettings) { s.AllowedModels = []string{"end_to_end"} }), true},
		{"empty default", mk(func(s *TenantSettings) { s.DefaultModel = "" }), true},
		{"unknown policy", mk(func(s *TenantSettings) { s.PickupConfirmation = "nobody" }), true},
		{"empty policy", mk(func(s *TenantSettings) { s.PickupConfirmation = "" }), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.s.Validate()
			if tc.bad != (err != nil) {
				t.Fatalf("err = %v, bad = %v", err, tc.bad)
			}
			if tc.bad && !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("err %v does not wrap ErrInvalidArgument", err)
			}
		})
	}
}

func TestDefaultTenantSettingsNotAliased(t *testing.T) {
	a := DefaultTenantSettings()
	a.AllowedModels[0] = "mutated"
	if DefaultTenantSettings().AllowedModels[0] != "direct" {
		t.Fatal("defaults must not be aliased")
	}
}

func TestTenantSettingsPrepRange(t *testing.T) {
	ptr := func(n int) *int { return &n }
	tests := []struct {
		name string
		prep *int
		bad  bool
	}{
		{"nil", nil, false}, {"1", ptr(1), false}, {"240", ptr(240), false},
		{"0", ptr(0), true}, {"241", ptr(241), true}, {"negative", ptr(-5), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := DefaultTenantSettings()
			s.DefaultPrepMinutes = tc.prep
			err := s.Validate()
			if tc.bad != (err != nil) {
				t.Fatalf("err = %v, bad = %v", err, tc.bad)
			}
			if tc.bad && !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("err %v does not wrap ErrInvalidArgument", err)
			}
		})
	}
}
