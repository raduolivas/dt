package consumer_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/outbox"
	"github.com/raduolivas/dt/backend/services/gateway/internal/adapters/inbound/consumer"
)

type expiry struct {
	id string
	at time.Time
}

type expirer struct {
	calls []expiry
	err   error
}

func (e *expirer) ExpireOnFinal(_ context.Context, id string, at time.Time) error {
	e.calls = append(e.calls, expiry{id, at})
	return e.err
}

func statusEnvelope(t *testing.T, typ string, msg proto.Message, occurred *timestamppb.Timestamp) *eventsv1.Envelope {
	t.Helper()
	var payload *anypb.Any
	if msg != nil {
		var err error
		if payload, err = anypb.New(msg); err != nil {
			t.Fatal(err)
		}
	}
	return &eventsv1.Envelope{Type: typ, Payload: payload, OccurredAt: occurred}
}

func TestDeliveriesConsumerExpiresLinksOnFinal(t *testing.T) {
	t.Parallel()
	if consumer.DeliveriesConfig.Durable != "gateway-deliveries" || consumer.DeliveriesStream != "DELIVERIES" ||
		consumer.DeliveriesConfig.FilterSubject != "deliveries.delivery.status_changed" {
		t.Fatalf("config = %+v", consumer.DeliveriesConfig)
	}
	ctx := context.Background()
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	e := &expirer{}
	h := consumer.Deliveries(e)
	for _, to := range []string{"delivered", "cancelled", "failed", "picked_up", "arriving"} {
		ev := statusEnvelope(t, "delivery.status_changed",
			&eventsv1.DeliveryStatusChanged{DeliveryId: "del_" + to, From: "x", To: to, OccurredAt: timestamppb.New(at)}, nil)
		if err := h(ctx, nil, ev); err != nil {
			t.Fatalf("%s: %v", to, err)
		}
	}
	if len(e.calls) != 3 || e.calls[0] != (expiry{"del_delivered", at}) || e.calls[2].id != "del_failed" {
		t.Fatalf("calls = %+v", e.calls)
	}

	// The envelope time stands in for a missing occurred_at.
	e.calls = nil
	if err := h(ctx, nil, statusEnvelope(t, "delivery.status_changed",
		&eventsv1.DeliveryStatusChanged{DeliveryId: "del_x", To: "delivered"}, timestamppb.New(at))); err != nil || len(e.calls) != 1 || !e.calls[0].at.Equal(at) {
		t.Fatalf("envelope time: %+v %v", e.calls, err)
	}
	// Other events are ignored.
	if err := h(ctx, nil, statusEnvelope(t, "delivery.created", nil, nil)); err != nil {
		t.Fatal(err)
	}
	// Malformed is permanent; a store error is retried.
	for name, ev := range map[string]*eventsv1.Envelope{
		"no payload": statusEnvelope(t, "delivery.status_changed", nil, nil),
		"no id":      statusEnvelope(t, "delivery.status_changed", &eventsv1.DeliveryStatusChanged{To: "delivered", OccurredAt: timestamppb.New(at)}, nil),
		"no time":    statusEnvelope(t, "delivery.status_changed", &eventsv1.DeliveryStatusChanged{DeliveryId: "del_y", To: "delivered"}, nil),
	} {
		if err := h(ctx, nil, ev); !errors.Is(err, outbox.ErrPermanent) {
			t.Errorf("%s: %v", name, err)
		}
	}
	e.err = errors.New("db down")
	err := h(ctx, nil, statusEnvelope(t, "delivery.status_changed",
		&eventsv1.DeliveryStatusChanged{DeliveryId: "del_z", To: "failed", OccurredAt: timestamppb.New(at)}, nil))
	if err == nil || errors.Is(err, outbox.ErrPermanent) {
		t.Fatalf("store error: %v", err)
	}
}
