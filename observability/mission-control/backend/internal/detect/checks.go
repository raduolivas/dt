package detect

import (
	"slices"
	"sort"
	"strings"
	"time"
)

// rules is every detector, run in this order each tick.
var rules = []func(*input) []Finding{
	courierStale, fixesDropped, rejectedAtGateway, stuckStatus, processedNotPublished,
	consumerLagging, snapshotNotDelivered, storm, dependencyFailing, mcBehind,
}

// Statuses in which a courier is carrying the order (and should be sending fixes).
var activeStatuses = map[string]bool{"courier_assigned": true, "at_pickup": true, "picked_up": true, "arriving": true}

func (e ev) str(k string) string { s, _ := e.a[k].(string); return s }

func (e ev) num(k string) (float64, bool) { f, ok := e.a[k].(float64); return f, ok }

// statusOf is the status a delivery.created / delivery.status_changed event puts the delivery in.
func statusOf(e ev) string {
	if e.str("event_type") == "delivery.created" {
		return e.str("status")
	}
	return e.str("to")
}

// hopOf is the component a log line or span event belongs to.
func hopOf(e ev) string {
	if e.Target != "" {
		return e.Target
	}
	return e.Source
}

// within reports whether e was observed inside the last d.
func (in *input) within(e ev, d time.Duration) bool { return !e.ObservedAt.Before(in.now.Add(-d)) }

