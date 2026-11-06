package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/topology"
)

// Health states reported per field. "not_probed" means Mission Control has no
// way to ask (deliveries and accounts serve health over mTLS only, ruling
// R22); it is never shown as unhealthy. "unknown" means probed but nothing
// observed yet.
const (
	hOK        = "ok"
	hFailed    = "failed"
	hNotProbed = "not_probed"
	hUnknown   = "unknown"
)

// Component states in /api/topology.
const (
	stNotBuilt       = "not_built"
	stNotInstrumeted = "not_instrumented"
	stUnreachable    = "unreachable"
	stWarning        = "warning"
	stActive         = "active"
	stIdle           = "idle"
	stStale          = "stale"

	activeWithin   = 30 * time.Second
	idleWithin     = 5 * time.Minute
	activityWindow = time.Hour
)

// probedServices are the DT services Mission Control probes (/livez, /readyz);
// keep in step with healthTargets in cmd/mc.
var probedServices = map[string]bool{"gateway": true, "tracking": true, "webhooks": true}

// serviceOf is the DT service a component id belongs to ("tracking.state" ->
// "tracking"), or "" for data stores, NATS streams and outside parties.
func serviceOf(id string) string {
	switch id {
	case "gateway", "deliveries", "tracking", "accounts", "webhooks":
		return id
	}
	if i := strings.IndexByte(id, '.'); i > 0 {
		switch id[:i] {
		case "gateway", "deliveries", "tracking", "accounts", "webhooks":
			return id[:i]
		}
	}
	return ""
}

func (a *API) component(id string) (topology.Component, bool) {
	for _, c := range a.topo.Components {
		if c.ID == id {
			return c, true
		}
	}
	return topology.Component{}, false
}

type healthField struct {
	State  string     `json:"state"`
	Reason string     `json:"reason,omitempty"`
	At     *time.Time `json:"at,omitempty"`
}

func attrBool(e store.Event, key string) (bool, bool) {
	var m map[string]any
	if json.Unmarshal(e.Attrs, &m) != nil {
		return false, false
	}
	v, ok := m[key].(bool)
	return v, ok
}

