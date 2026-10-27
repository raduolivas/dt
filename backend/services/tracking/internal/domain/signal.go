package domain

import "time"

// Signal thresholds (design §3.3).
const (
	// SignalStaleAfter is the fix age past which the signal is stale.
	SignalStaleAfter = 30 * time.Second
	// SignalLostAfter is the fix age (or, without a fix, the time visible)
	// past which the signal is lost.
	SignalLostAfter = 5 * time.Minute

	staleAfter = SignalStaleAfter
	lostAfter  = SignalLostAfter
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

// ViewerSignal is the signal the customer sees at now: SignalAt for an
// assigned courier who is visible, and "" when no courier is shown.
func ViewerSignal(st State, now time.Time) Signal {
	if st.Courier == nil || !CourierVisible(st.Model, st.Status) {
		return ""
	}
	return SignalAt(st, now)
}

// SweepActive reports whether the delivery's signal can change with time
// alone (a visible courier on a delivery that is not final), and the time
// its signal age counts from: the last fix, else when the courier became
// visible.
func SweepActive(st State) (since time.Time, active bool) {
	if st.Courier == nil || st.Status.IsFinal() || !CourierVisible(st.Model, st.Status) {
		return time.Time{}, false
	}
	switch {
	case st.LastFix != nil:
		return st.LastFix.T, true
	case st.CourierVisibleSince != nil:
		return *st.CourierVisibleSince, true
	default:
		return time.Time{}, true
	}
}
