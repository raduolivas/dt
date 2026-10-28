package domain

import "time"

const etaHalfWindowSec = 300

// ReadyTime is when the order is ready: ready_at, else creation plus the
// default preparation time, else unknown.
func ReadyTime(st State) (time.Time, bool) {
	if st.ReadyAt != nil {
		return *st.ReadyAt, true
	}
	if st.DefaultPrepMinutes != nil {
		return st.CreatedAt.Add(time.Duration(*st.DefaultPrepMinutes) * time.Minute), true
	}
	return time.Time{}, false
}

// ComputeETA applies the design §5.1 rules. Before pickup it returns the ETA
// and a window of ±300 s; after pickup the drive time and no window.
func ComputeETA(st State, now time.Time) (eta *int, window *[2]int, state EtaState) {
	switch {
	case st.Status.IsFinal():
		return nil, nil, EtaLive
	case st.Status == StatusPickedUp || st.Status == StatusArriving:
		v := st.Route.ToDropoffSec
		return &v, nil, EtaLive
	}
	ready, ok := ReadyTime(st)
	if !ok {
		return nil, nil, EtaCalculating
	}
	start := ready
	if st.Model == ModelEndToEnd && st.Courier != nil && CourierVisible(st.Model, st.Status) {
		if at := now.Add(time.Duration(st.Route.ToPickupSec) * time.Second); at.After(start) {
			start = at
		}
	}
	wait := max(0, int(start.Sub(now).Seconds()))
	v := wait + st.Route.ToDropoffSec
	w := [2]int{max(0, v-etaHalfWindowSec), v + etaHalfWindowSec}
	return &v, &w, EtaEstimated
}