func attrString(e store.Event, key string) string {
	var m map[string]any
	if json.Unmarshal(e.Attrs, &m) != nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

// liveReady derives the liveness and readiness fields of a component from the
// latest stored health events (latest is keyed by source).
func liveReady(id string, latest map[string]store.Event) (live, ready healthField) {
	svc := serviceOf(id)
	switch {
	case svc != "" && !probedServices[svc]:
		return healthField{State: hNotProbed, Reason: "health is mTLS-only"}, healthField{State: hNotProbed, Reason: "health is mTLS-only"}
	case svc != "":
		e, ok := latest[svc]
		if !ok {
			return healthField{State: hUnknown}, healthField{State: hUnknown}
		}
		l, lok := attrBool(e, "live")
		r, rok := attrBool(e, "ready")
		at := e.ObservedAt
		field := func(v, known bool) healthField {
			switch {
			case !known:
				return healthField{State: hUnknown, At: &at}
			case v:
				return healthField{State: hOK, At: &at}
			}
			return healthField{State: hFailed, Reason: e.Reason, At: &at}
		}
		return field(l, lok), field(r, rok)
	case id == "nats":
		if e, ok := latest["nats"]; ok { // the monitor endpoint answering
			at := e.ObservedAt
			if e.Outcome == "failed" {
				return healthField{State: hFailed, Reason: e.Reason, At: &at}, healthField{State: hNotProbed}
			}
			return healthField{State: hOK, At: &at}, healthField{State: hNotProbed}
		}
		return healthField{State: hUnknown}, healthField{State: hNotProbed}
	}
	return healthField{State: hNotProbed}, healthField{State: hNotProbed}
}

func healthSources() []string {
	return []string{"gateway", "tracking", "webhooks", "nats"}
}

// ---- topology -------------------------------------------------------------

type compState struct {
	topology.Component
	State    string     `json:"state"`
	LastSeen *time.Time `json:"lastSeen"`
}

func (a *API) getTopology(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := time.Now()
	act, err := a.st.Activity(ctx, now.Add(-activityWindow))
	if err != nil {
		a.fail(w, "topology activity", err)
		return
	}
	latest, err := a.st.LatestHealth(ctx, healthSources())
	if err != nil {
		a.fail(w, "topology health", err)
		return
	}
	warns, err := a.st.RecentDetectors(ctx, time.Time{}, 200)
	if err != nil {
		a.fail(w, "topology detectors", err)
		return
	}
	warned := map[string]bool{}
	for _, d := range warns {
		warned[d.Target] = true
	}
	comps := make([]compState, 0, len(a.topo.Components))
	for _, c := range a.topo.Components {
		// A cube is as recently seen as its newest working part.
		var seen time.Time
		for id, t := range act {
			if (id == c.ID || strings.HasPrefix(id, c.ID+".")) && t.After(seen) {
				seen = t
			}
		}
		cs := compState{Component: c}
		if !seen.IsZero() {
			s := seen
			cs.LastSeen = &s
		}
		live, ready := liveReady(c.ID, latest)
		switch {
		case c.Status == "not_built":
			cs.State = stNotBuilt
		case !c.Instrumented:
			cs.State = stNotInstrumeted
		case live.State == hFailed || ready.State == hFailed:
			cs.State = stUnreachable
		case warned[c.ID]:
			cs.State = stWarning
		case seen.IsZero():
			cs.State = stStale
		case now.Sub(seen) <= activeWithin:
			cs.State = stActive
		case now.Sub(seen) <= idleWithin:
			cs.State = stIdle
		default:
			cs.State = stStale
		}
		comps = append(comps, cs)
	}
	writeJSON(w, http.StatusOK, map[string]any{"components": comps, "wires": a.topo.Wires})
}

// ---- orders ---------------------------------------------------------------

func (a *API) searchOrders(w http.ResponseWriter, r *http.Request) {
	hits, err := a.st.SearchOrder(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		a.fail(w, "search orders", err)
		return
	}
	writeJSON(w, http.StatusOK, toOrderDTOs(hits))
}

// The status path of a delivery (spec §6.1); cancelled and failed end it.
var mainSteps = []string{"confirmed", "courier_assigned", "at_pickup", "picked_up", "arriving", "delivered"}

const (
	typeCreated    = "delivery.created"
	typeStatus     = "delivery.status_changed"
	typeSignalLost = "tracking.courier_signal_lost"
	typeRestored   = "tracking.courier_signal_restored"
)

type step struct {
	Status string     `json:"status"`
	At     *time.Time `json:"at"`
	State  string     `json:"state"` // done | warning | pending
}

type orderState struct {
	steps    []step
	terminal bool
}

// loadOrderState derives the status steps from the delivery's stored events:
// DeliveryCreated (attr status) and DeliveryStatusChanged (attr to). A step is
// done only with such evidence. The newest reached step of the main path turns
// into a warning while the courier signal is lost (tracking) or an open stored
// courier_stale finding is newer than the last accepted fix (ruling R14,
// amended), unless the delivery already ended.
func (a *API) loadOrderState(r *http.Request, deliveryID string, detectors []store.Event) (orderState, error) {
	evs, err := a.st.DeliveryEventsByType(r.Context(), deliveryID, []string{typeCreated, typeStatus, typeSignalLost, typeRestored})
	if err != nil {
		return orderState{}, err
	}
	reached := map[string]time.Time{}
	var lostAt, restoredAt time.Time
	for _, e := range evs {
		var s string
		switch attrString(e, "event_type") {
		case typeCreated:
			s = attrString(e, "status")
		case typeStatus:
			s = attrString(e, "to")
		case typeSignalLost:
			if e.OccurredAt.After(lostAt) {
				lostAt = e.OccurredAt
			}
		case typeRestored:
			if e.OccurredAt.After(restoredAt) {
				restoredAt = e.OccurredAt
			}
		}
		if s == "" {
			continue
		}
		if t, ok := reached[s]; !ok || e.OccurredAt.Before(t) {
			reached[s] = e.OccurredAt
		}
	}
	// Warning only while the newest of lost/restored is a loss, or while the
	// newest courier_stale finding is still open (not "cleared") and newer than
	// the delivery's last accepted fix (ruling R14 amended; T6 carry). The
	// accepted-fix test (store.LastAcceptedFixes) drops the warning the moment a
	// fix lands, before the detector's next tick stores the clear.
	warn := !lostAt.IsZero() && lostAt.After(restoredAt)
	var stale *store.Event
	for i, d := range detectors { // oldest first: the last match is the newest finding
		if d.Reason == "courier_stale" || attrString(d, "rule") == "courier_stale" {
			stale = &detectors[i]
		}
	}
	if stale != nil && stale.Outcome != "cleared" {
		fix, ok, err := a.st.LastAcceptedFix(r.Context(), deliveryID)
		if err != nil {
			return orderState{}, err
		}
		if !ok || stale.OccurredAt.After(fix.OccurredAt) {
			warn = true
		}
	}
	var st orderState
	newest := -1
	for i, name := range mainSteps {
		sp := step{Status: name, State: "pending"}
		if t, ok := reached[name]; ok {
			tt := t
			sp.At, sp.State = &tt, "done"
			newest = i
		}
		st.steps = append(st.steps, sp)
	}
	for _, name := range []string{"cancelled", "failed"} {
		if t, ok := reached[name]; ok {
			tt := t
			st.steps = append(st.steps, step{Status: name, At: &tt, State: "done"})
			st.terminal = true
		}
	}
	if _, ok := reached["delivered"]; ok {
		st.terminal = true
	}
	if warn && newest >= 0 && !st.terminal {
		st.steps[newest].State = "warning"
	}
	return st, nil
}

func (a *API) getOrder(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("delivery_id")
	hits, err := a.st.SearchOrder(ctx, id)
	if err != nil {
		a.fail(w, "order lookup", err)
		return
	}
	var order orderDTO
	switch {
	case len(hits) > 0 && hits[0].DeliveryID == id:
		order = toOrderDTOs(hits[:1])[0]
	default:
		_, _, ok, err := a.st.DeliverySpan(ctx, id)
		if err != nil {
			a.fail(w, "order span", err)
			return
		}
		if !ok {
			writeErr(w, http.StatusNotFound, "order not found")
			return
		}
		order = orderDTO{DeliveryID: id} // events exist, the index row was pruned or never written
	}
	detectors, err := a.st.DeliveryDetectors(ctx, id)
	if err != nil {
		a.fail(w, "order detectors", err)
		return
	}
	state, err := a.loadOrderState(r, id, detectors)
	if err != nil {
		a.fail(w, "order status", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"order": order, "statusSteps": state.steps, "detectors": toEventDTOs(detectors)})
}

type gap struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	Kind string    `json:"kind"` // mc_down | no_trace
}

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func intParam(r *http.Request, name string) (int64, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, errors.New("invalid " + name)
	}
	return n, nil
}

