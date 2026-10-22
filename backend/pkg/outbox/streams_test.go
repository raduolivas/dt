package outbox_test

import (
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/raduolivas/dt/backend/pkg/outbox"
)

func TestStreamDeliveries(t *testing.T) {
	s := outbox.StreamDeliveries
	if got := outbox.SubjectFor(s, "delivery.created"); got != "deliveries.delivery.created" {
		t.Fatalf("subject = %q", got)
	}
	if s.Name != "DELIVERIES" || len(s.Subjects) != 1 || s.Subjects[0] != "deliveries.>" ||
		s.Storage != jetstream.FileStorage || s.Duplicates != outbox.DuplicateWindow {
		t.Fatalf("stream = %+v", s)
	}
	if s.MaxAge != 168*time.Hour {
		t.Fatalf("MaxAge = %v, want 168h", s.MaxAge)
	}
}
