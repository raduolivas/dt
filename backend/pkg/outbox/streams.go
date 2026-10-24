package outbox

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// DuplicateWindow is how long JetStream remembers a Nats-Msg-Id.
const DuplicateWindow = 2 * time.Minute

// StreamAccounts carries the accounts service's events (tenant.*, apikey.*).
// It has no max age: the gateway rebuilds its key read model by replaying it.
var StreamAccounts = jetstream.StreamConfig{
	Name:       "ACCOUNTS",
	Subjects:   []string{"accounts.>"},
	Storage:    jetstream.FileStorage,
	Duplicates: DuplicateWindow,
}

// StreamDeliveries carries the deliveries service's events (delivery.*,
// courier.*). Consumers keep their own state, so it is bounded to 7 days.
var StreamDeliveries = jetstream.StreamConfig{
	Name:       "DELIVERIES",
	Subjects:   []string{"deliveries.>"},
	Storage:    jetstream.FileStorage,
	Duplicates: DuplicateWindow,
	MaxAge:     7 * 24 * time.Hour,
}

// SubjectFor returns the subject of eventType on stream:
// "<stream name lowercased>.<event type>", e.g. accounts.apikey.created.
func SubjectFor(stream jetstream.StreamConfig, eventType string) string {
	return strings.ToLower(stream.Name) + "." + eventType
}

// EnsureStream creates the stream or updates it to cfg. It is idempotent and
// safe to run from every replica at start-up.
func EnsureStream(ctx context.Context, js jetstream.JetStream, cfg jetstream.StreamConfig) error {
	if _, err := js.CreateOrUpdateStream(ctx, cfg); err != nil {
		return fmt.Errorf("outbox: ensure stream %s: %w", cfg.Name, err)
	}
	return nil
}
