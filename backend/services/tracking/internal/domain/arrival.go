package domain

import "time"

const (
	arrivalDistanceM = 200.0
	arrivalEtaSec    = 90
)

// ArrivalDue reports whether tracking should publish arrival_detected: a
// picked-up delivery whose courier is within 200 m of the dropoff or has an ETA
// of 90 s or less, and which has not been announced yet.
func ArrivalDue(st State, _ time.Time) bool {
	if st.ArrivalSent || st.Status != StatusPickedUp {
		return false
	}
	if st.LastFix != nil && distanceM(st.LastFix.Pos, st.Dropoff) <= arrivalDistanceM {
		return true
	}
	return st.EtaSeconds != nil && *st.EtaSeconds <= arrivalEtaSec
}
