package normalize

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/redact"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
)

// Span attributes that become columns (and so are not repeated in attrs).
var liftedKeys = map[string]string{
	"dt.tenant_id": "tenant", "tenant_id": "tenant",
	"dt.environment": "env", "environment": "env",
	"dt.delivery_id": "delivery", "delivery_id": "delivery",
}

// skippedKeys are never copied: they can embed values (SQL text, URLs with
// query strings) or are client addresses. Skipped silently and not counted as
// redactions; they are not DT redaction bugs.
var skippedKeys = map[string]bool{
	"db.statement": true, "db.query.text": true, "db.operation.parameter": true,
	"url.full": true, "url.query": true, "http.url": true, "http.target": true,
	// Client and peer addresses are personal data and never stored (R19).
	"client.address": true, "client.port": true, "client.socket.address": true,
	"network.peer.address": true, "network.peer.port": true, "network.local.address": true,
	"net.peer.ip": true, "net.sock.peer.addr": true, "net.sock.peer.port": true, "net.peer.name": true,
	"http.client_ip": true, "http.request.header.x-forwarded-for": true, "http.request.header.x-real-ip": true,
	"http.request.header.forwarded": true,
	// A connection string can embed user:pass@ (M2); see also skipped().
	"db.connection_string": true,
}

// skipped reports a key that is never copied: skippedKeys, or any
// *.connection_string (db.connection_string and its namespace cousins).
func skipped(k string) bool {
	l := strings.ToLower(k)
	return skippedKeys[k] || l == "connection_string" || strings.HasSuffix(l, ".connection_string") ||
		strings.HasSuffix(l, "_connection_string")
}

const maxAttrString = 200

// logSpanEvents are the span events that also become kind=log rows.
var logSpanEvents = map[string]bool{"fix_dropped": true, "snapshot_publish_failed": true, "state_conflict": true}

// FromSpan normalizes one span into its request / message_consumed /
// state_change event, plus one log event per logSpanEvents span event.
//
// Source and target name topology components. Server, Internal and Consumer
// spans happen at Resolve(span): that is the source, and the target too,
// except a Consumer span, whose source is the stream's nats.<STREAM> when the
// topology has the wire nats.<STREAM> -> that part. For Client spans the call
// goes from the caller to the callee: target = Resolve(span), source = the one
// part of the calling service that has a wire to that target, else the service
// cube. A Producer ("publish ...") span goes from the outbox part to
// nats.<STREAM>. Attribution is best effort, not part-perfect.
func (n *Normalizer) FromSpan(resource map[string]string, span *tracepb.Span) []store.Event {
	if span == nil {
		return nil
	}
	service := resource["service.name"]
	raw := attrMap(span.GetAttributes())
	strs := stringAttrs(raw)
	resolved := n.Topology.Resolve(service, span.GetName(), strs)

	start := nanoTime(span.GetStartTimeUnixNano())
	dur := time.Duration(0)
	if span.GetEndTimeUnixNano() > span.GetStartTimeUnixNano() {
		dur = time.Duration(min(span.GetEndTimeUnixNano()-span.GetStartTimeUnixNano(), math.MaxInt64)) //nolint:gosec // G115: clamped to MaxInt64
	}

	ev := store.Event{
		ObservedAt: n.now(),
		OccurredAt: start,
		Kind:       "request",
		Origin:     "live",
		TraceID:    idHex(span.GetTraceId()),
		SpanID:     idHex(span.GetSpanId()),
		Outcome:    "ok",
	}
	ev.TenantID, ev.Env, ev.DeliveryID = ids(raw)

	switch span.GetKind() {
	case tracepb.Span_SPAN_KIND_CONSUMER:
		ev.Kind = "message_consumed"
		ev.Target = resolved
		ev.Source = resolved
		if st := streamOf(strs["messaging.destination.name"], span.GetName()); st != "" && n.hasWire("nats."+st, resolved) {
			ev.Source = "nats." + st
		}
	case tracepb.Span_SPAN_KIND_PRODUCER:
		ev.Source = resolved
		if st := streamOf(strs["messaging.destination.name"], span.GetName()); st != "" && n.hasComp("nats."+st) {
			ev.Target = "nats." + st
		}
	case tracepb.Span_SPAN_KIND_CLIENT:
		ev.Target = resolved
		if ev.Target == "" && (strings.Contains(strs["server.address"], "osrm") || strings.Contains(strs["url.full"], "osrm")) {
			ev.Target = "osrm"
		}
		if ev.Target == service {
			ev.Target = "" // a client span that resolved to its own cube is not a wire
		}
		ev.Source = n.callerFor(service, ev.Target)
	default:
		ev.Source = resolved
		ev.Target = resolved
	}
	if span.GetName() == "tracking.apply" || strings.HasPrefix(span.GetName(), "deliveries.") {
		ev.Kind = "state_change"
	}
	if ev.Source == "" {
		ev.Source = service
	}
	if ev.Source == "tracking.simulator" || n.simulated(ev.DeliveryID) {
		ev.Origin = "simulated"
	}

	status := httpStatus(raw)
	switch {
	case span.GetStatus().GetCode() == tracepb.Status_STATUS_CODE_ERROR:
		ev.Outcome = "failed"
	case status == 401 || status == 403:
		ev.Outcome, ev.Reason = "rejected", "unauthorized"
	case status == 429:
		ev.Outcome, ev.Reason = "rejected", "rate_limited"
	case n.Thresholds.Slow > 0 && dur > n.Thresholds.Slow:
		ev.Outcome = "slow"
	}

	attrs := n.spanAttrs(raw)
	attrs["span_name"] = span.GetName()
	attrs["service"] = service
	attrs["duration_ms"] = float64(dur) / float64(time.Millisecond)
	if status != 0 {
		attrs["http_status"] = status
	}
	ev.Attrs = mustJSON(attrs)

	out := []store.Event{ev}
	for _, se := range span.GetEvents() {
		if !logSpanEvents[se.GetName()] {
			continue
		}
		out = append(out, n.spanLog(ev, se))
	}
	return out
}

