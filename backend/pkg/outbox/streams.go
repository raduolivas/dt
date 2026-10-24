package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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

const (
	// streamPollInterval is how often WaitForStream looks for the stream.
	streamPollInterval = 2 * time.Second
	// streamWarnInterval is how often WaitForStream says it is still waiting.
	streamWarnInterval = 30 * time.Second
)

// StreamExists returns nil when the stream exists, or an error wrapping
// jetstream.ErrStreamNotFound when it does not. It never creates the stream:
// only the owning service does.
func StreamExists(ctx context.Context, js jetstream.JetStream, name string) error {
	if _, err := js.Stream(ctx, name); err != nil {
		return fmt.Errorf("outbox: stream %s: %w", name, err)
	}
	return nil
}

// WaitForStream blocks until the stream exists, polling every 2 s and logging
// a warning every 30 s. It returns nil when the stream exists or ctx ends
// (shutdown is not an error).
func WaitForStream(ctx context.Context, js jetstream.JetStream, name string, log *slog.Logger) error {
	poll := time.NewTicker(streamPollInterval)
	defer poll.Stop()
	warn := time.NewTicker(streamWarnInterval)
	defer warn.Stop()
	for {
		err := StreamExists(ctx, js, name)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return nil
		}
		if !errors.Is(err, jetstream.ErrStreamNotFound) {
			log.DebugContext(ctx, "stream check failed", "stream", name, "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-warn.C:
			log.WarnContext(ctx, "waiting for stream to be created by its owner", "stream", name)
		case <-poll.C:
		}
	}
}
