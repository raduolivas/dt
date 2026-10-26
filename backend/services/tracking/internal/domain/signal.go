package domain

import "time"

// Signal thresholds (design §3.3).
const (
	staleAfter = 30 * time.Second
	lostAfter  = 5 * time.Minute
)

// SignalAt is the courier signal at now. With a fix it follows the fix age;
// without one, a visible courier is pending for 5 minutes and then lost.
func SignalAt(st State, now time.Time) Signal {
	if st.LastFix != nil {
		age := now.Sub(st.LastFix.T)
		switch {
		case age <= staleAfter:
			return SignalLive
		case age <= lostAfter:
			return SignalStale
		default:
			return SignalLost
		}
	}
	if st.CourierVisibleSince != nil && now.Sub(*st.CourierVisibleSince) > lostAfter {
		return SignalLost
	}
	return SignalPending
}