func (n *Normalizer) spanLog(parent store.Event, se *tracepb.Span_Event) store.Event {
	raw := attrMap(se.GetAttributes())
	e := parent
	e.Kind = "log"
	e.Target = parent.Source
	e.OccurredAt = nanoTime(se.GetTimeUnixNano())
	if se.GetTimeUnixNano() == 0 {
		e.OccurredAt = parent.OccurredAt
	}
	e.Reason = se.GetName()
	switch se.GetName() {
	case "fix_dropped":
		e.Outcome = "dropped"
		if r, ok := raw["reason"].(string); ok && r != "" && !redact.SecretValue(r) {
			e.Reason = r
		}
	case "state_conflict":
		e.Outcome = "retried"
	default:
		e.Outcome = "failed"
	}
	if t, env, d := ids(raw); t != "" || env != "" || d != "" {
		if t != "" {
			e.TenantID = t
		}
		if env != "" {
			e.Env = env
		}
		if d != "" {
			e.DeliveryID = d
			if n.simulated(d) {
				e.Origin = "simulated"
			}
		}
	}
	attrs := n.spanAttrs(raw)
	attrs["event"] = se.GetName()
	e.Attrs = mustJSON(attrs)
	return e
}

// spanAttrs copies attributes through the skip list, the lifted-id filter, the
// length cap and the redact deny-list.
//
// Redaction accounting (R20): OnRedacted counts only deny-list hits, i.e. keys
// or dt_sk_/dt_pk_ values that DT should already have stripped. Expected skips
// (skippedKeys: SQL text, URLs, client/peer addresses; bytes values dropped by
// anyValue) are silent, so the "DT redaction bug" signal is not noise.
//
// Like DT's collector (R18), keys in the standard OTel namespaces are not
// segment-tested (messaging.destination.name, server.address, ...); only
// credential-looking keys inside them are still dropped and counted.
func (n *Normalizer) spanAttrs(raw map[string]any) map[string]any {
	out := map[string]any{}
	dropped := 0
	segTested := map[string]any{}
	for k, v := range raw {
		if skipped(k) || liftedKeys[k] != "" {
			continue
		}
		if s, ok := v.(string); ok && len(s) > maxAttrString {
			v = s[:maxAttrString]
		}
		if !standardNamespace(k) {
			segTested[k] = v
			continue
		}
		if credentialKey(k) {
			dropped++
			continue
		}
		// Standard key: not segment-tested, but its value is still checked.
		cleaned, d := redact.Attrs(map[string]any{"v": v})
		dropped += d
		if cv, ok := cleaned["v"]; ok {
			out[k] = cv
		}
	}
	rest, d := redact.Attrs(segTested)
	dropped += d
	for k, v := range rest {
		out[k] = v
	}
	n.redacted(dropped)
	return out
}

var standardPrefixes = []string{"messaging.", "server.", "client.", "network.", "db.", "rpc.", "http.", "url.", "service."}

