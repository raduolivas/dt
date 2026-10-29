package domain

import (
	"fmt"
	"slices"
	"time"
)

// Delivery models and pickup confirmation policies a tenant may configure.
const (
	ModelDirect           = "direct"
	ModelEndToEnd         = "end_to_end"
	PickupAny             = "any"
	PickupCourier         = "courier"
	PickupStore           = "store"
	PickupCourierAndStore = "courier_and_store"
)

var (
	knownModels   = []string{ModelDirect, ModelEndToEnd}
	knownPolicies = []string{PickupAny, PickupCourier, PickupStore, PickupCourierAndStore}
)

// TenantSettings are a tenant's delivery settings.
type TenantSettings struct {
	AllowedModels      []string
	DefaultModel       string
	PickupConfirmation string
	// DefaultPrepMinutes is the usual preparation time; nil = none. 1..240.
	DefaultPrepMinutes *int
	// EmbedOrigins are the https origins allowed to frame the hosted tracking page.
	EmbedOrigins []string
	// Branding is the hosted page's look; nil = DT defaults.
	Branding  *Branding
	UpdatedAt time.Time
}

// DefaultTenantSettings returns the settings every new tenant starts with.
// UpdatedAt is left for the caller to stamp.
func DefaultTenantSettings() TenantSettings {
	return TenantSettings{
		AllowedModels:      []string{ModelDirect, ModelEndToEnd},
		DefaultModel:       ModelDirect,
		PickupConfirmation: PickupAny,
	}
}

// Validate reports ErrInvalidArgument unless the allowed models are non-empty,
// unique and known, the default is among them and the policy is known.
func (s TenantSettings) Validate() error {
	if len(s.AllowedModels) == 0 {
		return fmt.Errorf("%w: allowed_models must not be empty", ErrInvalidArgument)
	}
	for i, m := range s.AllowedModels {
		if !slices.Contains(knownModels, m) {
			return fmt.Errorf("%w: unknown model %q", ErrInvalidArgument, m)
		}
		if slices.Contains(s.AllowedModels[:i], m) {
			return fmt.Errorf("%w: duplicate model %q", ErrInvalidArgument, m)
		}
	}
	if !slices.Contains(s.AllowedModels, s.DefaultModel) {
		return fmt.Errorf("%w: default_model %q is not an allowed model", ErrInvalidArgument, s.DefaultModel)
	}
	if !slices.Contains(knownPolicies, s.PickupConfirmation) {
		return fmt.Errorf("%w: unknown pickup_confirmation %q", ErrInvalidArgument, s.PickupConfirmation)
	}
	if p := s.DefaultPrepMinutes; p != nil && (*p < 1 || *p > 240) {
		return fmt.Errorf("%w: default_prep_minutes must be 1..240", ErrInvalidArgument)
	}
	if err := validateEmbedOrigins(s.EmbedOrigins); err != nil {
		return err
	}
	if s.Branding != nil {
		return s.Branding.Validate()
	}
	return nil
}
