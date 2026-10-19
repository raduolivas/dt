package logx

import (
	"context"
	"log/slog"
	"strings"
)

// Redacted replaces the value of every sensitive attribute.
const Redacted = "[REDACTED]"

// sensitiveKeys hold PII, location or credentials (spec §10). Compared lowercased.
var sensitiveKeys = map[string]struct{}{
	"lat": {}, "lng": {}, "latitude": {}, "longitude": {}, "location": {}, "position": {},
	"first_name": {}, "name": {}, "display_name": {}, "notes": {},
	"email": {}, "phone": {}, "address": {},
	"token": {}, "authorization": {}, "api_key": {}, "password": {}, "secret": {}, "cookie": {},
}

var sensitiveSuffixes = []string{"_token", "_secret", "_password", "_key"}

func isSensitive(key string) bool {
	k := strings.ToLower(key)
	if _, ok := sensitiveKeys[k]; ok {
		return true
	}
	for _, s := range sensitiveSuffixes {
		if strings.HasSuffix(k, s) {
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
	if a.Value.Kind() == slog.KindGroup {
		group := a.Value.Group()
		out := make([]slog.Attr, len(group))
		for i, ga := range group {
			out[i] = redactAttr(ga)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	}
	return a
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
