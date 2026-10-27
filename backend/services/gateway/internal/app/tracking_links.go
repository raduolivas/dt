package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/raduolivas/dt/backend/pkg/ids"
	"github.com/raduolivas/dt/backend/pkg/viewerjwt"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

// ErrLinkInvalid is the one answer for a link token that is unknown,
// malformed, rotated away or expired; callers cannot tell the cases apart.
var ErrLinkInvalid = errors.New("gateway: tracking link invalid")

// LinkStore keeps tracking links (Postgres). Only token hashes are stored.
type LinkStore interface {
	// Replace atomically revokes the delivery's unrevoked link (at
	// l.CreatedAt), if any, and stores l with the token hash. When the
	// delivery is already final, l gets the delivery's link expiry. It
	// returns l as stored.
	Replace(ctx context.Context, l domain.TrackingLink, tokenHash []byte) (domain.TrackingLink, error)
	// FindByHash returns the link whose token hashes to tokenHash, or
	// ErrNotFound.
	FindByHash(ctx context.Context, tokenHash []byte) (domain.TrackingLink, error)
	// Get returns the link with id, or ErrNotFound.
	Get(ctx context.Context, id string) (domain.TrackingLink, error)
	// Expire records expiresAt as the delivery's link expiry (the first one
	// recorded wins) and sets it on the delivery's unrevoked links that have
	// none. It returns the ids of the links it set it on.
	Expire(ctx context.Context, deliveryID string, expiresAt time.Time) ([]string, error)
	// RevokedSince returns the ids of the delivery's links revoked at or
	// after since.
	RevokedSince(ctx context.Context, deliveryID string, since time.Time) ([]string, error)
}

// Revoker cuts off viewers of a revoked link at once: it deny-lists the link
// id for the lifetime of a viewer token and tells every tracking instance to
// close the link's open streams.
type Revoker interface {
	Revoke(ctx context.Context, linkID string) error
}

// revokeAttempts is the first try plus two retries of each revocation.
const revokeAttempts = 3

// TrackingLinks issues, rotates, resolves and expires tracking links.
type TrackingLinks struct {
	Store   LinkStore
	Revoker Revoker
	Clock   Clock
}

// Issue creates the delivery's tracking link and returns it with its token
// (shown once; only its hash is stored). The caller has checked that the
// delivery belongs to p. A link the delivery already had is revoked.
func (t *TrackingLinks) Issue(ctx context.Context, p domain.Principal, deliveryID string) (domain.TrackingLink, string, error) {
	return t.replace(ctx, p, deliveryID)
}

// Rotate replaces the delivery's link with a new one, or issues one if it
// has none, and cuts off the viewers of the old one. If the revocation
// cannot be published Rotate fails (the new link is stored, but its token is
// not returned); the caller's retry rotates again and republishes every link
// revoked within a viewer token's lifetime, so no revocation is lost.
func (t *TrackingLinks) Rotate(ctx context.Context, p domain.Principal, deliveryID string) (domain.TrackingLink, string, error) {
	return t.replace(ctx, p, deliveryID)
}

func (t *TrackingLinks) replace(ctx context.Context, p domain.Principal, deliveryID string) (domain.TrackingLink, string, error) {
	tok, err := domain.NewLinkToken()
	if err != nil {
		return domain.TrackingLink{}, "", err
	}
	now := t.Clock.Now()
	l, err := t.Store.Replace(ctx, domain.TrackingLink{
		ID: ids.New(ids.TrackingLink), TenantID: p.TenantID, Env: p.Env, DeliveryID: deliveryID, CreatedAt: now,
	}, domain.HashLinkToken(tok))
	if err != nil {
		return domain.TrackingLink{}, "", fmt.Errorf("store tracking link: %w", err)
	}
	revoked, err := t.Store.RevokedSince(ctx, deliveryID, now.Add(-viewerjwt.RevokedTTL))
	if err != nil {
		return domain.TrackingLink{}, "", fmt.Errorf("list revoked tracking links: %w", err)
	}
	for _, id := range revoked {
		if err := t.revoke(ctx, id); err != nil {
			return domain.TrackingLink{}, "", err
		}
	}
	return l, tok, nil
}

func (t *TrackingLinks) revoke(ctx context.Context, id string) error {
	var err error
	for range revokeAttempts {
		if err = t.Revoker.Revoke(ctx, id); err == nil {
			return nil
		}
	}
	return fmt.Errorf("revoke tracking link %s: %w", id, err)
}

// Resolve returns the usable link a token belongs to at now. Unknown,
// malformed, revoked and expired tokens are all ErrLinkInvalid; any other
// error means the store failed.
func (t *TrackingLinks) Resolve(ctx context.Context, token string, now time.Time) (domain.TrackingLink, error) {
	if !domain.ValidLinkToken(token) {
		return domain.TrackingLink{}, ErrLinkInvalid
	}
	l, err := t.Store.FindByHash(ctx, domain.HashLinkToken(token))
	if errors.Is(err, ErrNotFound) {
		return domain.TrackingLink{}, ErrLinkInvalid
	}
	if err != nil {
		return domain.TrackingLink{}, fmt.Errorf("find tracking link: %w", err)
	}
	if !l.Usable(now) {
		return domain.TrackingLink{}, ErrLinkInvalid
	}
	return l, nil
}

// Active returns link id if it is usable at now, else ErrLinkInvalid.
func (t *TrackingLinks) Active(ctx context.Context, id string, now time.Time) (domain.TrackingLink, error) {
	l, err := t.Store.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return domain.TrackingLink{}, ErrLinkInvalid
	}
	if err != nil {
		return domain.TrackingLink{}, fmt.Errorf("get tracking link: %w", err)
	}
	if !l.Usable(now) {
		return domain.TrackingLink{}, ErrLinkInvalid
	}
	return l, nil
}

// ExpireOnFinal makes the delivery's links expire 30 minutes after finalAt,
// the time it reached a final status. It is idempotent: the first final
// time recorded wins, so a redelivered event never extends a link.
func (t *TrackingLinks) ExpireOnFinal(ctx context.Context, deliveryID string, finalAt time.Time) error {
	if _, err := t.Store.Expire(ctx, deliveryID, domain.LinkExpiry(finalAt)); err != nil {
		return fmt.Errorf("expire tracking links: %w", err)
	}
	return nil
}
