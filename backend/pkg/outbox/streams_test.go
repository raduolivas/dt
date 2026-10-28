package outbox_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/raduolivas/dt/backend/pkg/outbox"
	"github.com/raduolivas/dt/backend/pkg/testkit"
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

func TestStreamTracking(t *testing.T) {
	s := outbox.StreamTracking
	if got := outbox.SubjectFor(s, "tracking.arrival_detected"); got != "tracking.tracking.arrival_detected" {
		t.Fatalf("subject = %q", got)
	}
	if s.Name != "TRACKING" || len(s.Subjects) != 1 || s.Subjects[0] != "tracking.>" ||
		s.Storage != jetstream.FileStorage || s.Duplicates != outbox.DuplicateWindow {
		t.Fatalf("stream = %+v", s)
	}
	if s.MaxAge != 168*time.Hour {
		t.Fatalf("MaxAge = %v, want 168h", s.MaxAge)
	}
}

func TestWaitForStreamReturnsWhenCreated(t *testing.T) {
	nc, err := nats.Connect(testkit.StartNATS(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := outbox.StreamExists(ctx, js, "ACCOUNTS"); !errors.Is(err, jetstream.ErrStreamNotFound) {
		t.Fatalf("StreamExists before create = %v, want ErrStreamNotFound", err)
	}
	log := slog.New(slog.DiscardHandler)
	done := make(chan error, 1)
	go func() { done <- outbox.WaitForStream(ctx, js, "ACCOUNTS", log) }()
	time.Sleep(3 * time.Second)
	select {
	case err := <-done:
		t.Fatalf("returned before the stream existed: %v", err)
	default:
	}
	if err := outbox.EnsureStream(ctx, js, outbox.StreamAccounts); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitForStream: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitForStream did not return after the stream was created")
	}
	if err := outbox.StreamExists(ctx, js, "ACCOUNTS"); err != nil {
		t.Fatalf("StreamExists after create = %v", err)
	}
}

func TestWaitForStreamStopsOnCancel(t *testing.T) {
	nc, err := nats.Connect(testkit.StartNATS(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- outbox.WaitForStream(ctx, js, "ACCOUNTS", slog.New(slog.DiscardHandler))
	}()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitForStream on cancel = %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WaitForStream did not stop on cancel")
	}
}
