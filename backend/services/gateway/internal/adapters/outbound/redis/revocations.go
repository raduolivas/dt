package redislimit

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/raduolivas/dt/backend/pkg/viewerjwt"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
)

// revokeTimeout bounds one revocation.
const revokeTimeout = 500 * time.Millisecond

// Revoker deny-lists revoked tracking links (dt:viewer:revoked:<tl_id>, for
// a viewer token's lifetime plus leeway) and announces them on
// dt:viewer:revocations so tracking closes their open streams.
type Revoker struct{ c *redis.Client }

var _ app.Revoker = (*Revoker)(nil)

// NewRevoker returns a Revoker over c.
func NewRevoker(c *redis.Client) *Revoker { return &Revoker{c: c} }

// Revoke sets the deny-list key first, then publishes, in one round trip.
func (r *Revoker) Revoke(ctx context.Context, linkID string) error {
	ctx, cancel := context.WithTimeout(ctx, revokeTimeout)
	defer cancel()
	_, err := r.c.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.Set(ctx, viewerjwt.RevokedKey(linkID), "1", viewerjwt.RevokedTTL)
		p.Publish(ctx, viewerjwt.RevocationsChannel, linkID)
		return nil
	})
	if err != nil {
		return fmt.Errorf("revoke %s: %w", linkID, err)
	}
	return nil
}

var _ app.ExpiryAnnouncer = (*Revoker)(nil)

// AnnounceExpiry stores the link's expiry at dt:viewer:expiry:<tl_id> (unix
// seconds, kept until a token exchanged before it can no longer verify), for
// streams that open later, then publishes "<tl_id> <unix>" on
// dt:viewer:expiries for streams already open; one round trip.
func (r *Revoker) AnnounceExpiry(ctx context.Context, linkID string, expiresAt time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, revokeTimeout)
	defer cancel()
	_, err := r.c.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.Set(ctx, viewerjwt.ExpiryKey(linkID), expiresAt.Unix(), viewerjwt.ExpiryKeyTTL(expiresAt, time.Now()))
		p.Publish(ctx, viewerjwt.ExpiriesChannel, viewerjwt.EncodeExpiry(linkID, expiresAt))
		return nil
	})
	if err != nil {
		return fmt.Errorf("announce expiry %s: %w", linkID, err)
	}
	return nil
}
