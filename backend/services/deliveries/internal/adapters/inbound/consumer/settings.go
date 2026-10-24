// Package consumer holds the deliveries service's event consumers.
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
	"github.com/raduolivas/dt/backend/services/deliveries/internal/domain"
)

// SettingsStream is the stream tenant.settings_set events arrive on.
const SettingsStream = "ACCOUNTS"

// settingsEventType is the accounts event this consumer handles.
const settingsEventType = "tenant.settings_set"

// SettingsConfig is the durable consumer of tenant settings. MaxAckPending 1
// keeps events in stream order.
var SettingsConfig = jetstream.ConsumerConfig{
	Durable:       "deliveries-tenant-settings",
	FilterSubject: "accounts." + settingsEventType,
	DeliverPolicy: jetstream.DeliverAllPolicy,
	AckPolicy:     jetstream.AckExplicitPolicy,
	MaxAckPending: 1,
	AckWait:       30 * time.Second,
}

// Settings returns the handler that stores a tenant's settings with apply.
// The event is tenant-level, so its environment is not checked. apply keeps
// the newest settings by updatedAt, so an older event after a newer one is
// ignored. Settings that are not domain.TenantSettings.Valid (an unknown
// model or policy, or a default outside the allowed models) are never
// applied: the event is logged at error with its tenant and event ids only
// and rejected as outbox.ErrPermanent, so it is terminated instead of
// blocking the ordered consumer for every tenant.
func Settings(apply func(ctx context.Context, tx pgx.Tx, tenantID string, s domain.TenantSettings, updatedAt time.Time) error, log *slog.Logger) outbox.Handler {
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
		models := make([]domain.Model, len(ev.GetAllowedModels()))
		for i, m := range ev.GetAllowedModels() {
			models[i] = domain.Model(m)
		}
		s := domain.TenantSettings{
			AllowedModels: models,
			DefaultModel:  domain.Model(ev.GetDefaultModel()),
			PickupPolicy:  domain.PickupPolicy(ev.GetPickupConfirmation()),
		}
		if !s.Valid() {
			// No payload values: only the ids say which event to look at.
			log.ErrorContext(ctx, "tenant_settings_invalid",
				"tenant_id", tenantID, "event_id", env.GetEventId())
			return fmt.Errorf("invalid tenant settings: %w", outbox.ErrPermanent)
		}
		return apply(ctx, tx, tenantID, s, at)
	}
}
