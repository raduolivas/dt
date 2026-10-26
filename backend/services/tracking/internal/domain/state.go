package domain

import "time"

// Model is a delivery model (MVP spec §4.1). Spellings match deliveries.
type Model string

// Delivery models.
const (
	ModelDirect   Model = "direct"
	ModelEndToEnd Model = "end_to_end"
)

// Status is a delivery status. Spellings match deliveries.
type Status string

// Delivery statuses.
const (
	StatusConfirmed       Status = "confirmed"
	StatusCourierAssigned Status = "courier_assigned"
	StatusAtPickup        Status = "at_pickup"
	StatusPickedUp        Status = "picked_up"
	StatusArriving        Status = "arriving"
	StatusDelivered       Status = "delivered"
	StatusCancelled       Status = "cancelled"
	StatusFailed          Status = "failed"
)

// IsFinal reports whether s is a final status.
func (s Status) IsFinal() bool {
	return s == StatusDelivered || s == StatusCancelled || s == StatusFailed
}

// BeforePickup reports whether the parcel has not left the store yet.
func (s Status) BeforePickup() bool {
	return s == StatusConfirmed || s == StatusCourierAssigned || s == StatusAtPickup
}

// Signal is the freshness of the courier position (design §3.3).
type Signal string

// Signal values.
const (
	SignalPending Signal = "pending"
	SignalLive    Signal = "live"
	SignalStale   Signal = "stale"
	SignalLost    Signal = "lost"
)

// EtaState says how the ETA was obtained (design §5.1).
type EtaState string

// ETA states.
const (
	EtaCalculating EtaState = "calculating"
	EtaEstimated   EtaState = "estimated"
	EtaLive        EtaState = "live"
)

// LatLng is a WGS84 coordinate.
type LatLng struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// Fix is one courier position report.
type Fix struct {
	T          time.Time
	Pos        LatLng
	AccuracyM  float64
	SpeedMps   *float64
	HeadingDeg *float64
	Source     string
}

// Courier is the assigned courier.
type Courier struct {
	ID          string
	ExternalID  string
	DisplayName string
	Vehicle     string
	PhotoURL    *string
}

// Route holds the route legs as point lists and their drive times.
type Route struct {
	ToPickup     []LatLng
	ToDropoff    []LatLng
	ToPickupSec  int
	ToDropoffSec int
	// Estimated is true when the legs are a fallback (straight line or last known route).
	Estimated bool

	// Bookkeeping of the routing in tracking/internal/app.

	// DropoffStage says where ToDropoff starts: RouteFromStore or RouteFromCourier.
	DropoffStage string
	// DropoffEstimated and PickupEstimated say a leg is a fallback.
	DropoffEstimated, PickupEstimated bool
	// Profile is the routing profile the legs were built for (car or bike).
	Profile string
	// BaseDropoffSec and BasePickupSec are the router's drive time for the
	// whole of each leg as built; the live seconds scale them by the share
	// of the leg left.
	BaseDropoffSec, BasePickupSec int
	// RoutedAt is when the router was last asked (also after a failure).
	RoutedAt time.Time
	// ComputedAt is when the drive times were last recomputed.
	ComputedAt time.Time
}

// Where the dropoff leg starts.
const (
	RouteFromStore   = "store"
	RouteFromCourier = "courier"
)

// TimelineEntry is one status change.
type TimelineEntry struct {
	Status Status
	At     time.Time
}

// State is the live tracking state of one delivery.
type State struct {
	DeliveryID string
	TenantID   string
	Env        string
	Model      Model
	Status     Status
	Timeline   []TimelineEntry

	Pickup     LatLng
	PickupName string
	Dropoff    LatLng

	CreatedAt          time.Time
	ReadyAt            *time.Time
	DefaultPrepMinutes *int

	Courier             *Courier
	CourierVisibleSince *time.Time
	LastFix             *Fix

	Route          Route
	EtaSeconds     *int
	EtaState       EtaState
	EtaComputedAt  time.Time
	LastEtaEventAt time.Time
	// LastEtaEventSec is the ETA last published as an event.
	LastEtaEventSec *int

	ArrivalSent    bool
	SignalLostSent bool

	// Version is the state revision: it increases with every stored change
	// (the store's compare-and-set key) and is the partner view's version.
	Version int64
	// ViewerVersion is the customer snapshot's version: it takes the value of
	// Version only when the snapshot's content changed, so a change the
	// customer cannot see (a hidden courier's fix) leaves it alone.
	ViewerVersion int64
	// UpdatedAt is when the customer-visible state last changed.
	UpdatedAt time.Time
}

// ApplyFix records f as the live position when it is newer than the current
// one. It returns whether the live position changed. UpdatedAt moves only
// when the customer can see the courier, so a hidden courier's fixes leave
// the viewer snapshot untouched.
func (st *State) ApplyFix(f Fix, now time.Time) bool {
	if st.LastFix != nil && !f.T.After(st.LastFix.T) {
		return false
	}
	c := f
	st.LastFix = &c
	if CourierVisible(st.Model, st.Status) {
		st.UpdatedAt = now
	}
	return true
}

// NextVersion returns a snapshot version greater than prev, following the
// clock (milliseconds) when it is ahead.
func NextVersion(prev int64, now time.Time) int64 {
	v := now.UnixMilli()
	if v <= prev {
		return prev + 1
	}
	return v
}
