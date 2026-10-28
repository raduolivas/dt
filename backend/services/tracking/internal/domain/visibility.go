package domain

// CourierVisible is the MVP spec §4.5 table: whether the customer may see the
// courier for a delivery of model m in status s. It is decided per delivery.
func CourierVisible(m Model, s Status) bool {
	switch s {
	case StatusPickedUp, StatusArriving:
		return m == ModelDirect || m == ModelEndToEnd
	case StatusCourierAssigned, StatusAtPickup:
		return m == ModelEndToEnd
	default:
		return false
	}
}
