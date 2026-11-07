// Package normalize turns DT's NATS envelopes, OTLP spans and OTLP logs into
// store.Event rows. It is the privacy boundary of Mission Control: payload
// fields are copied by allow-list only, span and log attributes go through the
// redact deny-list, and every drop is reported through Normalizer.OnRedacted.
package normalize

import (
	"time"

	"google.golang.org/protobuf/reflect/protoreflect"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/config"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/topology"
)

// SimRegistry tells whether a delivery was created by Mission Control's
// simulator. A nil registry means none are simulated.
type SimRegistry interface{ IsSimulated(deliveryID string) bool }

// Normalizer converts DT telemetry into store events.
type Normalizer struct {
	Topology    topology.Topology
	SimRegistry SimRegistry
	Thresholds  config.Thresholds
	// OnRedacted is called with the number of attributes dropped by the
	// deny-list (never with 0). Task 4 wires it to the self-health counter.
	OnRedacted func(n int)
	// Now stamps ObservedAt on spans and logs; defaults to time.Now.
	Now func() time.Time
}

func (n *Normalizer) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

func (n *Normalizer) redacted(d int) {
	if d > 0 && n.OnRedacted != nil {
		n.OnRedacted(d)
	}
}

func (n *Normalizer) simulated(deliveryID string) bool {
	return n.SimRegistry != nil && deliveryID != "" && n.SimRegistry.IsSimulated(deliveryID)
}

// SimOrderRefPrefix is the order ref prefix of simulated deliveries (ruling R38).
const SimOrderRefPrefix = "mc-sim-"

const pkgPrefix = "dt.events.v1."

// Allowed maps a payload's full message name to the only fields copied into
// attrs. Fields that do not exist in the protos are never listed, and personal
// data (dropoff, pickup_name, display_name, photo_url, coordinates) never is.
//
// Tracking events (everything in tracking.proto) copy every scalar field
// except timestamps (the "at" timestamp becomes occurred_at), derived from the
// descriptors at init, and additionally skip fields named like a position
// (R10). Messages not listed here produce no attrs and reason=unknown_payload.
var Allowed = map[string][]string{
	pkgPrefix + "DeliveryCreated":       {"delivery_id", "external_id", "delivery_model", "status", "pickup_policy"},
	pkgPrefix + "DeliveryStatusChanged": {"delivery_id", "from", "to", "actor"},
	pkgPrefix + "CourierAssigned":       {"delivery_id", "courier_id"},
	pkgPrefix + "CourierUpserted":       {"courier_id", "vehicle"},         // never display_name, photo_url
	pkgPrefix + "DeliveryUpdated":       {"delivery_id", "changed_fields"}, // never dropoff
}

// positionNames are field names that could carry a coordinate or a bearing.
var positionNames = map[string]bool{
	"lat": true, "lng": true, "lon": true, "latitude": true, "longitude": true, "accuracy": true, "heading": true,
}

func init() {
	msgs := eventsv1.File_dt_events_v1_tracking_proto.Messages()
	for i := 0; i < msgs.Len(); i++ {
		md := msgs.Get(i)
		var fields []string
		for j := 0; j < md.Fields().Len(); j++ {
			fd := md.Fields().Get(j)
			if fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind || positionNames[string(fd.Name())] {
				continue // timestamps, LatLng and any other message
			}
			fields = append(fields, string(fd.Name()))
		}
		Allowed[string(md.FullName())] = fields
	}
}

// copyAllowed copies the listed scalar (or repeated scalar) fields of m.
func copyAllowed(m protoreflect.Message, fields []string) map[string]any {
	out := map[string]any{}
	for _, name := range fields {
		fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
		if fd == nil || fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind || fd.Kind() == protoreflect.BytesKind {
			continue
		}
		v := m.Get(fd)
		switch {
		case fd.IsList():
			l := v.List()
			arr := make([]any, 0, l.Len())
			for i := 0; i < l.Len(); i++ {
				arr = append(arr, scalar(fd, l.Get(i)))
			}
			out[name] = arr
		case fd.IsMap():
		default:
			out[name] = scalar(fd, v)
		}
	}
	return out
}

func scalar(fd protoreflect.FieldDescriptor, v protoreflect.Value) any {
	switch fd.Kind() {
	case protoreflect.EnumKind:
		if ev := fd.Enum().Values().ByNumber(v.Enum()); ev != nil {
			return string(ev.Name())
		}
		return int64(v.Enum())
	case protoreflect.BytesKind:
		return nil // bytes are never copied
	}
	return v.Interface()
}