// getTimeline pages the order's events by insert id. Ordering is by id, so a
// trace sibling stored before the cursor can be missed by a client that
// resumes from a later cursor: a client re-fetches from after=0 when it opens
// an order.
//
// gaps: mc_down = Mission Control was not running inside the order's span
// (first event to last event, or to now while the order is open); no_trace =
// one band from the order's first event to Mission Control's first recorded
// start when the order began earlier (no trace data for that period).
func (a *API) getTimeline(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("delivery_id")
	after, err := intParam(r, "after")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	limit, err := intParam(r, "limit")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	evs, next, err := a.st.Timeline(ctx, id, after, int(limit))
	if err != nil {
		a.fail(w, "timeline", err)
		return
	}
	gaps := []gap{}
	first, last, ok, err := a.st.DeliverySpan(ctx, id)
	if err != nil {
		a.fail(w, "timeline span", err)
		return
	}
	if ok {
		state, err := a.loadOrderState(r, id, nil)
		if err != nil {
			a.fail(w, "timeline status", err)
			return
		}
		to := last
		if !state.terminal {
			to = time.Now()
		}
		downs, err := a.st.Downtime(ctx, first, to)
		if err != nil {
			a.fail(w, "timeline downtime", err)
			return
		}
		for _, d := range downs {
			gaps = append(gaps, gap{From: d.Start, To: d.End, Kind: "mc_down"})
		}
		// Before Mission Control's first start only NATS history exists (OTLP
		// is not recoverable): one band, not a per-event heuristic (R23).
		fs, haveFirst, err := a.st.FirstStart(ctx)
		if err != nil {
			a.fail(w, "timeline first start", err)
			return
		}
		if haveFirst && first.Before(fs) {
			gaps = append(gaps, gap{From: first, To: earlier(fs, last), Kind: "no_trace"})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": toEventDTOs(evs), "next": next, "gaps": gaps})
}

// ---- components -----------------------------------------------------------

func (a *API) getComponent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	comp, ok := a.component(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown component")
		return
	}
	deliveryID := r.URL.Query().Get("delivery_id")
	recent, err := a.st.ComponentEvents(ctx, id, deliveryID, 50)
	if err != nil {
		a.fail(w, "component events", err)
		return
	}
	latest, err := a.st.LatestHealth(ctx, healthSources())
	if err != nil {
		a.fail(w, "component health", err)
		return
	}
	warns, err := a.st.RecentDetectors(ctx, time.Time{}, 200)
	if err != nil {
		a.fail(w, "component detectors", err)
		return
	}
	live, ready := liveReady(id, latest)

	// dependencies: the newest outcome toward each target in the recent events
	type dep struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	deps := []dep{}
	seen := map[string]bool{}
	for _, e := range recent { // newest first
		if e.Source != id || e.Target == "" || seen[e.Target] {
			continue
		}
		seen[e.Target] = true
		st := hOK
		if e.Outcome == "failed" {
			st = hFailed
		}
		deps = append(deps, dep{e.Target, st})
	}
	findings := 0
	for _, d := range warns {
		if d.Target == id {
			findings++
		}
	}
	business := healthField{State: hUnknown, Reason: "no detector findings"}
	if findings > 0 {
		business = healthField{State: stWarning, Reason: strconv.Itoa(findings) + " recent findings"}
	}
	links := a.links.Links(id, "")
	if links == nil {
		links = []Link{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"component": comp,
		"health":    map[string]any{"liveness": live, "readiness": ready, "dependencies": deps, "business": business},
		"recent":    toEventDTOs(recent),
		"links":     links,
	})
}

func (a *API) getComponentLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := a.component(id); !ok {
		writeErr(w, http.StatusNotFound, "unknown component")
		return
	}
	level := strings.ToLower(r.URL.Query().Get("level"))
	if !store.ValidLevel(level) {
		writeErr(w, http.StatusBadRequest, "invalid level")
		return
	}
	after, err := intParam(r, "after")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	limit, err := intParam(r, "limit")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	source := serviceOf(id) // logs carry the service name
	if source == "" {
		source = id
	}
	lim := int(limit)
	if lim <= 0 {
		lim = store.DefaultTimelineLimit
	}
	logs, err := a.st.ComponentLogs(r.Context(), source, r.URL.Query().Get("delivery_id"), level, after, lim)
	if err != nil {
		a.fail(w, "component logs", err)
		return
	}
	var next int64
	if len(logs) >= min(lim, store.MaxTimelineLimit) {
		next = logs[len(logs)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": toEventDTOs(logs), "next": next})
}

// ---- events, system, self --------------------------------------------------

func (a *API) getEvent(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	e, err := a.st.EventByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "event not found")
		return
	}
	if err != nil {
		a.fail(w, "event", err)
		return
	}
	logs, err := a.st.LogsForSpan(r.Context(), e.TraceID, e.SpanID)
	if err != nil {
		a.fail(w, "event logs", err)
		return
	}
	links := a.links.Links("", e.TraceID)
	if links == nil {
		links = []Link{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"event": toEventDTO(e), "logs": toEventDTOs(logs), "links": links})
}

