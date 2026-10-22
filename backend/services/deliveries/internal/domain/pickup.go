package domain

// PickupPolicy is which confirmations a handover needs (spec §4.4).
type PickupPolicy string

// Pickup policies.
const (
	PolicyAny             PickupPolicy = "any"
	PolicyCourier         PickupPolicy = "courier"
	PolicyStore           PickupPolicy = "store"
	PolicyCourierAndStore PickupPolicy = "courier_and_store"
)

// ConfirmedBy is who confirmed a pickup.
type ConfirmedBy string

// Pickup confirmers.
const (
	ByCourier ConfirmedBy = "courier"
	ByStore   ConfirmedBy = "store"
	ByPartner ConfirmedBy = "partner"
)

// PolicySatisfied reports whether the confirmations satisfy policy p.
func PolicySatisfied(p PickupPolicy, confirmed []ConfirmedBy) bool {
	switch p {
	case PolicyAny:
		return len(confirmed) > 0
	case PolicyCourier:
		return contains(confirmed, ByCourier)
	case PolicyStore:
		return contains(confirmed, ByStore)
	case PolicyCourierAndStore:
		return contains(confirmed, ByCourier) && contains(confirmed, ByStore)
	}
	return false
}
