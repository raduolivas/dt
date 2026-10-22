package domain

import "slices"

// TenantSettings are the tenant's delivery settings, as last received in a
// tenant.settings_set event.
type TenantSettings struct {
	AllowedModels []Model
	DefaultModel  Model
	PickupPolicy  PickupPolicy
}

// Valid reports whether s can be applied: the allowed models are non-empty,
// known and unique, the default is one of them and the pickup policy is
// known (the same rules accounts enforces when the settings are set).
func (s TenantSettings) Valid() bool {
	if len(s.AllowedModels) == 0 {
		return false
	}
	for i, m := range s.AllowedModels {
		if !m.valid() || slices.Contains(s.AllowedModels[:i], m) {
			return false
		}
	}
	return slices.Contains(s.AllowedModels, s.DefaultModel) && s.PickupPolicy.valid()
}

// DefaultTenantSettings are used until a tenant's settings arrive.
func DefaultTenantSettings() TenantSettings {
	return TenantSettings{
		AllowedModels: []Model{ModelDirect, ModelEndToEnd},
		DefaultModel:  ModelDirect,
		PickupPolicy:  PolicyAny,
	}
}

// withDefaults fills empty fields from DefaultTenantSettings.
func (s TenantSettings) withDefaults() TenantSettings {
	d := DefaultTenantSettings()
	if len(s.AllowedModels) == 0 {
		s.AllowedModels = d.AllowedModels
	}
	if s.DefaultModel == "" {
		s.DefaultModel = d.DefaultModel
	}
	if s.PickupPolicy == "" {
		s.PickupPolicy = d.PickupPolicy
	}
	return s
}
