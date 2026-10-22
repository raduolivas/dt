package domain

// Status is a delivery state (spec §4.2).
type Status string

// Delivery states.
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

// Final reports whether s is a final state.
func (s Status) Final() bool {
	return s == StatusDelivered || s == StatusCancelled || s == StatusFailed
}

// rank orders the states along the lifecycle; the final states share the
// highest rank.
func (s Status) rank() int {
	switch s {
	case StatusConfirmed:
		return 0
	case StatusCourierAssigned:
		return 1
	case StatusAtPickup:
		return 2
	case StatusPickedUp:
		return 3
	case StatusArriving:
		return 4
	default:
		return 5
	}
}

// ActorKind is who triggered a change.
type ActorKind string

// Actors. The partner is one side of the conflict rule; courier and system
// are the other.
const (
	ActorPartner ActorKind = "partner"
	ActorCourier ActorKind = "courier"
	ActorSystem  ActorKind = "system"
)

func (a ActorKind) valid() bool {
	return a == ActorPartner || a == ActorCourier || a == ActorSystem
}

// opposes reports whether a and b are on different sides (partner vs
// courier or system).
func (a ActorKind) opposes(b ActorKind) bool { return (a == ActorPartner) != (b == ActorPartner) }

// ReportType is the kind of a status report.
type ReportType string

// Report types.
const (
	ReportAtPickup        ReportType = "at_pickup"
	ReportPickupConfirmed ReportType = "pickup_confirmed"
	ReportArriving        ReportType = "arriving"
	ReportDelivered       ReportType = "delivered"
	ReportFailed          ReportType = "failed"
)

// target is the state a report asks for; pickup_confirmed asks for
// picked_up subject to the pickup policy.
func (t ReportType) target() Status {
	switch t {
	case ReportAtPickup:
		return StatusAtPickup
	case ReportPickupConfirmed:
		return StatusPickedUp
	case ReportArriving:
		return StatusArriving
	case ReportDelivered:
		return StatusDelivered
	case ReportFailed:
		return StatusFailed
	}
	return ""
}

// Outcome is what a report did.
type Outcome string

// Report outcomes.
const (
	OutcomeApplied   Outcome = "applied"
	OutcomeDuplicate Outcome = "duplicate"
	OutcomeStale     Outcome = "stale"
	OutcomeConflict  Outcome = "conflict"
	OutcomeRecorded  Outcome = "recorded"
)