func (a *API) getSystem(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	traffic, err := a.traffic.Traffic(ctx)
	if err != nil {
		a.fail(w, "system traffic", err)
		return
	}
	if traffic == nil {
		traffic = []WireTraffic{}
	}
	orders, err := a.st.RecentOrders(ctx, 20)
	if err != nil {
		a.fail(w, "system orders", err)
		return
	}
	warns, err := a.st.RecentDetectors(ctx, time.Time{}, 50)
	if err != nil {
		a.fail(w, "system warnings", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"traffic": traffic, "recentOrders": toOrderDTOs(orders),
		"warnings": toEventDTOs(warns)})
}

func (a *API) getSelf(w http.ResponseWriter, r *http.Request) {
	rows, err := a.st.SelfRows(r.Context())
	if err != nil {
		a.fail(w, "self", err)
		return
	}
	type row struct {
		Source        string    `json:"source"`
		LastSeen      time.Time `json:"lastSeen"`
		LagMS         int64     `json:"lagMs"`
		Drops         int64     `json:"drops"`
		Redacted      int64     `json:"redacted"`
		StorageErrors int64     `json:"storageErrors"`
		// MetricsSourceUnavailable is true while the configured metrics source fails and builtin is used.
		MetricsSourceUnavailable bool `json:"metricsSourceUnavailable"`
	}
	out := make([]row, len(rows))
	for i, x := range rows {
		out[i] = row{x.Source, x.LastSeen, x.LagMS, x.Drops, x.Redacted, x.StorageErrors, x.Unavailable}
	}
	writeJSON(w, http.StatusOK, out)
}
