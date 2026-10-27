package consumer_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/outbox"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/adapters/inbound/consumer"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/app"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/domain"
)

var arrivalT0 = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

// memReporter applies reports to one in-memory delivery with the real
// domain rules, as app.Service.Report does in a transaction.
type memReporter struct {
	d       *domain.Delivery
	calls   []app.Scope
	reports []domain.Report
	applied int // status_changed events emitted
	err     error
}

func (m *memReporter) Report(_ context.Context, sc app.Scope, id string, r domain.Report) (*domain.Delivery, domain.Outcome, error) {
	m.calls = append(m.calls, sc)
	m.reports = append(m.reports, r)
	if m.err != nil {
		return nil, "", m.err
	}
	if id != m.d.ID || sc.TenantID != m.d.TenantID || sc.Env != m.d.Env {
		return nil, "", app.ErrNotFound
	}
	r.ReceivedAt = r.OccurredAt.Add(time.Second)
	o, evs, err := m.d.Report(r)
	for _, e := range evs {
		if e.Type == domain.EventStatusChanged {
			m.applied++
		}
	}
	return m.d, o, err
}

// pickedUpDelivery is a Direct delivery the courier has picked up.
func pickedUpDelivery(t *testing.T) *domain.Delivery {
	t.Helper()
	model := domain.ModelDirect
	s := domain.DefaultTenantSettings()
	s.AllowedModels = []domain.Model{domain.ModelDirect, domain.ModelEndToEnd}
	d, _, err := domain.NewDelivery("del_01jarrival000000000000000a", "ten_a", "test", domain.NewDeliveryInput{
		ExternalID: "o-1", Model: &model,
		Pickup: domain.Location{Lat: 52.368, Lng: 4.903}, PickupName: "Hub",
		Dropoff: domain.Location{Lat: 52.374, Lng: 4.889},
	}, s, arrivalT0)
	if err != nil {
		t.Fatal(err)
	}
	at := arrivalT0.Add(time.Minute)
	if _, _, err := d.Report(domain.Report{EventID: "p1", Type: domain.ReportPickupConfirmed, Actor: domain.ActorCourier,
		OccurredAt: at, ReceivedAt: at}); err != nil {
		t.Fatal(err)
	}
	if d.Status != domain.StatusPickedUp {
		t.Fatalf("status = %s", d.Status)
	}
	return d
}

func arrivalEnvelope(t *testing.T, eventID, deliveryID string, env eventsv1.Environment, at time.Time) *eventsv1.Envelope {
	t.Helper()
	payload, err := anypb.New(&eventsv1.ArrivalDetected{DeliveryId: deliveryID, At: timestamppb.New(at), DistanceM: 150})
	if err != nil {
		t.Fatal(err)
	}
	return &eventsv1.Envelope{EventId: eventID, Type: "tracking.arrival_detected", TenantId: "ten_a",
		Environment: env, OccurredAt: timestamppb.New(at), Payload: payload}
}

const (
	evtArrival1 = "evt_01jarrival0000000000000001"
	evtArrival2 = "evt_01jarrival0000000000000002"
	evtArrival3 = "evt_01jarrival0000000000000003"
)

