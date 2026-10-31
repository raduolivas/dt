package domain

// PickupConfirmation is one recorded pickup confirmation.
type PickupConfirmation struct{ By ConfirmedBy }

// NextActions returns the event types the courier may report now, in the
// order at_pickup, pickup_confirmed, delivered, failed. It reads the same
// transitions table as Report, so the two cannot disagree. arriving is never
// offered: the courier app does not report it. pickup_confirmed is offered
// while the move to picked_up is open to the courier, unless the policy
// leaves the confirmation to the store or the courier already confirmed.
func NextActions(m Model, s Status, policy PickupPolicy, confs []PickupConfirmation) []string {
	out := []string{}
	if allowed(m, ActorCourier, s, StatusAtPickup) {
		out = append(out, string(ReportAtPickup))
	}
	if allowed(m, ActorCourier, s, StatusPickedUp) && policy != PolicyStore && !courierConfirmed(confs) {
		out = append(out, string(ReportPickupConfirmed))
	}
	if allowed(m, ActorCourier, s, StatusDelivered) {
		out = append(out, string(ReportDelivered))
	}
	if allowed(m, ActorCourier, s, StatusFailed) {
		out = append(out, string(ReportFailed))
	}
	return out
}

func courierConfirmed(confs []PickupConfirmation) bool {
	for _, c := range confs {
		if c.By == ByCourier {
			return true
		}
	}
	return false
}

// CourierNextActions is NextActions for d's current state and history.
func (d *Delivery) CourierNextActions() []string {
	var confs []PickupConfirmation
	for _, by := range d.confirmations() {
		confs = append(confs, PickupConfirmation{By: by})
	}
	return NextActions(d.Model, d.Status, d.Policy, confs)
}
