package detect

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
)

// fakeSource is an in-memory Source; Insert keeps the events so Active sees them.
type fakeSource struct {
	win, statuses, health []store.Event
	fixes                 map[string]store.Fix
	self                  []store.SelfRow
	stored                []store.Event
	nextID                int64
}

func (f *fakeSource) Window(context.Context, time.Time) ([]store.Event, error) { return f.win, nil }
func (f *fakeSource) Statuses(context.Context, time.Time) ([]store.Event, error) {
	return f.statuses, nil
}
func (f *fakeSource) LastFixes(_ context.Context, _ []string) (map[string]store.Fix, error) {
	return f.fixes, nil
}
func (f *fakeSource) Health(context.Context, time.Time) ([]store.Event, error) { return f.health, nil }
func (f *fakeSource) Self(context.Context) ([]store.SelfRow, error)            { return f.self, nil }
func (f *fakeSource) Insert(_ context.Context, evs []store.Event) error {
	for i := range evs {
		f.nextID++
		evs[i].ID = 1000 + f.nextID
		f.stored = append(f.stored, evs[i])
	}
	return nil
}

// Active returns the newest stored event per key unless it is a clear.
func (f *fakeSource) Active(context.Context) ([]store.Event, error) {
	newest := map[string]store.Event{}
	for _, e := range f.stored {
		newest[e.Target+"|"+e.DeliveryID+"|"+e.Reason] = e
	}
	var out []store.Event
	for _, e := range newest {
		if e.Outcome != SevCleared {
			out = append(out, e)
		}
	}
	return out, nil
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func setup(t *testing.T) (*fakeSource, *clock, *Engine, *[]Finding) {
	t.Helper()
	src := &fakeSource{fixes: map[string]store.Fix{}}
	ck := &clock{t: t0}
	var got []Finding
	return src, ck, NewEngine(src, defaults(t), ck.now, func(f Finding) { got = append(got, f) }, nil), &got
}

func staleDelivery(src *fakeSource, fixAgo time.Duration) {
	src.statuses = []store.Event{status(1, "d1", "picked_up", t0.Add(-time.Hour)).Event}
	src.fixes["d1"] = store.Fix{EventID: 7, OccurredAt: t0.Add(-fixAgo)}
}

func TestEngineEmitsOnceAndStoresADetectorEvent(t *testing.T) {
	src, ck, eng, got := setup(t)
	staleDelivery(src, 40*time.Second)
	require.NoError(t, eng.Tick(context.Background()))
	require.NoError(t, eng.Tick(context.Background()))
	ck.t = ck.t.Add(5 * time.Second)
	require.NoError(t, eng.Tick(context.Background()))
	require.Len(t, *got, 1, "same (detector, hop, delivery, reason) is emitted once")
	require.Len(t, src.stored, 1)

	e := src.stored[0]
	require.Equal(t, "detector", e.Kind)
	require.Equal(t, "courier", e.Target, "hop is the target the API groups findings by")
	require.Equal(t, "d1", e.DeliveryID)
	require.Equal(t, "no_accepted_fix", e.Reason)
	require.Equal(t, SevWarning, e.Outcome)
	var a map[string]any
	require.NoError(t, json.Unmarshal(e.Attrs, &a))
	require.Equal(t, "courier_stale", a["rule"])
	require.Equal(t, []any{float64(7)}, a["evidence"])
	require.Equal(t, "t1", e.TenantID, "tenant, environment and origin come from the delivery's own events")
	require.Equal(t, "test", e.Env)
	require.Equal(t, "live", e.Origin)
	require.Equal(t, t0, e.ObservedAt)
}

func TestEngineReEmitsOnSeverityChangeAndClearsWhenItStopsHolding(t *testing.T) {
	src, ck, eng, got := setup(t)
	ctx := context.Background()
	staleDelivery(src, 40*time.Second)
	require.NoError(t, eng.Tick(ctx))

	ck.t = t0.Add(6 * time.Minute) // the fix is now 6m40s old: red
	require.NoError(t, eng.Tick(ctx))
	require.Len(t, *got, 2)
	require.Equal(t, SevFailure, (*got)[1].Severity)

	src.fixes["d1"] = store.Fix{EventID: 8, OccurredAt: ck.t.Add(-time.Second)} // a fix arrives
	require.NoError(t, eng.Tick(ctx))
	require.Len(t, *got, 3)
	last := (*got)[2]
	require.Equal(t, SevCleared, last.Severity)
	require.Equal(t, "courier_stale", last.Detector)
	require.Equal(t, []int64{7}, last.Evidence, "a clear keeps the evidence it replaces")
	require.Equal(t, SevCleared, src.stored[2].Outcome)

	require.NoError(t, eng.Tick(ctx))
	require.Len(t, *got, 3, "a cleared finding stays quiet")

	ck.t = ck.t.Add(2 * time.Minute) // stale again: a new finding after the clear
	require.NoError(t, eng.Tick(ctx))
	require.Len(t, *got, 4)
	require.Equal(t, SevWarning, (*got)[3].Severity)
}

func TestEngineRestartDoesNotReEmitAnActiveFinding(t *testing.T) {
	src, ck, eng, got := setup(t)
	ctx := context.Background()
	staleDelivery(src, 40*time.Second)
	require.NoError(t, eng.Tick(ctx))

	var again []Finding
	eng2 := NewEngine(src, defaults(t), ck.now, func(f Finding) { again = append(again, f) }, nil)
	require.NoError(t, eng2.Tick(ctx))
	require.Empty(t, again, "the stored finding is reloaded, not repeated")
	require.Len(t, *got, 1)

	src.fixes["d1"] = store.Fix{EventID: 8, OccurredAt: ck.t}
	require.NoError(t, eng2.Tick(ctx))
	require.Len(t, again, 1)
	require.Equal(t, SevCleared, again[0].Severity, "a finding stored before the restart is still cleared")
}

func TestEngineMCBehindDropsOpenThenCloseAfterTheHold(t *testing.T) {
	src, ck, eng, got := setup(t)
	ctx := context.Background()
	src.self = []store.SelfRow{{Source: "otlp", Drops: 3}}
	require.NoError(t, eng.Tick(ctx))
	require.Len(t, *got, 1, "first sight of a non-zero drop counter counts as a drop now")
	require.Equal(t, "events_dropped", (*got)[0].Reason)

	ck.t = ck.t.Add(2 * time.Minute) // counter unchanged for longer than drops_hold
	require.NoError(t, eng.Tick(ctx))
	require.Len(t, *got, 2)
	require.Equal(t, SevCleared, (*got)[1].Severity)

	src.self = []store.SelfRow{{Source: "otlp", Drops: 5}} // grows again
	require.NoError(t, eng.Tick(ctx))
	require.Len(t, *got, 3)
	require.Equal(t, SevWarning, (*got)[2].Severity)
}
