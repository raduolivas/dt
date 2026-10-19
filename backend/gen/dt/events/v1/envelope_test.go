package eventsv1_test

import (
	"testing"
	"time"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestEnvelopeRoundTrip(t *testing.T) {
	payload, err := anypb.New(timestamppb.New(time.Unix(1760000000, 0)))
	if err != nil {
		t.Fatal(err)
	}
	in := &eventsv1.Envelope{
		EventId: "evt_01", Type: "delivery.created", SchemaVersion: 1,
		TenantId: "ten_01", Environment: eventsv1.Environment_ENVIRONMENT_TEST,
		OccurredAt: timestamppb.New(time.Unix(1760000000, 0)), Source: "partner_api",
		Subject: &eventsv1.Subject{DeliveryId: "del_01"}, Payload: payload,
	}
	b, err := proto.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	out := &eventsv1.Envelope{}
	if err := proto.Unmarshal(b, out); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(in, out) {
		t.Fatalf("round trip mismatch: %v vs %v", in, out)
	}
}
