package normalize

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/config"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/topology"
)

const (
	latSentinel = 51.123456
	lngSentinel = 8.654321
)

// fill sets every field of m: strings to SECRET_<field>, doubles to 12.5,
// LatLng to the coordinate sentinels, everything else non-zero.
func fill(m protoreflect.Message, depth int) {
	fds := m.Descriptor().Fields()
	for i := 0; i < fds.Len(); i++ {
		fd := fds.Get(i)
		value := func() protoreflect.Value {
			switch fd.Kind() {
			case protoreflect.StringKind:
				return protoreflect.ValueOfString("SECRET_" + string(fd.Name()))
			case protoreflect.BytesKind:
				return protoreflect.ValueOfBytes([]byte("SECRET_" + string(fd.Name())))
			case protoreflect.DoubleKind:
				return protoreflect.ValueOfFloat64(12.5)
			case protoreflect.Int32Kind:
				return protoreflect.ValueOfInt32(777)
			case protoreflect.Uint64Kind:
				return protoreflect.ValueOfUint64(9)
			case protoreflect.BoolKind:
				return protoreflect.ValueOfBool(true)
			case protoreflect.EnumKind:
				return protoreflect.ValueOfEnum(1)
			}
			return m.NewField(fd)
		}
		if fd.Kind() == protoreflect.MessageKind && !fd.IsList() {
			sub := m.NewField(fd)
			mm := sub.Message()
			switch mm.Descriptor().FullName() {
			case "dt.events.v1.LatLng":
				mm.Set(mm.Descriptor().Fields().ByName("lat"), protoreflect.ValueOfFloat64(latSentinel))
				mm.Set(mm.Descriptor().Fields().ByName("lng"), protoreflect.ValueOfFloat64(lngSentinel))
			case "google.protobuf.Timestamp":
				mm.Set(mm.Descriptor().Fields().ByName("seconds"), protoreflect.ValueOfInt64(1_700_000_000))
			default:
				if depth < 3 {
					fill(mm, depth+1)
				}
			}
			m.Set(fd, sub)
			continue
		}
		if fd.IsList() {
			l := m.Mutable(fd).List()
			if fd.Kind() == protoreflect.MessageKind {
				l.Append(protoreflect.ValueOfMessage(l.NewElement().Message()))
			} else {
				l.Append(value())
			}
			continue
		}
		m.Set(fd, value())
	}
}

func newNormalizer() *Normalizer {
	return &Normalizer{Topology: topology.Default(), Thresholds: config.Thresholds{Slow: time.Second}}
}

func envelopeFor(t *testing.T, msg proto.Message, source string, delivery string) *eventsv1.Envelope {
	t.Helper()
	a, err := anypb.New(msg)
	require.NoError(t, err)
	return &eventsv1.Envelope{
		EventId: "evt_1", Type: "x", TenantId: "ten_1", Environment: eventsv1.Environment_ENVIRONMENT_TEST,
		OccurredAt: timestamppb.New(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)), Source: source,
		Subject: &eventsv1.Subject{DeliveryId: delivery, CourierId: "cou_1", DeliveryVersion: 3}, Payload: a,
	}
}

func TestEveryPayloadTypeIsAllowListed(t *testing.T) {
	n := newNormalizer()
	forbidden := []string{
		fmt.Sprint(latSentinel), fmt.Sprint(lngSentinel), "SECRET_display_name", "SECRET_photo_url",
		"SECRET_pickup_name", "SECRET_notes", "SECRET_hash", "SECRET_lookup", "SECRET_logo", "SECRET_allowed_ips",
		"SECRET_embed_origins", "SECRET_primary_color",
	}
	count := 0
	for _, name := range []string{"deliveries", "tracking", "gateway", "accounts"} {
		fd, err := protoregistry.GlobalFiles.FindFileByPath("dt/events/v1/" + name + ".proto")
		require.NoError(t, err)
		msgs := fd.Messages()
		for i := 0; i < msgs.Len(); i++ {
			md := msgs.Get(i)
			mt, err := protoregistry.GlobalTypes.FindMessageByName(md.FullName())
			require.NoError(t, err)
			msg := mt.New()
			fill(msg, 0)
			env := envelopeFor(t, msg.Interface(), "relay", "del_1")
			ev, err := n.FromEnvelope("tracking."+string(md.Name()), env, time.Now())
			require.NoError(t, err, md.FullName())
			text := fmt.Sprintf("%v %v %v %v %v %v %v %s", ev.Kind, ev.Source, ev.Target, ev.Reason, ev.DeliveryID, ev.OrderRef, ev.TenantID, ev.Attrs)
			for _, f := range forbidden {
				require.NotContains(t, text, f, "%s leaked %s", md.FullName(), f)
			}
			count++
		}
	}
	require.GreaterOrEqual(t, count, 15)
}

func TestAllowedFieldsExistInProtos(t *testing.T) {
	require.NotEmpty(t, Allowed)
	for full, fields := range Allowed {
		d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(full))
		require.NoError(t, err, full)
		md := d.(protoreflect.MessageDescriptor)
		for _, f := range fields {
			require.NotNil(t, md.Fields().ByName(protoreflect.Name(f)), "%s.%s", full, f)
		}
	}
	require.Equal(t, []string{"delivery_id", "changed_fields"}, Allowed["dt.events.v1.DeliveryUpdated"])
	require.Equal(t, []string{"courier_id", "vehicle"}, Allowed["dt.events.v1.CourierUpserted"])
}