func standardNamespace(k string) bool {
	k = strings.ToLower(k)
	for _, p := range standardPrefixes {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

var credentialSegments = map[string]bool{
	"authorization": true, "cookie": true, "token": true, "key": true, "apikey": true,
	"secret": true, "password": true, "jwt": true, "bearer": true, "dsn": true,
}

func credentialKey(k string) bool {
	for _, seg := range strings.FieldsFunc(strings.ToLower(k), func(r rune) bool { return r == '_' || r == '.' || r == '-' }) {
		if credentialSegments[seg] {
			return true
		}
	}
	return false
}

// callerFor picks the calling component of a client span: the single part of
// service with a wire to target, else the service cube.
func (n *Normalizer) callerFor(service, target string) string {
	if target == "" {
		return service
	}
	var found []string
	for _, w := range n.Topology.Wires {
		if w.To == target && (w.From == service || strings.HasPrefix(w.From, service+".")) {
			found = append(found, w.From)
		}
	}
	if len(found) == 1 {
		return found[0]
	}
	return service
}

// streamOf returns the upper-case stream of a NATS subject
// ("deliveries.delivery.created" -> "DELIVERIES"), from the messaging
// attribute or a "publish <subject>" / "consume <subject>" span name.
func streamOf(dest, spanName string) string {
	s := dest
	if s == "" {
		if _, after, ok := strings.Cut(spanName, " "); ok {
			s = after
		}
	}
	pre, _, _ := strings.Cut(s, ".")
	return strings.ToUpper(pre)
}

// ids reads tenant, environment and delivery id; the dt.* key wins over the
// bare one, so the choice never depends on map order.
func ids(raw map[string]any) (tenant, env, delivery string) {
	get := func(keys ...string) string {
		for _, k := range keys {
			if s, ok := raw[k].(string); ok && s != "" {
				return s
			}
		}
		return ""
	}
	return get("dt.tenant_id", "tenant_id"), get("dt.environment", "environment"), get("dt.delivery_id", "delivery_id")
}

func httpStatus(raw map[string]any) int {
	for _, k := range []string{"http.response.status_code", "http.status_code"} {
		switch v := raw[k].(type) {
		case int64:
			return int(v)
		case string:
			if i, err := strconv.Atoi(v); err == nil {
				return i
			}
		}
	}
	return 0
}

func idHex(b []byte) string {
	for _, x := range b {
		if x != 0 {
			return hex.EncodeToString(b)
		}
	}
	return ""
}

func mustJSON(m map[string]any) []byte {
	b, err := json.Marshal(m)
	if err != nil {
		return []byte("{}")
	}
	return b
}

// attrMap flattens OTLP key values to plain Go values.
func attrMap(kvs []*commonpb.KeyValue) map[string]any {
	out := make(map[string]any, len(kvs))
	for _, kv := range kvs {
		if v, ok := anyValue(kv.GetValue()); ok {
			out[kv.GetKey()] = v
		}
	}
	return out
}

func anyValue(v *commonpb.AnyValue) (any, bool) {
	switch x := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return x.StringValue, true
	case *commonpb.AnyValue_IntValue:
		return x.IntValue, true
	case *commonpb.AnyValue_DoubleValue:
		return x.DoubleValue, true
	case *commonpb.AnyValue_BoolValue:
		return x.BoolValue, true
	case *commonpb.AnyValue_ArrayValue:
		arr := []any{}
		for _, e := range x.ArrayValue.GetValues() {
			if ev, ok := anyValue(e); ok {
				arr = append(arr, ev)
			}
		}
		return arr, true
	case *commonpb.AnyValue_KvlistValue:
		return attrMap(x.KvlistValue.GetValues()), true
	}
	return nil, false // bytes and empty values are not copied
}

// stringAttrs renders scalar attributes as strings for Topology.Resolve.
func stringAttrs(raw map[string]any) map[string]string {
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		switch x := v.(type) {
		case string:
			out[k] = x
		case int64, float64, bool:
			out[k] = fmt.Sprint(x)
		}
	}
	return out
}

func (n *Normalizer) hasWire(from, to string) bool {
	for _, w := range n.Topology.Wires {
		if w.From == from && w.To == to {
			return true
		}
	}
	return false
}

func (n *Normalizer) hasComp(id string) bool {
	for _, c := range n.Topology.Components {
		if c.ID == id {
			return true
		}
	}
	return false
}

// nanoTime converts OTLP unix nanoseconds to a UTC time; values past the int64
// range (year 2262) clamp instead of wrapping to a negative time.
func nanoTime(ns uint64) time.Time {
	if ns > math.MaxInt64 {
		ns = math.MaxInt64
	}
	return time.Unix(0, int64(ns)).UTC() //nolint:gosec // G115: clamped to MaxInt64 above
}
