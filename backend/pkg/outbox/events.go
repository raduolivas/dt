// Package outbox moves domain events from a service's database to NATS
// JetStream and into another service's database, exactly once in effect.
//
//   - Enqueue writes an event in the caller's transaction, so it is published
//     if and only if the business change commits.
//   - Relay publishes unpublished rows in creation order, with Nats-Msg-Id set
//     to the event id so JetStream drops retries inside its duplicate window.
//   - Consume records each event id in the inbox in the same transaction as
//     the handler's writes, so a redelivered event is a no-op.
//
// Every service's first migration creates the outbox and inbox tables with
// SchemaSQL, copied verbatim.
package outbox

import (
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/ids"
)

// SchemaVersion is the envelope schema version this package writes.
const SchemaVersion = 1

// SchemaSQL creates the outbox and inbox tables. Services copy it verbatim
// into their first migration; tests may execute it directly.
const SchemaSQL = `CREATE TABLE outbox (event_id text PRIMARY KEY, subject text NOT NULL, payload bytea NOT NULL,
  headers jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now(), published_at timestamptz);
CREATE INDEX outbox_unpublished ON outbox (created_at) WHERE published_at IS NULL;
CREATE TABLE inbox (consumer text NOT NULL, event_id text NOT NULL,
  processed_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY (consumer, event_id));
`

// NewEnvelope wraps payload in a new envelope with a fresh evt_ id and schema
// version 1. subject may be nil.
func NewEnvelope(eventType, tenantID string, env eventsv1.Environment, source string,
	subject *eventsv1.Subject, payload proto.Message, occurredAt time.Time,
) (*eventsv1.Envelope, error) {
	if eventType == "" {
		return nil, errors.New("outbox: event type is required")
	}
	if payload == nil {
		return nil, errors.New("outbox: payload is required")
	}
	packed, err := anypb.New(payload)
	if err != nil {
		return nil, fmt.Errorf("outbox: pack payload: %w", err)
	}
	return &eventsv1.Envelope{
		EventId:       ids.New(ids.Event),
		Type:          eventType,
		SchemaVersion: SchemaVersion,
		TenantId:      tenantID,
		Environment:   env,
		OccurredAt:    timestamppb.New(occurredAt),
		Source:        source,
		Subject:       subject,
		Payload:       packed,
	}, nil
}
