package outbox_test

import (
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/ids"
	"github.com/raduolivas/dt/backend/pkg/outbox"
)

func TestNewEnvelope(t *testing.T) {
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	payload := &eventsv1.ApiKeyRevoked{KeyId: "ak_01"}
	env, err := outbox.NewEnvelope("apikey.revoked", "ten_01", eventsv1.Environment_ENVIRONMENT_LIVE,
		"accounts", nil, payload, at)
	if err != nil {
		t.Fatal(err)
	}
	if !ids.Valid(ids.Event, env.GetEventId()) {
		t.Errorf("event_id = %q, want an evt_ id", env.GetEventId())
	}
	if env.GetType() != "apikey.revoked" || env.GetSchemaVersion() != 1 || env.GetTenantId() != "ten_01" ||
		env.GetEnvironment() != eventsv1.Environment_ENVIRONMENT_LIVE || env.GetSource() != "accounts" {
		t.Errorf("envelope fields = %v", env)
	}
	if !env.GetOccurredAt().AsTime().Equal(at) {
		t.Errorf("occurred_at = %v, want %v", env.GetOccurredAt().AsTime(), at)
	}
	got := &eventsv1.ApiKeyRevoked{}
	if err := env.GetPayload().UnmarshalTo(got); err != nil {
		t.Fatal(err)
	}
	if got.GetKeyId() != "ak_01" {
		t.Errorf("payload key_id = %q", got.GetKeyId())
	}
	other, err := outbox.NewEnvelope("apikey.revoked", "ten_01", eventsv1.Environment_ENVIRONMENT_LIVE,
		"accounts", nil, payload, at)
	if err != nil {
		t.Fatal(err)
	}
	if other.GetEventId() == env.GetEventId() {
		t.Error("two envelopes share an event_id")
	}
}

func TestNewEnvelopeRejectsMissingPayload(t *testing.T) {
	if _, err := outbox.NewEnvelope("apikey.revoked", "ten_01", eventsv1.Environment_ENVIRONMENT_TEST,
		"accounts", nil, nil, time.Now()); err == nil {
		t.Fatal("want an error for a nil payload")
	}
}

func TestStreamTopology(t *testing.T) {
	s := outbox.StreamAccounts
	if s.Name != "ACCOUNTS" || len(s.Subjects) != 1 || s.Subjects[0] != "accounts.>" ||
		s.Storage != jetstream.FileStorage || s.Duplicates != 2*time.Minute || s.MaxAge != 0 {
		t.Errorf("StreamAccounts = %+v", s)
	}
	if got := outbox.SubjectFor(s, "apikey.created"); got != "accounts.apikey.created" {
		t.Errorf("SubjectFor = %q", got)
	}
}
