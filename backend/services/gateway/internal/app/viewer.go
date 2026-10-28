package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/raduolivas/dt/backend/pkg/viewerjwt"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

var (
	// ErrViewerTokenInvalid is the one answer for a viewer token that is
	// malformed, badly signed, expired, or whose link was rotated away or
	// expired.
	ErrViewerTokenInvalid = errors.New("gateway: viewer token invalid")
	// ErrTrackingUnavailable means the tracking service (or the keys to
	// check a viewer token) could not be reached; the caller may retry.
	ErrTrackingUnavailable = errors.New("gateway: tracking unavailable")
)

// Viewer rate limits (MVP spec §11.1), per client IP and per tracking link,
// shared by the session and refresh endpoints.
var (
	// ViewerIP is keyed rl:viewer:ip:<client ip>.
	ViewerIP = Limit{Rate: 10, Period: time.Minute, Burst: 10}
	// ViewerLink is keyed rl:viewer:tl:<link id>.
	ViewerLink = Limit{Rate: 30, Period: time.Minute, Burst: 30}
)

// ViewerLinkKey is the rate-limit key of a tracking link.
func ViewerLinkKey(linkID string) string { return "rl:viewer:tl:" + linkID }

// RateLimitedError is returned when a viewer request is over its limit.
type RateLimitedError struct{ Decision Decision }

func (e *RateLimitedError) Error() string { return "gateway: viewer rate limited" }

// SnapshotSource reads the customer's current snapshot of a link's delivery
// from the tracking service, as the JSON the public API carries.
type SnapshotSource interface {
	Snapshot(ctx context.Context, l domain.TrackingLink) (json.RawMessage, error)
}

// ViewerTokens signs and verifies viewer tokens. Verify returns
// viewerjwt.ErrInvalid for a token to refuse.
type ViewerTokens interface {
	Sign(c viewerjwt.Claims, now time.Time) (string, time.Time, error)
	Verify(ctx context.Context, token string, now time.Time) (viewerjwt.Claims, error)
}

// SignerTokens is ViewerTokens over a viewerjwt signer and verifier.
type SignerTokens struct {
	Signer   *viewerjwt.Signer
	Verifier *viewerjwt.Verifier
}

// Sign implements ViewerTokens.
func (s SignerTokens) Sign(c viewerjwt.Claims, now time.Time) (string, time.Time, error) {
	return s.Signer.Sign(c, now)
}

// Verify implements ViewerTokens.
func (s SignerTokens) Verify(ctx context.Context, token string, now time.Time) (viewerjwt.Claims, error) {
	return s.Verifier.Verify(ctx, token, now)
}

// Viewer runs the customer's tracking session: it exchanges a link token for
// a viewer token plus the current snapshot, and renews viewer tokens while
// their link stays valid.
type Viewer struct {
	Links     *TrackingLinks
	Tokens    ViewerTokens
	Snapshots SnapshotSource
	// Limiter applies ViewerLink. Nil disables it; a failing limiter lets
	// the request through (it never turns an outage into errors).
	Limiter Limiter
}

// Start resolves token to its link (ErrLinkInvalid), charges the link's rate
// limit (*RateLimitedError), reads the snapshot (ErrTrackingUnavailable) and
// signs a viewer token bound to the link. It returns the viewer token, its
// expiry and the snapshot.
func (v *Viewer) Start(ctx context.Context, token string, now time.Time) (string, time.Time, json.RawMessage, error) {
	l, err := v.Links.Resolve(ctx, token, now)
	if err != nil {
		return "", time.Time{}, nil, err
	}
	if err := v.limit(ctx, l.ID); err != nil {
		return "", time.Time{}, nil, err
	}
	snap, err := v.Snapshots.Snapshot(ctx, l)
	if err != nil {
		return "", time.Time{}, nil, fmt.Errorf("%w: %w", ErrTrackingUnavailable, err)
	}
	jwt, exp, err := v.Tokens.Sign(viewerjwt.Claims{DeliveryID: l.DeliveryID, TenantID: l.TenantID,
		Env: string(l.Env), LinkID: l.ID}, now)
	if err != nil {
		return "", time.Time{}, nil, err
	}
	return jwt, exp, snap, nil
}

// Refresh verifies a viewer token (ErrViewerTokenInvalid), charges its
// link's rate limit, checks that the link is still usable and still the one
// the token names, and signs a new token with the same claims. The link
// token is never needed again.
func (v *Viewer) Refresh(ctx context.Context, token string, now time.Time) (string, time.Time, error) {
	c, err := v.Tokens.Verify(ctx, token, now)
	switch {
	case errors.Is(err, viewerjwt.ErrUnavailable):
		return "", time.Time{}, fmt.Errorf("%w: %w", ErrTrackingUnavailable, err)
	case err != nil:
		return "", time.Time{}, ErrViewerTokenInvalid
	}
	if err := v.limit(ctx, c.LinkID); err != nil {
		return "", time.Time{}, err
	}
	l, err := v.Links.Active(ctx, c.LinkID, now)
	if errors.Is(err, ErrLinkInvalid) {
		return "", time.Time{}, ErrViewerTokenInvalid
	}
	if err != nil {
		return "", time.Time{}, err
	}
	if l.DeliveryID != c.DeliveryID || l.TenantID != c.TenantID || string(l.Env) != c.Env {
		return "", time.Time{}, ErrViewerTokenInvalid
	}
	return v.Tokens.Sign(c, now)
}

func (v *Viewer) limit(ctx context.Context, linkID string) error {
	if v.Limiter == nil {
		return nil
	}
	d, err := v.Limiter.Allow(ctx, ViewerLinkKey(linkID), ViewerLink)
	if err != nil || d.Allowed {
		return nil //nolint:nilerr // fail open, like every limiter (R21)
	}
	return &RateLimitedError{Decision: d}
}