func TestAllowedCopyValues(t *testing.T) {
	n := newNormalizer()
	du := &eventsv1.DeliveryUpdated{DeliveryId: "del_1", ChangedFields: []string{"dropoff", "ready_at"},
		Dropoff: &eventsv1.LatLng{Lat: latSentinel, Lng: lngSentinel}}
	ev, err := n.FromEnvelope("deliveries.delivery.updated", envelopeFor(t, du, "api", "del_1"), time.Now())
	require.NoError(t, err)
	require.JSONEq(t, `{"delivery_id":"del_1","changed_fields":["dropoff","ready_at"],"event_type":"x"}`, string(ev.Attrs))

	el := &eventsv1.EtaChanged{DeliveryId: "del_1", EtaSeconds: 90, EtaState: "live", At: timestamppb.New(time.Unix(1_700_000_000, 0))}
	ev, err = n.FromEnvelope("tracking.tracking.eta_changed", envelopeFor(t, el, "relay", "del_1"), time.Now())
	require.NoError(t, err)
	require.JSONEq(t, `{"delivery_id":"del_1","eta_seconds":90,"eta_state":"live","event_type":"x"}`, string(ev.Attrs))
	require.Equal(t, time.Unix(1_700_000_000, 0).UTC(), ev.OccurredAt.UTC())

	ev, err = n.FromEnvelope("accounts.apikey.created", envelopeFor(t, &eventsv1.ApiKeyCreated{KeyId: "k", Hash: []byte("h")}, "api", ""), time.Now())
	require.NoError(t, err)
	require.Equal(t, "unknown_payload", ev.Reason)
	require.JSONEq(t, `{}`, string(ev.Attrs))
}

func TestOrderRefFromDeliveryCreated(t *testing.T) {
	n := newNormalizer()
	dc := &eventsv1.DeliveryCreated{DeliveryId: "del_1", ExternalId: "ORDER-77", DeliveryModel: "direct", Status: "created", PickupPolicy: "any",
		Pickup: &eventsv1.LatLng{Lat: latSentinel}, PickupName: "SECRET_pickup_name"}
	ev, err := n.FromEnvelope("deliveries.delivery.created", envelopeFor(t, dc, "api", "del_1"), time.Unix(50, 0))
	require.NoError(t, err)
	require.Equal(t, "ORDER-77", ev.OrderRef)
	require.Equal(t, "message_published", ev.Kind)
	require.Equal(t, "deliveries", ev.Source)
	require.Equal(t, "nats.DELIVERIES", ev.Target)
	require.Equal(t, "del_1", ev.DeliveryID)
	require.Equal(t, "ten_1", ev.TenantID)
	require.Equal(t, "test", ev.Env)
	require.Equal(t, "live", ev.Origin)
	require.Equal(t, time.Unix(50, 0), ev.ObservedAt)

	st := &eventsv1.DeliveryStatusChanged{DeliveryId: "del_1", From: "a", To: "b", Actor: "system"}
	ev, err = n.FromEnvelope("deliveries.delivery.status_changed", envelopeFor(t, st, "api", "del_1"), time.Now())
	require.NoError(t, err)
	require.Empty(t, ev.OrderRef, "external_id only from DeliveryCreated")
}

type reg map[string]bool

func (r reg) IsSimulated(id string) bool { return r[id] }

func TestSimulatorSourceIsSimulated(t *testing.T) {
	n := newNormalizer()
	m := &eventsv1.CourierAssigned{DeliveryId: "del_1", CourierId: "cou_1"}
	ev, err := n.FromEnvelope("deliveries.courier.assigned", envelopeFor(t, m, "simulator", "del_1"), time.Now())
	require.NoError(t, err)
	require.Equal(t, "simulated", ev.Origin)

	n.SimRegistry = reg{"del_2": true}
	ev, err = n.FromEnvelope("deliveries.courier.assigned", envelopeFor(t, m, "api", "del_2"), time.Now())
	require.NoError(t, err)
	require.Equal(t, "simulated", ev.Origin)
	ev, err = n.FromEnvelope("deliveries.courier.assigned", envelopeFor(t, m, "api", "del_1"), time.Now())
	require.NoError(t, err)
	require.Equal(t, "live", ev.Origin)
}

func TestTrackingAllowListSkipsPositionsAndTimestamps(t *testing.T) {
	for full, fields := range Allowed {
		for _, f := range fields {
			require.False(t, positionNames[f], "%s.%s", full, f)
		}
	}
	require.Equal(t, []string{"delivery_id", "eta_seconds", "eta_state"}, Allowed["dt.events.v1.EtaChanged"])
	require.Equal(t, []string{"delivery_id", "courier_id"}, Allowed["dt.events.v1.CourierSignalLost"])
}

func TestFromEnvelopeErrors(t *testing.T) {
	n := newNormalizer()
	_, err := n.FromEnvelope("deliveries.x", nil, time.Now())
	require.Error(t, err)
	env := &eventsv1.Envelope{Payload: &anypb.Any{TypeUrl: "type.googleapis.com/nope.Missing"}}
	_, err = n.FromEnvelope("deliveries.x", env, time.Now())
	require.Error(t, err)
}
