package consumer

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go/jetstream"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/outbox"
	"github.com/raduolivas/dt/backend/services/tracking/internal/app"
)

// AccountsStream is the stream tenant.settings_set events arrive on.
const AccountsStream = "ACCOUNTS"

const settingsEventType = "tenant.settings_set"

// AccountsConfig is the durable consumer of tenant settings. MaxAckPending
// 1 keeps events in stream order.
var AccountsConfig = jetstream.ConsumerConfig{
	Durable:       "tracking-accounts",
	FilterSubject: "accounts." + settingsEventType,
	DeliverPolicy: jetstream.DeliverAllPolicy,
	AckPolicy:     jetstream.AckExplicitPolicy,
	MaxAckPending: 1,
	AckWait:       30 * time.Second,
}

// Settings returns the handler that stores a tenant's default preparation
// time (newest updated_at wins) and refreshes the ETA of its active
// deliveries. A value outside 1..240 is rejected as outbox.ErrPermanent.
func Settings(b Binder, svc *app.Service, log *slog.Logger) outbox.Handler {
	if log == nil {
		log = slog.Default()
	}
	return func(ctx context.Context, tx pgx.Tx, env *eventsv1.Envelope) error {
		if env.GetType() != settingsEventType {
			return nil
		}
		var ev eventsv1.TenantSettingsSet
		if err := env.GetPayload().UnmarshalTo(&ev); err != nil {
			return fmt.Errorf("settings payload: %w", outbox.ErrPermanent)
		}
		tenantID := ev.GetTenantId()
		if tenantID == "" {
			tenantID = env.GetTenantId()
		}
		if tenantID == "" {
			return fmt.Errorf("settings event without tenant: %w", outbox.ErrPermanent)
		}
		at := env.GetOccurredAt().AsTime()
		if ev.GetUpdatedAt() != nil {
			at = ev.GetUpdatedAt().AsTime()
		}
		var prep *int
		if ev.DefaultPrepMinutes != nil {
			p := int(ev.GetDefaultPrepMinutes())
			if p < 1 || p > 240 {
				log.ErrorContext(ctx, "tenant_settings_invalid", "tenant_id", tenantID, "event_id", env.GetEventId())
				return fmt.Errorf("default_prep_minutes out of range: %w", outbox.ErrPermanent)
			}
			prep = &p
		}
		return svc.OnTenantSettings(ctx, b.Settings(tx), tenantID, prep, at)
	}
}
