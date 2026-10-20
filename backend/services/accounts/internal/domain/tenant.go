package domain

import "time"

// Tenant is a partner account that owns API keys.
type Tenant struct {
	ID          string
	Name        string
	LiveEnabled bool
	CreatedAt   time.Time
}
