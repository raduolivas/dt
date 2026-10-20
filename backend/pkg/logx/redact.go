package logx

import (
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

// Redacted replaces the value of every sensitive attribute.
const Redacted = "[REDACTED]"

// sensitiveSegments mark PII, location or credentials (spec §10). A key is
// sensitive when any of its segments (split on "_", "." and "-", compared
// lowercased) is listed, so "dropoff_lat", "customer_first_name" and
// "x-api-key" are caught as well as "lat", "name" and "api_key".
var sensitiveSegments = map[string]struct{}{
	"lat": {}, "lng": {}, "lon": {}, "latitude": {}, "longitude": {}, "location": {}, "position": {}, "coordinates": {},
	"name": {}, "notes": {}, "email": {}, "phone": {}, "address": {},
	"token": {}, "authorization": {}, "apikey": {}, "key": {}, "password": {}, "secret": {}, "cookie": {},
	"jwt": {}, "bearer": {}, "dsn": {},
}

// sensitiveKeys are whole keys whose segments alone are harmless.
var sensitiveKeys = map[string]struct{}{
	"database_url": {},
}

func isSensitive(key string) bool {
	k := strings.ToLower(key)
	if _, ok := sensitiveKeys[k]; ok {
		return true
	}
	for _, seg := range strings.FieldsFunc(k, func(r rune) bool { return r == '_' || r == '.' || r == '-' }) {
		if _, ok := sensitiveSegments[seg]; ok {
			return true
		}
	}
	return false
}

func redactAttr(a slog.Attr) slog.Attr {
	a.Value = a.Value.Resolve()
	if isSensitive(a.Key) {
		return slog.String(a.Key, Redacted)
	}
	switch a.Value.Kind() {
	case slog.KindGroup:
		group := a.Value.Group()
		out := make([]slog.Attr, len(group))
		for i, ga := range group {
			out[i] = redactAttr(ga)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	case slog.KindAny:
		return slog.Attr{Key: a.Key, Value: redactComposite(a.Value)}
	default:
		return a
	}
}

// redactComposite walks structs, maps and slices (which carry field names
// such as Lat or first_name) through their JSON form and redacts sensitive
// keys at any depth. Errors keep their message; anything that cannot be
// walked is redacted whole.
func redactComposite(v slog.Value) slog.Value {
	x := v.Any()
	if _, ok := x.(error); ok {
		return v
	}
	t := reflect.TypeOf(x)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil {
		return v
	}
	switch t.Kind() {
	case reflect.Struct, reflect.Map, reflect.Slice, reflect.Array, reflect.Interface:
	default:
		return v
	}
	b, err := json.Marshal(x)
	if err != nil {
		return slog.StringValue(Redacted)
	}
	var decoded any
	if err := json.Unmarshal(b, &decoded); err != nil {
		return slog.StringValue(Redacted)
	}
	return slog.AnyValue(redactJSON(decoded))
}

func redactJSON(x any) any {
	switch t := x.(type) {
	case map[string]any:
		for k, val := range t {
			if isSensitive(k) {
				t[k] = Redacted
			} else {
				t[k] = redactJSON(val)
			}
		}
		return t
	case []any:
		for i, val := range t {
			t[i] = redactJSON(val)
		}
		return t
	default:
		return x
	}
}

type redactingHandler struct {
	next slog.Handler
	// redactAll is set once a sensitive group is opened with WithGroup:
	// everything logged under it is redacted.
	redactAll bool
}

// NewRedactingHandler wraps next so that sensitive attributes never reach it,
// including attributes inside groups and those bound with With.
func NewRedactingHandler(next slog.Handler) slog.Handler {
	return &redactingHandler{next: next}
}

func (h *redactingHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h *redactingHandler) redact(a slog.Attr) slog.Attr {
	if h.redactAll {
		return slog.String(a.Key, Redacted)
	}
	return redactAttr(a)
}

func (h *redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.redact(a))
		return true
	})
	// Added last and unredacted: trace ids are not sensitive. (Added to the
	// record, so they sit inside any group opened with WithGroup.)
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		out.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.next.Handle(ctx, out)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	red := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		red[i] = h.redact(a)
	}
	return &redactingHandler{next: h.next.WithAttrs(red), redactAll: h.redactAll}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{next: h.next.WithGroup(name), redactAll: h.redactAll || isSensitive(name)}
}
