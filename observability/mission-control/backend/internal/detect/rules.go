package detect

import (
	"encoding/json"
	"time"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/config"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
)

// ev is a stored event with its attrs parsed once.
type ev struct {
	store.Event
	a map[string]any
}

func wrap(evs []store.Event) []ev {
	out := make([]ev, len(evs))
	for i, e := range evs {
		out[i] = ev{Event: e}
		if len(e.Attrs) > 0 {
			_ = json.Unmarshal(e.Attrs, &out[i].a)
		}
	}
	return out
}

// input is everything the rules look at in one tick.
type input struct {
	now time.Time
	th  config.Thresholds
	// win holds non-detector, non-health events observed inside th.Detect.Window, oldest first.
	win []ev
	// status is the newest delivery.created / delivery.status_changed event per delivery.
	status map[string]ev
	// fixes is the newest accepted fix per delivery (store.LastAcceptedFixes).
	fixes map[string]store.Fix
	// health holds health events inside the window plus the newest per (source, consumer).
	health []ev
	// self is the mc_self rows; dropAt is when each source's drop counter last increased.
	self   []store.SelfRow
	dropAt map[string]time.Time
}
