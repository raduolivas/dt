package domain

// transition is one row of the spec §4.3 table: who may move a delivery of
// the given models from one of from to to. Reports, Assign and Cancel all
// consult this table.
type transition struct {
	from   []Status
	to     Status
	actors []ActorKind
	models []Model
}

var (
	bothModels       = []Model{ModelDirect, ModelEndToEnd}
	partnerOnly      = []ActorKind{ActorPartner}
	partnerOrCourier = []ActorKind{ActorPartner, ActorCourier}
	anyActor         = []ActorKind{ActorPartner, ActorCourier, ActorSystem}
	prePickup        = []Status{StatusConfirmed, StatusCourierAssigned, StatusAtPickup}
	postPickup       = []Status{StatusPickedUp, StatusArriving}
)

var transitions = []transition{
	// Assign (end_to_end only; in direct, assigning does not change state).
	{[]Status{StatusConfirmed}, StatusCourierAssigned, partnerOnly, []Model{ModelEndToEnd}},
	// end_to_end needs courier_assigned before at_pickup or picked_up.
	{[]Status{StatusConfirmed}, StatusAtPickup, partnerOrCourier, []Model{ModelDirect}},
	{[]Status{StatusCourierAssigned}, StatusAtPickup, partnerOrCourier, bothModels},
	// Reached via the pickup policy.
	{[]Status{StatusConfirmed}, StatusPickedUp, partnerOrCourier, []Model{ModelDirect}},
	{[]Status{StatusCourierAssigned, StatusAtPickup}, StatusPickedUp, partnerOrCourier, bothModels},
	{[]Status{StatusPickedUp}, StatusArriving, anyActor, bothModels},
	{postPickup, StatusDelivered, partnerOrCourier, bothModels},
	{postPickup, StatusFailed, partnerOrCourier, bothModels},
	// Cancel.
	{prePickup, StatusCancelled, partnerOnly, bothModels},
}

// allowed reports whether actor a may move a delivery of model m from
// from to to.
func allowed(m Model, a ActorKind, from, to Status) bool {
	for _, t := range transitions {
		if t.to == to && contains(t.from, from) && contains(t.actors, a) && contains(t.models, m) {
			return true
		}
	}
	return false
}

func contains[T comparable](s []T, v T) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
