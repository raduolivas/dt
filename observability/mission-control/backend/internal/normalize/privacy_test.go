package normalize

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
)

// sentinels maps the rendered form of every unique sentinel value to the
// dotted field path it was written to ("pickup.lat", "changed_fields", ...).
type sentinels map[string]string

// fillAll writes a unique sentinel into every scalar field (all int, uint,
// sint, fixed, float, double, string and bytes kinds), recurses into nested
// messages and fills repeated fields with filled elements. Booleans and enums
// are set but carry no unique value, so they are not tracked.
func fillAll(m protoreflect.Message, path string, depth int, counter *int, out sentinels) {
	fds := m.Descriptor().Fields()
	for i := 0; i < fds.Len(); i++ {
		fd := fds.Get(i)
		p := string(fd.Name())
		if path != "" {
			p = path + "." + p
		}
		if fd.IsMap() {
			continue
		}
		if fd.IsList() {
			l := m.Mutable(fd).List()
			el := l.NewElement()
			l.Append(fillValue(m, fd, el, p, depth, counter, out))
			continue
		}
		var v protoreflect.Value
		if fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind {
			v = m.NewField(fd)
		}
		m.Set(fd, fillValue(m, fd, v, p, depth, counter, out))
	}
}

func fillValue(_ protoreflect.Message, fd protoreflect.FieldDescriptor, cur protoreflect.Value, p string, depth int, counter *int, out sentinels) protoreflect.Value {
	*counter++
	k := *counter
	num := int64(7_000_000 + k)
	track := func(rendered string) { out[rendered] = strings.TrimSuffix(p, "[]") }
	switch fd.Kind() {
	case protoreflect.StringKind:
		s := fmt.Sprintf("SENT%04d_%s", k, fd.Name())
		track(s)
		return protoreflect.ValueOfString(s)
	case protoreflect.BytesKind:
		s := fmt.Sprintf("SENT%04d_%s", k, fd.Name())
		track(s)
		return protoreflect.ValueOfBytes([]byte(s))
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		track(strconv.FormatInt(num, 10))
		return protoreflect.ValueOfInt32(int32(num))
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		track(strconv.FormatInt(num, 10))
		return protoreflect.ValueOfInt64(num)
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		track(strconv.FormatInt(num, 10))
		return protoreflect.ValueOfUint32(uint32(num))
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		track(strconv.FormatInt(num, 10))
		return protoreflect.ValueOfUint64(uint64(num))
	case protoreflect.FloatKind:
		f := float32(num) + 0.25
		track(strconv.FormatFloat(float64(f), 'f', -1, 32))
		return protoreflect.ValueOfFloat32(f)
	case protoreflect.DoubleKind:
		f := float64(num) + 0.25
		track(strconv.FormatFloat(f, 'f', -1, 64))
		return protoreflect.ValueOfFloat64(f)
	case protoreflect.BoolKind:
		return protoreflect.ValueOfBool(true)
	case protoreflect.EnumKind:
		return protoreflect.ValueOfEnum(1)
	}
	// Message kinds.
	if !cur.IsValid() {
		return cur
	}
	mm := cur.Message()
	if mm.Descriptor().FullName() == "google.protobuf.Timestamp" {
		mm.Set(mm.Descriptor().Fields().ByName("seconds"), protoreflect.ValueOfInt64(1_700_000_000+int64(k)))
		return cur
	}
	if depth < 4 {
		fillAll(mm, p, depth+1, counter, out)
	}
	return cur
}

func allMessages(t *testing.T) []protoreflect.MessageDescriptor {
	t.Helper()
	var all []protoreflect.MessageDescriptor
	var walk func(ms protoreflect.MessageDescriptors)
	walk = func(ms protoreflect.MessageDescriptors) {
		for i := 0; i < ms.Len(); i++ {
			all = append(all, ms.Get(i))
			walk(ms.Get(i).Messages())
		}
	}
	protoregistry.GlobalFiles.RangeFilesByPackage("dt.events.v1", func(fd protoreflect.FileDescriptor) bool {
		walk(fd.Messages())
		return true
	})
	return all
}

// TestEveryPayloadTypeIsAllowListed walks every message in the events
// package, fills every field with a unique sentinel, normalizes it and fails
// if any sentinel reaches the Event unless its field is in Allowed for that
// message. A new unlisted PII field therefore fails here.
func TestEveryPayloadTypeIsAllowListed(t *testing.T) {
	n := newNormalizer()
	count, allowedSeen := 0, 0
	for _, md := range allMessages(t) {
		if md.Name() == "Envelope" || md.Name() == "Subject" {
			continue
		}
		mt, err := protoregistry.GlobalTypes.FindMessageByName(md.FullName())
		require.NoError(t, err, md.FullName())
		msg := mt.New()
		counter := 0
		sent := sentinels{}
		fillAll(msg, "", 0, &counter, sent)
		ev, err := n.FromEnvelope("tracking."+string(md.Name()), envelopeFor(t, msg.Interface(), "relay", "del_1"), time.Now())
		require.NoError(t, err, md.FullName())
		text := fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s", ev.Kind, ev.Source, ev.Target, ev.Outcome, ev.Reason,
			ev.TenantID, ev.Env, ev.DeliveryID, ev.OrderRef, ev.TraceID, ev.SpanID, ev.Origin, ev.Attrs)
		allowed := map[string]bool{}
		for _, f := range Allowed[string(md.FullName())] {
			allowed[f] = true
		}
		for rendered, path := range sent {
			if !strings.Contains(text, rendered) {
				continue
			}
			require.True(t, allowed[path], "%s: field %q (value %s) reached the event but is not in Allowed", md.FullName(), path, rendered)
			allowedSeen++
		}
		count++
	}
	require.GreaterOrEqual(t, count, 15)
	require.Greater(t, allowedSeen, 10, "the test must see allowed values reach the event, or it proves nothing")
}

func TestPrivacySentinelsCoverPII(t *testing.T) {
	// Guard the guard: the fill really writes the sentinels for the PII fields.
	md := (&eventsv1.DeliveryCreated{}).ProtoReflect().Descriptor()
	msg := (&eventsv1.DeliveryCreated{}).ProtoReflect().New()
	counter := 0
	sent := sentinels{}
	fillAll(msg, "", 0, &counter, sent)
	paths := map[string]bool{}
	for _, p := range sent {
		paths[p] = true
	}
	for _, want := range []string{"pickup.lat", "pickup.lng", "dropoff.lat", "pickup_name", "external_id"} {
		require.True(t, paths[want], "%s missing from sentinels of %s", want, md.FullName())
	}
	_ = eventsv1.File_dt_events_v1_deliveries_proto
}
