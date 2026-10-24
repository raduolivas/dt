package domain

import "time"

// Event types on the DELIVERIES stream.
const (
	EventDeliveryCreated           = "delivery.created"
	EventStatusChanged             = "delivery.status_changed"
	EventPickupConfirmed           = "delivery.pickup_confirmed"
	EventPickupConfirmationPending = "delivery.pickup_confirmation_pending"
	EventDeliveryUpdated           = "delivery.updated"
	EventPersonalDataErased        = "delivery.personal_data_erased"
	EventCourierAssigned           = "courier.assigned"
	EventCourierUpserted           = "courier.upserted"
)

// Event is a domain event. Payload is one of the structs below; they mirror
// dt.events.v1 and never carry the customer first name, notes, dropoff label
// or metadata (spec §12). The adapter converts them to proto.
type Event struct {
	Type    string
	Payload any
}

// DeliveryCreated is the payload of delivery.created.
type DeliveryCreated struct {
	DeliveryID string
	ExternalID string
	Model      Model
	Status     Status
	Policy     PickupPolicy
	Pickup     Location
	PickupName string
	Dropoff    Location
	CreatedAt  time.Time
}

// StatusChanged is the payload of delivery.status_changed.
type StatusChanged struct {
	DeliveryID string
	From, To   Status
	Actor      ActorKind
	OccurredAt time.Time
	// Overrode is set when a partner report overrode the other side's state
	// under the conflict rule.
	Overrode bool
}

// PickupConfirmed is the payload of delivery.pickup_confirmed.
type PickupConfirmed struct {
	DeliveryID string
	By         ConfirmedBy
	OccurredAt time.Time
}

// PickupConfirmationPending is the payload of
// delivery.pickup_confirmation_pending.
type PickupConfirmationPending struct {
	DeliveryID  string
	ConfirmedBy []ConfirmedBy
	Since       time.Time
}

// DeliveryUpdated is the payload of delivery.updated. Dropoff is set only
// when the dropoff location changed.
type DeliveryUpdated struct {
	DeliveryID    string
	ChangedFields []string
	Dropoff       *Location
}

// PersonalDataErased is the payload of delivery.personal_data_erased.
type PersonalDataErased struct {
	DeliveryID string
	ErasedAt   time.Time
}

// CourierAssigned is the payload of courier.assigned.
type CourierAssigned struct {
	DeliveryID string
	CourierID  string
	AssignedAt time.Time
}

// CourierUpserted is the payload of courier.upserted.
type CourierUpserted struct {
	CourierID   string
	ExternalID  string
	DisplayName string
	Vehicle     Vehicle
	PhotoURL    string
}
