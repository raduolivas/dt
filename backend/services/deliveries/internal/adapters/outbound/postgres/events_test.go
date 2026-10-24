package postgres_test

import (
	"bytes"
	"testing"
	"time"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/domain"
)

func TestStatusChangedCarriesNoReason(t *testing.T) {
	t.Parallel()
	e := setup(t)
	const reason = "customer Ana moved"
	d := create(t, e, scopeA, fullInput("ord-evt"), domain.DefaultTenantSettings(), t0)
	mutate(t, e, scopeA, d.ID, func(d *domain.Delivery) ([]domain.Event, error) {
		return d.Cancel(reason, t0.Add(time.Minute))
	})
	var found bool
	for _, r := range outboxRows(t, e) {
		if r.env.GetType() != domain.EventStatusChanged {
			continue
		}
		found = true
		m := &eventsv1.DeliveryStatusChanged{}
		if err := r.env.GetPayload().UnmarshalTo(m); err != nil {
			t.Fatal(err)
		}
		if f := m.ProtoReflect().Descriptor().Fields().ByName("reason"); f != nil {
			t.Errorf("DeliveryStatusChanged still has field %q", f.Name())
		}
		if bytes.Contains(r.raw, []byte(reason)) {
			t.Error("status_changed event carries the reason")
		}
	}
	if !found {
		t.Fatal("no status_changed event")
	}
}
