package normalize

import (
	"time"

	logspb "go.opentelemetry.io/proto/otlp/logs/v1"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/redact"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
)

// FromLog normalizes one OTLP log record (kind=log). attrs holds the body (as
// "body") and the attributes, after the redact deny-list. Records at ERROR or
// above get outcome failed; others carry no outcome (logs never pulse).
func (n *Normalizer) FromLog(resource map[string]string, rec *logspb.LogRecord) store.Event {
	service := resource["service.name"]
	raw := attrMap(rec.GetAttributes())
	ev := store.Event{
		ObservedAt: n.now(),
		OccurredAt: n.now(),
		Kind:       "log",
		Source:     service,
		Origin:     "live",
		TraceID:    idHex(rec.GetTraceId()),
		SpanID:     idHex(rec.GetSpanId()),
	}
	switch {
	case rec.GetTimeUnixNano() != 0:
		ev.OccurredAt = time.Unix(0, int64(rec.GetTimeUnixNano())).UTC()
	case rec.GetObservedTimeUnixNano() != 0:
		ev.OccurredAt = time.Unix(0, int64(rec.GetObservedTimeUnixNano())).UTC()
	}
	ev.TenantID, ev.Env, ev.DeliveryID = ids(raw)
	if n.simulated(ev.DeliveryID) {
		ev.Origin = "simulated"
	}
	if rec.GetSeverityNumber() >= logspb.SeverityNumber_SEVERITY_NUMBER_ERROR {
		ev.Outcome = "failed"
	}
	if r, ok := raw["reason"].(string); ok && !redact.SecretValue(r) && len(r) <= maxAttrString {
		ev.Reason = r
	}
	attrs := n.spanAttrs(raw)
	if body, ok := anyValue(rec.GetBody()); ok {
		if s, isStr := body.(string); isStr && len(s) > maxAttrString*5 {
			body = s[:maxAttrString*5]
		}
		b, d := redact.Attrs(map[string]any{"body": body})
		n.redacted(d)
		for k, v := range b {
			attrs[k] = v
		}
	}
	if sev := rec.GetSeverityText(); sev != "" {
		attrs["severity"] = sev
	}
	ev.Attrs = mustJSON(attrs)
	return ev
}
