package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/api"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
)

func finding(at time.Time, outcome string) store.Event {
	e := ev(at, "detector", "mc", "courier", "del_1")
	e.Outcome, e.Reason = outcome, "no_accepted_fix"
	e.Attrs = []byte(`{"rule":"courier_stale","severity":"` + outcome + `","evidence":[]}`)
	return e
}

// An accepted fix is a tracking.apply in the trace of a tracking.ingest that accepted fixes.
func acceptedFix(at time.Time, trace string) []store.Event {
	ing := ev(at, "request", "tracking.ingest", "tracking.ingest", "")
	ing.TraceID, ing.Attrs = trace, []byte(`{"span_name":"tracking.ingest","tracking.fixes.accepted":1}`)
	app := ev(at, "state_change", "tracking.state", "tracking.state", "del_1")
	app.TraceID, app.Attrs = trace, []byte(`{"span_name":"tracking.apply"}`)
	return []store.Event{ing, app}
}

func TestStatusStepWarningFollowsStaleFindingAndAcceptedFix(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	srv := newServer(t, st, api.NewBroadcaster(), nil)
	now := time.Now().UTC()
	require.NoError(t, st.InsertEvents(ctx, []store.Event{
		withAttrs(ev(now.Add(-10*time.Minute), "message_published", "deliveries", "nats.DELIVERIES", "del_1"),
			`{"event_type":"delivery.created","status":"confirmed"}`),
		finding(now.Add(-4*time.Minute), "warning"),
	}))
	var r stepsResp
	getJSON(t, srv.URL+"/api/orders/del_1", &r)
	require.Equal(t, "warning", states(r)["confirmed"], "open finding, no fix yet")

	// a fix older than the finding does not clear it
	require.NoError(t, st.InsertEvents(ctx, acceptedFix(now.Add(-5*time.Minute), "tr_old")))
	r = stepsResp{}
	getJSON(t, srv.URL+"/api/orders/del_1", &r)
	require.Equal(t, "warning", states(r)["confirmed"])

	// an accepted fix newer than the finding drops the warning before the detector's next tick
	require.NoError(t, st.InsertEvents(ctx, acceptedFix(now.Add(-3*time.Minute), "tr_new")))
	r = stepsResp{}
	getJSON(t, srv.URL+"/api/orders/del_1", &r)
	require.Equal(t, "done", states(r)["confirmed"])

	// stale again: a new finding after the fix
	require.NoError(t, st.InsertEvents(ctx, []store.Event{finding(now.Add(-2*time.Minute), "failure")}))
	r = stepsResp{}
	getJSON(t, srv.URL+"/api/orders/del_1", &r)
	require.Equal(t, "warning", states(r)["confirmed"])

	// a cleared finding is no warning
	require.NoError(t, st.InsertEvents(ctx, []store.Event{finding(now.Add(-time.Minute), "cleared")}))
	r = stepsResp{}
	getJSON(t, srv.URL+"/api/orders/del_1", &r)
	require.Equal(t, "done", states(r)["confirmed"])
}

func TestDroppedFixIsNotAnAcceptedFix(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	now := time.Now().UTC()
	evs := acceptedFix(now.Add(-time.Minute), "tr1")
	drop := ev(now.Add(-time.Minute), "log", "tracking.ingest", "tracking.ingest", "del_1")
	drop.TraceID, drop.Outcome, drop.Reason = "tr1", "dropped", "delivery_reassigned"
	require.NoError(t, st.InsertEvents(ctx, append(evs, drop)))
	_, ok, err := st.LastAcceptedFix(ctx, "del_1")
	require.NoError(t, err)
	require.False(t, ok, "the delivery dropped that fix")
	require.NoError(t, st.InsertEvents(ctx, acceptedFix(now, "tr2")))
	f, ok, err := st.LastAcceptedFix(ctx, "del_1")
	require.NoError(t, err)
	require.True(t, ok)
	require.WithinDuration(t, now, f.OccurredAt, time.Second)
}
