package consumer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go/jetstream"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/outbox"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

// EventTenantSettingsSet is the accounts event that carries the tenant
// settings the hosted page reads.
const EventTenantSettingsSet = "tenant.settings_set"

// maxLogoBytes is the largest logo accounts accepts; a bigger one is not
// stored (defence in depth, accounts validates it).
const maxLogoBytes = 102400

// TenantsConfig is the durable consumer that builds the viewer settings read
// model. Like the key consumer it replays the whole stream, one message at a
// time.
var TenantsConfig = jetstream.ConsumerConfig{
	Durable:       "gateway-tenants",
	FilterSubject: outbox.SubjectFor(outbox.StreamAccounts, EventTenantSettingsSet),
	DeliverPolicy: jetstream.DeliverAllPolicy,
	AckPolicy:     jetstream.AckExplicitPolicy,
	MaxAckPending: 1,
	AckWait:       30 * time.Second,
}

// Tenants returns the handler of tenant.settings_set: it stores the tenant's
// embed origins and branding unless the stored settings are as new or newer
// (the event's updated_at, else the envelope's occurred_at, orders them).
// The logo's SHA-256 is computed here. Malformed events are
// outbox.ErrPermanent; store errors are retried. The write is an idempotent
// upsert, so it does not need the inbox transaction.
func Tenants(store app.ViewerSettingsStore) outbox.Handler {
	return func(ctx context.Context, _ pgx.Tx, env *eventsv1.Envelope) error {
		if env.GetType() != EventTenantSettingsSet {
			return nil
		}
		p := &eventsv1.TenantSettingsSet{}
		if err := unpack(env, p); err != nil {
			return err
		}
		at := p.GetUpdatedAt()
		if at == nil {
			at = env.GetOccurredAt()
		}
		if p.GetTenantId() == "" || at == nil {
			return permanent("settings_set needs tenant_id and updated_at")
		}
		s := domain.ViewerSettings{EmbedOrigins: p.GetEmbedOrigins()}
		var logo []byte
		if b := p.GetBranding(); b != nil {
			if len(b.GetLogo()) > maxLogoBytes {
				return permanent("settings_set logo is too large")
			}
			br := &domain.Branding{DisplayName: b.GetDisplayName(), PrimaryColor: b.GetPrimaryColor(), Theme: b.GetTheme()}
			if len(b.GetLogo()) > 0 {
				sum := sha256.Sum256(b.GetLogo())
				br.LogoSHA256, br.LogoContentType = hex.EncodeToString(sum[:]), b.GetLogoContentType()
				logo = b.GetLogo()
				if br.LogoContentType != "image/png" && br.LogoContentType != "image/webp" {
					return permanent("settings_set logo has an unsupported content type")
				}
			}
			s.Branding = br
		}
		return store.Put(ctx, p.GetTenantId(), s, logo, at.AsTime())
	}
}
