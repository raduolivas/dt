package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/api"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
)

// With more than 200 findings the order view must use the newest, not the oldest 200.
func TestOrderUsesNewestFindingsBeyondTwoHundred(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	srv := newServer(t, st, api.NewBroadcaster(), nil)
	now := time.Now().UTC()
	evs := []store.Event{
		withAttrs(ev(now.Add(-2*time.Hour), "message_published", "deliveries", "nats.DELIVERIES", "del_1"),
			`{"event_type":"delivery.created","status":"confirmed"}`),
	}
	for i := 0; i < 250; i++ { // old open findings, then the newest is a clear
		evs = append(evs, finding(now.Add(-time.Hour+time.Duration(i)*time.Second), "warning"))
	}
	evs = append(evs, finding(now.Add(-time.Minute), "cleared"))
	require.NoError(t, st.InsertEvents(ctx, evs))

	var r stepsResp
	getJSON(t, srv.URL+"/api/orders/del_1", &r)
	require.Equal(t, "done", states(r)["confirmed"], "the newest courier_stale finding is a clear")
	require.Len(t, r.Detectors, 200)
	require.Equal(t, "cleared", r.Detectors[len(r.Detectors)-1]["outcome"], "newest findings, oldest first")
}
