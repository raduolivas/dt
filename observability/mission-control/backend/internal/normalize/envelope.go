package normalize

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/redact"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
)

// FromEnvelope normalizes one NATS event (the stream's view, kind
// message_published). subject is "<stream lowercase>.<type>"; the prefix is the
// owning service and names the target nats.<STREAM>.
func (n *Normalizer) FromEnvelope(subject string, env *eventsv1.Envelope, observedAt time.Time) (store.Event, error) {
	if env == nil {
		return store.Event{}, errors.New("normalize: nil envelope")
	}
	prefix, _, _ := strings.Cut(subject, ".")
	ev := store.Event{
		ObservedAt: observedAt,
		OccurredAt: observedAt,
		Kind:       "message_published",
		Source:     prefix,
		Target:     "nats." + strings.ToUpper(prefix),
		Outcome:    "ok",
		TenantID:   env.GetTenantId(),
		Env:        environmentName(env.GetEnvironment()),
		DeliveryID: env.GetSubject().GetDeliveryId(),
		Origin:     "live",
		Attrs:      []byte("{}"),
	}
	if env.GetOccurredAt().IsValid() {
		ev.OccurredAt = env.GetOccurredAt().AsTime()
	}
	if env.GetSource() == "simulator" || n.simulated(ev.DeliveryID) {
		ev.Origin = "simulated"
	}
	if env.GetPayload() == nil {
		return store.Event{}, errors.New("normalize: envelope without payload")
	}
	msg, err := env.GetPayload().UnmarshalNew()
	if err != nil {
		return store.Event{}, fmt.Errorf("normalize: unpack payload: %w", err)
	}
	m := msg.ProtoReflect()
	fields, ok := Allowed[string(m.Descriptor().FullName())]
	if !ok {
		ev.Reason = "unknown_payload"
		return ev, nil
	}
	attrs := copyAllowed(m, fields)
	if env.GetType() != "" {
		attrs["event_type"] = env.GetType()
	}
	if m.Descriptor().FullName() == pkgPrefix+"DeliveryCreated" {
		ev.OrderRef, _ = attrs["external_id"].(string)
	}
	// Tracking events carry their own "at"; it is when the thing happened.
	if at := m.Descriptor().Fields().ByName("at"); at != nil && m.Has(at) {
		if ts, ok := m.Get(at).Message().Interface().(interface{ AsTime() time.Time }); ok {
			ev.OccurredAt = ts.AsTime()
		}
	}
	clean, dropped := redact.Attrs(attrs)
	n.redacted(dropped)
	if ev.OrderRef != "" {
		if s, _ := clean["external_id"].(string); s == "" {
			ev.OrderRef = "" // the redactor dropped it
		}
	}
	b, err := json.Marshal(clean)
	if err != nil {
		return store.Event{}, fmt.Errorf("normalize: marshal attrs: %w", err)
	}
	ev.Attrs = b
	return ev, nil
}

func environmentName(e eventsv1.Environment) string {
	switch e {
	case eventsv1.Environment_ENVIRONMENT_TEST:
		return "test"
	case eventsv1.Environment_ENVIRONMENT_LIVE:
		return "live"
	}
	return ""
}
