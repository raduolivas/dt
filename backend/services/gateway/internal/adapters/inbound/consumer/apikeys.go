// Package consumer applies accounts events to the gateway's read models.
package consumer

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/outbox"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

// The apikey.* event types accounts publishes.
const (
	EventKeyCreated   = "apikey.created"
	EventKeyExpirySet = "apikey.expiry_set"
	EventKeyRevoked   = "apikey.revoked"
)

// APIKeysStream is the stream the key consumer reads.
var APIKeysStream = outbox.StreamAccounts.Name

// APIKeysConfig is the durable consumer that builds the key read model. It
// replays the whole stream (DeliverAll) and keeps one message in flight
// (MaxAckPending 1), so events apply in stream order: a revoke never lands
// before its create.
var APIKeysConfig = jetstream.ConsumerConfig{
	Durable:       "gateway-apikeys",
	FilterSubject: "accounts.apikey.>",
	DeliverPolicy: jetstream.DeliverAllPolicy,
	AckPolicy:     jetstream.AckExplicitPolicy,
	MaxAckPending: 1,
	AckWait:       30 * time.Second,
}

// KeyWriter writes the key read model inside the consumer's transaction.
type KeyWriter interface {
	// InsertKey adds k; an existing id is left unchanged.
	InsertKey(ctx context.Context, tx pgx.Tx, k domain.KeyRecord) error
	// SetExpiry and Revoke are no-ops for an unknown id.
	SetExpiry(ctx context.Context, tx pgx.Tx, id string, at time.Time) error
	Revoke(ctx context.Context, tx pgx.Tx, id string, at time.Time) error
}

// APIKeys returns the handler for apikey.* events. Payloads that cannot be
// applied (wrong type, missing fields) fail with outbox.ErrPermanent so they
// are terminated instead of stalling the ordered consumer; store errors stay
// transient and are retried. Unknown event types are acked and ignored.
func APIKeys(w KeyWriter) outbox.Handler {
	return func(ctx context.Context, tx pgx.Tx, env *eventsv1.Envelope) error {
		switch env.GetType() {
		case EventKeyCreated:
			p := &eventsv1.ApiKeyCreated{}
			if err := unpack(env, p); err != nil {
				return err
			}
			k, err := keyRecord(p)
			if err != nil {
				return err
			}
			return w.InsertKey(ctx, tx, k)
		case EventKeyExpirySet:
			p := &eventsv1.ApiKeyExpirySet{}
			if err := unpack(env, p); err != nil {
				return err
			}
			if p.GetKeyId() == "" || p.GetExpiresAt() == nil {
				return permanent("expiry_set needs key_id and expires_at")
			}
			return w.SetExpiry(ctx, tx, p.GetKeyId(), p.GetExpiresAt().AsTime())
		case EventKeyRevoked:
			p := &eventsv1.ApiKeyRevoked{}
			if err := unpack(env, p); err != nil {
				return err
			}
			if p.GetKeyId() == "" {
				return permanent("revoked needs key_id")
			}
			// Never drop a revocation over a missing timestamp.
			at := p.GetRevokedAt()
			if at == nil {
				at = env.GetOccurredAt()
			}
			if at == nil {
				return permanent("revoked has neither revoked_at nor occurred_at")
			}
			return w.Revoke(ctx, tx, p.GetKeyId(), at.AsTime())
		}
		return nil
	}
}

// unpack decodes env's payload into p; a missing or mismatched payload is
// permanent.
func unpack(env *eventsv1.Envelope, p proto.Message) error {
	if env.GetPayload() == nil {
		return permanent(env.GetType() + " has no payload")
	}
	if err := env.GetPayload().UnmarshalTo(p); err != nil {
		return fmt.Errorf("unpack %s: %w: %w", env.GetType(), err, outbox.ErrPermanent)
	}
	return nil
}

func keyRecord(p *eventsv1.ApiKeyCreated) (domain.KeyRecord, error) {
	if p.GetKeyId() == "" || p.GetTenantId() == "" || p.GetLookup() == "" || len(p.GetHash()) == 0 {
		return domain.KeyRecord{}, permanent("created needs key_id, tenant_id, lookup and hash")
	}
	var env domain.Environment
	switch p.GetEnvironment() {
	case eventsv1.Environment_ENVIRONMENT_TEST:
		env = domain.EnvTest
	case eventsv1.Environment_ENVIRONMENT_LIVE:
		env = domain.EnvLive
	default:
		return domain.KeyRecord{}, permanent("created has no environment")
	}
	k := domain.KeyRecord{ID: p.GetKeyId(), TenantID: p.GetTenantId(), Lookup: p.GetLookup(), Env: env, Hash: p.GetHash()}
	for _, s := range p.GetScopes() {
		k.Scopes = append(k.Scopes, domain.Scope(s))
	}
	for _, s := range p.GetAllowedIps() {
		pre, err := netip.ParsePrefix(s)
		if err != nil {
			return domain.KeyRecord{}, permanent("created has an invalid allowed_ips entry")
		}
		k.AllowedIPs = append(k.AllowedIPs, pre)
	}
	return k, nil
}

func permanent(msg string) error { return fmt.Errorf("%s: %w", msg, outbox.ErrPermanent) }