func ids(es ...ev) []int64 {
	out := make([]int64, 0, len(es))
	for _, e := range es {
		out = append(out, e.ID)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func earliest(es []ev) time.Time {
	t := es[0].OccurredAt
	for _, e := range es {
		if e.OccurredAt.Before(t) {
			t = e.OccurredAt
		}
	}
	return t
}

// group buckets events by key, returning the keys sorted for stable output.
func group(es []ev, key func(ev) string) (map[string][]ev, []string) {
	m := map[string][]ev{}
	for _, e := range es {
		k := key(e)
		m[k] = append(m[k], e)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return m, keys
}

// courierStale: an active delivery with no accepted fix for courier_stale
// (amber) / courier_lost (red), DT's own thresholds. With no fix at all the
// age counts from the status change that made the delivery active.
func courierStale(in *input) []Finding {
	var out []Finding
	for id, st := range in.status {
		if !activeStatuses[statusOf(st)] {
			continue
		}
		base, evidence := st.OccurredAt, st.ID
		if fx, ok := in.fixes[id]; ok {
			base, evidence = fx.OccurredAt, fx.EventID
		}
		age := in.now.Sub(base)
		sev := ""
		switch {
		case age >= in.th.CourierLost:
			sev = SevFailure
		case age >= in.th.CourierStale:
			sev = SevWarning
		default:
			continue
		}
		out = append(out, Finding{Detector: "courier_stale", Hop: "courier", DeliveryID: id, Reason: "no_accepted_fix",
			Severity: sev, Since: base, Evidence: []int64{evidence},
			Attrs: map[string]any{"age_s": int(age.Seconds()), "status": statusOf(st)}})
	}
	return out
}

// fixesDropped: fix_dropped logs (outcome dropped) grouped by delivery and reason.
func fixesDropped(in *input) []Finding {
	var hits []ev
	for _, e := range in.win {
		if e.Kind == "log" && e.Outcome == "dropped" && in.within(e, in.th.Detect.FixesDropped) {
			hits = append(hits, e)
		}
	}
	m, keys := group(hits, func(e ev) string { return hopOf(e) + "|" + e.DeliveryID + "|" + e.Reason })
	var out []Finding
	for _, k := range keys {
		g := m[k]
		sev := SevWarning
		if g[0].Reason == "state_update_failed" {
			sev = SevFailure // DT could not write state at all: an outage, not a rejected fix
		}
		out = append(out, Finding{Detector: "fixes_dropped", Hop: hopOf(g[0]), DeliveryID: g[0].DeliveryID, Reason: g[0].Reason,
			Severity: sev, Since: earliest(g), Evidence: ids(g...), Attrs: map[string]any{"events": len(g)}})
	}
	return out
}

// rejectedAtGateway: 401/403/429 (already outcome rejected) and any other 4xx
// except 404 on the gateway's courier and partner routes.
func rejectedAtGateway(in *input) []Finding {
	var hits []ev
	reasons := map[int64]string{}
	for _, e := range in.win {
		if e.Kind != "request" || !strings.HasPrefix(e.Source, "gateway.") || !in.within(e, in.th.Detect.RejectedAtGateway) {
			continue
		}
		if e.Source != "gateway.courier_api" && e.Source != "gateway.partner_api" {
			continue
		}
		switch st, _ := e.num("http_status"); {
		case e.Outcome == "rejected":
			reasons[e.ID] = e.Reason
		case st >= 400 && st < 500 && st != 404:
			reasons[e.ID] = "client_error"
		default:
			continue
		}
		hits = append(hits, e)
	}
	m, keys := group(hits, func(e ev) string { return e.Source + "|" + reasons[e.ID] })
	var out []Finding
	for _, k := range keys {
		g := m[k]
		out = append(out, Finding{Detector: "rejected_at_gateway", Hop: g[0].Source, Reason: reasons[g[0].ID],
			Severity: SevWarning, Since: earliest(g), Evidence: ids(g...), Attrs: map[string]any{"events": len(g)}})
	}
	return out
}

// stuckStatus: the status has not changed for longer than its per-status limit.
func stuckStatus(in *input) []Finding {
	var out []Finding
	for id, st := range in.status {
		s := statusOf(st)
		limit, ok := in.th.Detect.StuckStatus[s]
		if !ok || in.now.Sub(st.OccurredAt) <= limit {
			continue
		}
		out = append(out, Finding{Detector: "stuck_status", Hop: "deliveries.state_machine", DeliveryID: id, Reason: "stuck_" + s,
			Severity: SevWarning, Since: st.OccurredAt.Add(limit), Evidence: []int64{st.ID},
			Attrs: map[string]any{"status": s, "limit_s": int(limit.Seconds())}})
	}
	return out
}

// processedNotPublished: a deliveries use case that changes state (so writes an
// outbox row) has no "publish" span in its trace after the threshold. Reports
// that change nothing (duplicate / stale / conflict / recorded) are skipped.
func processedNotPublished(in *input) []Finding {
	published := map[string]bool{}
	for _, e := range in.win {
		if strings.HasPrefix(e.str("span_name"), "publish ") && e.TraceID != "" {
			published[e.TraceID] = true
		}
	}
	var hits []ev
	for _, e := range in.win {
		if e.Kind != "state_change" || e.Source != "deliveries.state_machine" || e.Outcome != "ok" || e.TraceID == "" ||
			!strings.HasPrefix(e.str("span_name"), "deliveries.") || e.DeliveryID == "" {
			continue
		}
		if o := e.str("deliveries.outcome"); o != "" && o != "applied" {
			continue
		}
		if in.now.Sub(e.OccurredAt) <= in.th.Detect.ProcessedNotPublished || published[e.TraceID] {
			continue
		}
		hits = append(hits, e)
	}
	m, keys := group(hits, func(e ev) string { return e.DeliveryID })
	var out []Finding
	for _, k := range keys {
		g := m[k]
		out = append(out, Finding{Detector: "processed_not_published", Hop: "deliveries.outbox", DeliveryID: k, Reason: "no_publish",
			Severity: SevWarning, Since: earliest(g), Evidence: ids(g...)})
	}
	return out
}

// consumerLagging: a consumer's pending count is above zero and has not
// shrunk for the whole threshold. Health events are written on change, so the
// value in force at the start of the period is the newest event before it; a
// consumer first seen inside the period is not yet known to be stuck.
func consumerLagging(in *input) []Finding {
	byConsumer, keys := group(in.health, func(e ev) string { return e.Source + "|" + e.str("consumer") })
	cutoff := in.now.Add(-in.th.Detect.ConsumerLagging)
	type lag struct {
		consumers []string
		evidence  []ev
		since     time.Time
	}
	streams := map[string]*lag{}
	var order []string
	for _, k := range keys {
		g := byConsumer[k]
		name := g[0].str("consumer")
		if name == "" || strings.HasPrefix(name, "mc-") {
			continue
		}
		sort.Slice(g, func(i, j int) bool { return g[i].ID < g[j].ID })
		baseIdx := -1
		for i, e := range g {
			if !e.ObservedAt.After(cutoff) {
				baseIdx = i
			}
		}
		if baseIdx < 0 {
			continue
		}
		prev, _ := g[baseIdx].num("pending")
		last := prev
		shrank := false
		for _, e := range g[baseIdx+1:] {
			p, _ := e.num("pending")
			if p < prev {
				shrank = true
			}
			prev, last = p, p
		}
		if first, _ := g[baseIdx].num("pending"); first <= 0 || last <= 0 || shrank {
			continue
		}
		l := streams[g[0].Source]
		if l == nil {
			l = &lag{since: g[baseIdx].ObservedAt}
			streams[g[0].Source] = l
			order = append(order, g[0].Source)
		}
		l.consumers = append(l.consumers, name)
		l.evidence = append(l.evidence, g[baseIdx:]...)
	}
	sort.Strings(order)
	var out []Finding
	for _, hop := range order {
		l := streams[hop]
		out = append(out, Finding{Detector: "consumer_lagging", Hop: hop, Reason: "pending_not_shrinking", Severity: SevWarning,
			Since: l.since, Evidence: ids(l.evidence...), Attrs: map[string]any{"consumer": strings.Join(l.consumers, ",")}})
	}
	return out
}

// snapshotNotDelivered: a snapshot_publish_failed log, or a fan-out to zero
// viewers while a viewer connection that was open at that moment exists for
// the delivery (a viewer span is stored when the connection ends, so its
// start and duration say whether it was connected).
func snapshotNotDelivered(in *input) []Finding {
	var out []Finding
	var failed []ev
	for _, e := range in.win {
		if e.Kind == "log" && in.within(e, in.th.Detect.SnapshotNotDelivered) &&
			(e.Reason == "snapshot_publish_failed" || e.str("body") == "snapshot_publish_failed") {
			failed = append(failed, e)
		}
	}
	m, keys := group(failed, func(e ev) string {
		hop := hopOf(e)
		if hop == "tracking" {
			hop = "tracking.state" // the OTLP log carries the service; Apply publishes the snapshot
		}
		return hop + "|" + e.DeliveryID
	})
	for _, k := range keys {
		g := m[k]
		hop, _, _ := strings.Cut(k, "|")
		out = append(out, Finding{Detector: "snapshot_not_delivered", Hop: hop, DeliveryID: g[0].DeliveryID, Reason: "snapshot_publish_failed",
			Severity: SevWarning, Since: earliest(g), Evidence: ids(g...)})
	}

	byDelivery := map[string][]ev{}
	for _, e := range in.win {
		if e.DeliveryID != "" && e.Target == "tracking.hub" && e.str("span_name") != "tracking.viewer_fanout" {
			if _, ok := e.num("duration_ms"); ok {
				byDelivery[e.DeliveryID] = append(byDelivery[e.DeliveryID], e)
			}
		}
	}
	reported := map[string]bool{}
	for _, f := range in.win {
		if f.str("span_name") != "tracking.viewer_fanout" || f.DeliveryID == "" || !in.within(f, in.th.Detect.SnapshotNotDelivered) {
			continue
		}
		if n, ok := f.num("tracking.viewers"); !ok || n != 0 || reported[f.DeliveryID] {
			continue
		}
		for _, v := range byDelivery[f.DeliveryID] {
			d, _ := v.num("duration_ms")
			if v.OccurredAt.After(f.OccurredAt) || f.OccurredAt.After(v.OccurredAt.Add(time.Duration(d)*time.Millisecond)) {
				continue
			}
			reported[f.DeliveryID] = true
			out = append(out, Finding{Detector: "snapshot_not_delivered", Hop: "tracking.hub", DeliveryID: f.DeliveryID, Reason: "no_viewers_reached",
				Severity: SevWarning, Since: f.OccurredAt, Evidence: ids(v, f)})
			break
		}
	}
	return out
}

var failureOutcomes = map[string]bool{"failed": true, "rejected": true, "dropped": true, "retried": true}

// storm: MORE than N of the same failure reason inside the window on one wire.
func storm(in *input) []Finding {
	var hits []ev
	for _, e := range in.win {
		if !failureOutcomes[e.Outcome] || !in.within(e, in.th.Detect.Storm.Window) {
			continue
		}
		switch e.Kind {
		case "request", "message_consumed", "message_published", "state_change":
		case "log":
			if e.Outcome == "failed" {
				continue // the failing span already counts
			}
		default:
			continue
		}
		hits = append(hits, e)
	}
	reason := func(e ev) string {
		if e.Reason != "" {
			return e.Reason
		}
		return e.Outcome
	}
	wire := func(e ev) string {
		if e.Target == "" || e.Target == e.Source {
			return e.Source
		}
		return e.Source + ">" + e.Target
	}
	m, keys := group(hits, func(e ev) string { return wire(e) + "|" + reason(e) })
	var out []Finding
	for _, k := range keys {
		g := m[k]
		if len(g) <= in.th.Detect.Storm.Count {
			continue
		}
		out = append(out, Finding{Detector: "storm", Hop: wire(g[0]), Reason: reason(g[0]), Severity: SevWarning,
			Since: earliest(g), Evidence: ids(g...), Attrs: map[string]any{"events": len(g)}})
	}
	return out
}

func isDependency(c string) bool { return c == "redis" || c == "osrm" || strings.HasPrefix(c, "pg.") }

// dependencyFailing (ruling R15): at least N failed outcomes in the window on
// a wire into redis / osrm / pg.*, or a failing dependency in a health event.
// OSRM's client in tracking is not traced, so its outage is read from the
// route_fallback log lines instead.
func dependencyFailing(in *input) []Finding {
	w := in.th.Detect.DependencyFailing.Window
	var spans, osrmLogs []ev
	for _, e := range in.win {
		if !in.within(e, w) {
			continue
		}
		switch {
		case e.Kind == "log" && (e.str("body") == "route_fallback" || e.str("body") == "simulation_route_fallback"):
			osrmLogs = append(osrmLogs, e)
		case e.Kind != "log" && e.Outcome == "failed" && isDependency(e.Target):
			spans = append(spans, e)
		}
	}
	var out []Finding
	m, keys := group(spans, func(e ev) string { return e.Target })
	for _, k := range keys {
		if g := m[k]; len(g) >= in.th.Detect.DependencyFailing.Count {
			out = append(out, Finding{Detector: "dependency_failing", Hop: k, Reason: "calls_failing", Severity: SevFailure,
				Since: earliest(g), Evidence: ids(g...), Attrs: map[string]any{"events": len(g)}})
		}
	}
	if len(osrmLogs) >= in.th.Detect.DependencyFailing.Count {
		out = append(out, Finding{Detector: "dependency_failing", Hop: "osrm", Reason: "route_fallback", Severity: SevFailure,
			Since: earliest(osrmLogs), Evidence: ids(osrmLogs...), Attrs: map[string]any{"events": len(osrmLogs)}})
	}
	// The newest health event per (source, dependency) decides.
	newest := map[string]ev{}
	for _, e := range in.health {
		if d := e.str("dependency"); isDependency(d) {
			if k := e.Source + "|" + d; e.ID >= newest[k].ID {
				newest[k] = e
			}
		}
	}
	hk := make([]string, 0, len(newest))
	for k := range newest {
		hk = append(hk, k)
	}
	sort.Strings(hk)
	for _, k := range hk {
		if e := newest[k]; e.Outcome == "failed" {
			out = append(out, Finding{Detector: "dependency_failing", Hop: e.str("dependency"), Reason: "health_failing",
				Severity: SevFailure, Since: e.OccurredAt, Evidence: []int64{e.ID}, Attrs: map[string]any{"reporter": e.Source}})
		}
	}
	return out
}

// mcBehind: Mission Control's own ingest lag above the threshold, or events
// dropped recently. Self-health counters are not mc_event rows, so Evidence is
// empty; the numbers are in Attrs.
func mcBehind(in *input) []Finding {
	var out []Finding
	var lagSrc, dropSrc string
	var lagMS int64
	var dropTime time.Time
	for _, r := range in.self {
		if time.Duration(r.LagMS)*time.Millisecond > in.th.Detect.MCBehind.Lag && r.LagMS > lagMS {
			lagSrc, lagMS = r.Source, r.LagMS
		}
		if t, ok := in.dropAt[r.Source]; ok && r.Drops > 0 && in.now.Sub(t) <= in.th.Detect.MCBehind.DropsHold && t.After(dropTime) {
			dropSrc, dropTime = r.Source, t
		}
	}
	if lagSrc != "" {
		out = append(out, Finding{Detector: "mc_behind", Hop: "mission-control", Reason: "ingest_lag", Severity: SevWarning, Since: in.now,
			Attrs: map[string]any{"source": lagSrc, "lag_ms": lagMS}})
	}
	if dropSrc != "" {
		out = append(out, Finding{Detector: "mc_behind", Hop: "mission-control", Reason: "events_dropped", Severity: SevWarning, Since: dropTime,
			Attrs: map[string]any{"source": dropSrc}})
	}
	return out
}
