package domain

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func TestCanManage(t *testing.T) {
	tests := []struct {
		role      Role
		env       Environment
		forbidden bool
	}{
		{RoleOwner, EnvTest, false},
		{RoleOwner, EnvLive, false},
		{RoleAdmin, EnvTest, false},
		{RoleAdmin, EnvLive, false},
		{RoleDeveloper, EnvTest, false},
		{RoleDeveloper, EnvLive, true},
		{RoleSupport, EnvTest, true},
		{RoleSupport, EnvLive, true},
		{Role("bogus"), EnvTest, true},
		{RoleOwner, Environment("staging"), true},
	}
	for _, tc := range tests {
		err := CanManage(Actor{Type: "console_user", ID: "u1", Role: tc.role}, tc.env)
		if tc.forbidden != errors.Is(err, ErrForbiddenRole) {
			t.Errorf("role=%s env=%s: err=%v, forbidden=%v", tc.role, tc.env, err, tc.forbidden)
		}
		if !tc.forbidden && err != nil {
			t.Errorf("role=%s env=%s: unexpected %v", tc.role, tc.env, err)
		}
	}
}

func TestRoll(t *testing.T) {
	soon := t0.Add(time.Hour)
	late := t0.Add(72 * time.Hour)
	tests := []struct {
		name    string
		exp     *time.Time
		revoked *time.Time
		want    time.Time
		err     error
	}{
		{"no expiry", nil, nil, t0.Add(RollOverlap), nil},
		{"earlier expiry kept", &soon, nil, soon, nil},
		{"later expiry shortened", &late, nil, t0.Add(RollOverlap), nil},
		{"revoked", nil, &t0, time.Time{}, ErrKeyRevoked},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k := APIKey{ExpiresAt: tc.exp, RevokedAt: tc.revoked}
			got, err := k.Roll(t0)
			if !errors.Is(err, tc.err) {
				t.Fatalf("err=%v want %v", err, tc.err)
			}
			if err != nil {
				return
			}
			if !got.Equal(tc.want) || k.ExpiresAt == nil || !k.ExpiresAt.Equal(tc.want) {
				t.Fatalf("got %v, ExpiresAt %v, want %v", got, k.ExpiresAt, tc.want)
			}
		})
	}
}

func TestRevoke(t *testing.T) {
	k := APIKey{}
	if !k.Revoke(t0) || k.RevokedAt == nil || !k.RevokedAt.Equal(t0) {
		t.Fatal("first revoke should change")
	}
	if k.Revoke(t0.Add(time.Hour)) || !k.RevokedAt.Equal(t0) {
		t.Fatal("second revoke should be a no-op")
	}
}

func TestValidateScopes(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
		err  error
	}{
		{"empty gives all", nil, AllScopes, nil},
		{"dedupe and sort", []string{"webhooks:write", "deliveries:read", "webhooks:write"}, []string{"deliveries:read", "webhooks:write"}, nil},
		{"unknown", []string{"deliveries:read", "admin:all"}, nil, ErrInvalidScope},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateScopes(tc.in)
			if !errors.Is(err, tc.err) {
				t.Fatalf("err=%v want %v", err, tc.err)
			}
			if err == nil && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
	got, _ := ValidateScopes(nil)
	got[0] = "mutated"
	if AllScopes[0] == "mutated" {
		t.Fatal("AllScopes must not be aliased")
	}
}
