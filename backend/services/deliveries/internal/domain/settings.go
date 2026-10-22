package domain

// TenantSettings are the tenant's delivery settings, as last received in a
// tenant.settings_set event.
type TenantSettings struct {
	AllowedModels []Model
	DefaultModel  Model
	PickupPolicy  PickupPolicy
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
