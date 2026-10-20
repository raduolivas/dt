package domain

// Scope is a permission a credential carries, e.g. "deliveries:read".
type Scope string

// The scopes a secret key may carry (spec §5).
const (
	ScopeDeliveriesRead  Scope = "deliveries:read"
	ScopeDeliveriesWrite Scope = "deliveries:write"
	ScopeCouriersWrite   Scope = "couriers:write"
	ScopeWebhooksWrite   Scope = "webhooks:write"
)

// AllScopes lists every scope, in display order.
var AllScopes = []Scope{ScopeDeliveriesRead, ScopeDeliveriesWrite, ScopeCouriersWrite, ScopeWebhooksWrite}
