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