func TestArrivalAppliesArrivingOnce(t *testing.T) {
	ctx := context.Background()
	d := pickedUpDelivery(t)
	rep := &memReporter{d: d}
	h := consumer.Tracking(rep, slog.New(slog.DiscardHandler))
	at := arrivalT0.Add(10 * time.Minute)

	if err := h(ctx, nil, arrivalEnvelope(t, evtArrival1, d.ID, eventsv1.Environment_ENVIRONMENT_TEST, at)); err != nil {
		t.Fatal(err)
	}
	if d.Status != domain.StatusArriving || rep.applied != 1 {
		t.Fatalf("status = %s, applied = %d", d.Status, rep.applied)
	}
	r := rep.reports[0]
	if r.Type != domain.ReportArriving || r.Actor != domain.ActorSystem || r.EventID != evtArrival1 || !r.OccurredAt.Equal(at) {
		t.Fatalf("report = %+v", r)
	}
	if rep.calls[0] != (app.Scope{TenantID: "ten_a", Env: "test", ActorID: "system"}) {
		t.Fatalf("scope = %+v", rep.calls[0])
	}

	// The same event again (a redelivery past the inbox): a duplicate.
	if err := h(ctx, nil, arrivalEnvelope(t, evtArrival1, d.ID, eventsv1.Environment_ENVIRONMENT_TEST, at)); err != nil {
		t.Fatal(err)
	}
	// Tracking re-firing after losing its state: a new event id, already
	// arriving, so nothing changes.
	if err := h(ctx, nil, arrivalEnvelope(t, evtArrival2, d.ID, eventsv1.Environment_ENVIRONMENT_TEST, at.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	// After delivery, a late arrival is an invalid transition: a no-op.
	if _, _, err := d.Report(domain.Report{EventID: "d1", Type: domain.ReportDelivered, Actor: domain.ActorCourier,
		OccurredAt: at.Add(time.Minute), ReceivedAt: at.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := h(ctx, nil, arrivalEnvelope(t, evtArrival3, d.ID, eventsv1.Environment_ENVIRONMENT_TEST, at.Add(2*time.Minute))); err != nil {
		t.Fatalf("late arrival = %v, want nil (no-op)", err)
	}
	if d.Status != domain.StatusDelivered || rep.applied != 1 {
		t.Fatalf("status = %s, arriving applied %d times", d.Status, rep.applied)
	}
}

func TestArrivalConsumerErrors(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	at := arrivalT0.Add(10 * time.Minute)

	// Another event type on the stream is ignored.
	rep := &memReporter{d: pickedUpDelivery(t)}
	other := arrivalEnvelope(t, evtArrival1, rep.d.ID, eventsv1.Environment_ENVIRONMENT_TEST, at)
	other.Type = "tracking.eta_changed"
	if err := consumer.Tracking(rep, log)(ctx, nil, other); err != nil || len(rep.calls) != 0 {
		t.Fatalf("other type: err %v, calls %d", err, len(rep.calls))
	}
	// An unknown delivery (erased, or never this scope's) is dropped.
	if err := consumer.Tracking(rep, log)(ctx, nil, arrivalEnvelope(t, evtArrival1, "del_01jarrival000000000000000z",
		eventsv1.Environment_ENVIRONMENT_TEST, at)); err != nil {
		t.Fatalf("unknown delivery = %v", err)
	}
	// Events that can never apply are permanent.
	for name, env := range map[string]*eventsv1.Envelope{
		"no environment": arrivalEnvelope(t, evtArrival1, rep.d.ID, eventsv1.Environment_ENVIRONMENT_UNSPECIFIED, at),
		"no delivery":    arrivalEnvelope(t, evtArrival1, "", eventsv1.Environment_ENVIRONMENT_TEST, at),
		"bad payload": func() *eventsv1.Envelope {
			e := arrivalEnvelope(t, evtArrival1, rep.d.ID, eventsv1.Environment_ENVIRONMENT_TEST, at)
			e.Payload, _ = anypb.New(&eventsv1.EtaChanged{})
			return e
		}(),
	} {
		if err := consumer.Tracking(rep, log)(ctx, nil, env); !errors.Is(err, outbox.ErrPermanent) {
			t.Fatalf("%s: err = %v, want ErrPermanent", name, err)
		}
	}
	// A database failure is retried.
	rep = &memReporter{d: pickedUpDelivery(t), err: errors.New("db down")}
	err := consumer.Tracking(rep, log)(ctx, nil, arrivalEnvelope(t, evtArrival1, rep.d.ID, eventsv1.Environment_ENVIRONMENT_TEST, at))
	if err == nil || errors.Is(err, outbox.ErrPermanent) {
		t.Fatalf("db down = %v, want a retryable error", err)
	}
}

func TestTrackingConsumerConfig(t *testing.T) {
	c := consumer.TrackingConfig
	if c.Durable != "deliveries-tracking" || c.FilterSubject != "tracking.tracking.arrival_detected" ||
		consumer.TrackingStream != "TRACKING" {
		t.Fatalf("config = %+v", c)
	}
}
