package domain

import "time"

// Vehicle is how a courier travels.
type Vehicle string

// Vehicles.
const (
	VehicleBicycle Vehicle = "bicycle"
	VehicleEBike   Vehicle = "e_bike"
	VehicleScooter Vehicle = "scooter"
	VehicleCar     Vehicle = "car"
	VehicleOnFoot  Vehicle = "on_foot"
)

func (v Vehicle) valid() bool {
	switch v {
	case VehicleBicycle, VehicleEBike, VehicleScooter, VehicleCar, VehicleOnFoot:
		return true
	}
	return false
}

// CourierInput is what a partner sends to upsert a courier.
type CourierInput struct {
	ExternalID, DisplayName, PhotoURL string
	Vehicle                           Vehicle
}

// Courier is a partner's courier.
type Courier struct {
	ID, ExternalID, DisplayName, PhotoURL string
	Vehicle                               Vehicle
	CreatedAt, UpdatedAt                  time.Time
}

// CourierRef is the courier identity attached to a delivery.
type CourierRef struct {
	ID, ExternalID, DisplayName string
	Vehicle                     Vehicle
}

// NewCourier validates in and returns a courier stamped with now.
func NewCourier(id string, in CourierInput, now time.Time) (Courier, error) {
	if err := checkASCIIID("external_id", in.ExternalID); err != nil {
		return Courier{}, err
	}
	if in.DisplayName == "" {
		return Courier{}, invalid("display_name", "required", "must not be empty")
	}
	if err := checkLen("display_name", in.DisplayName, maxDisplayNameLen); err != nil {
		return Courier{}, err
	}
	if err := checkPhotoURL(in.PhotoURL); err != nil {
		return Courier{}, err
	}
	if !in.Vehicle.valid() {
		return Courier{}, invalid("vehicle", "invalid", "must be bicycle, e_bike, scooter, car or on_foot")
	}
	return Courier{
		ID: id, ExternalID: in.ExternalID, DisplayName: in.DisplayName, PhotoURL: in.PhotoURL,
		Vehicle: in.Vehicle, CreatedAt: now, UpdatedAt: now,
	}, nil
}

// UpsertedEvent is the courier.upserted event for c.
func (c Courier) UpsertedEvent() Event {
	return Event{Type: EventCourierUpserted, Payload: CourierUpserted{
		CourierID: c.ID, ExternalID: c.ExternalID, DisplayName: c.DisplayName,
		Vehicle: c.Vehicle, PhotoURL: c.PhotoURL,
	}}
}
