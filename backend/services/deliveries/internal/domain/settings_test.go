package domain

import "testing"

func TestTenantSettingsValid(t *testing.T) {
	ok := func(s TenantSettings) TenantSettings { return s }
	for name, tc := range map[string]struct {
		s    TenantSettings
		want bool
	}{
		"defaults": {DefaultTenantSettings(), true},
		"end to end only": {ok(TenantSettings{
			AllowedModels: []Model{ModelEndToEnd}, DefaultModel: ModelEndToEnd, PickupPolicy: PolicyCourierAndStore,
		}), true},
		"no models":           {TenantSettings{DefaultModel: ModelDirect, PickupPolicy: PolicyAny}, false},
		"unknown model":       {TenantSettings{AllowedModels: []Model{ModelDirect, "drone"}, DefaultModel: ModelDirect, PickupPolicy: PolicyAny}, false},
		"duplicate model":     {TenantSettings{AllowedModels: []Model{ModelDirect, ModelDirect}, DefaultModel: ModelDirect, PickupPolicy: PolicyAny}, false},
		"default not allowed": {TenantSettings{AllowedModels: []Model{ModelDirect}, DefaultModel: ModelEndToEnd, PickupPolicy: PolicyAny}, false},
		"empty default":       {TenantSettings{AllowedModels: []Model{ModelDirect}, PickupPolicy: PolicyAny}, false},
		"unknown policy":      {TenantSettings{AllowedModels: []Model{ModelDirect}, DefaultModel: ModelDirect, PickupPolicy: "drone"}, false},
		"empty policy":        {TenantSettings{AllowedModels: []Model{ModelDirect}, DefaultModel: ModelDirect}, false},
	} {
		if got := tc.s.Valid(); got != tc.want {
			t.Errorf("%s: Valid() = %v, want %v", name, got, tc.want)
		}
	}
}
