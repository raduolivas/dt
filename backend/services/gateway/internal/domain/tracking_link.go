package domain

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"
)

// LinkTTLAfterFinal is how long a tracking link keeps working after its
// delivery reached a final status.
const LinkTTLAfterFinal = 30 * time.Minute

// linkTokenBytes is the link token's entropy: 128 bits.
const linkTokenBytes = 16

// linkEncoding is unpadded base64url with canonical trailing bits, so every
// token has exactly one spelling (22 characters).
var linkEncoding = base64.RawURLEncoding.Strict()

// TrackingLink is a customer-facing tracking link. Only a keyed hash of its
// token (app.LinkHasher) is ever stored; the token itself is shown once, to
// the partner.
type TrackingLink struct {
	ID, TenantID, DeliveryID string
	Env                      Environment
	CreatedAt                time.Time
	// RevokedAt is set when the link was rotated away.
	RevokedAt *time.Time
	// ExpiresAt is set once the delivery is final (LinkExpiry).
	ExpiresAt *time.Time
}

// Usable reports whether the link may be used at now: not revoked, and not
// past its expiry (a link expiring at now is unusable).
func (l TrackingLink) Usable(now time.Time) bool {
	if l.RevokedAt != nil {
		return false
	}
	return l.ExpiresAt == nil || now.Before(*l.ExpiresAt)
}

// LinkExpiry is when the links of a delivery that became final at finalAt
// stop working.
func LinkExpiry(finalAt time.Time) time.Time { return finalAt.Add(LinkTTLAfterFinal) }

// NewLinkToken returns a fresh 128-bit random token, base64url without
// padding (22 characters).
func NewLinkToken() (string, error) {
	b := make([]byte, linkTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("tracking link token: %w", err)
	}
	return linkEncoding.EncodeToString(b), nil
}

// ValidLinkToken reports whether s has the shape of a link token, so a
// malformed value never reaches the store.
func ValidLinkToken(s string) bool {
	if len(s) != linkEncoding.EncodedLen(linkTokenBytes) {
		return false
	}
	b, err := linkEncoding.DecodeString(s)
	return err == nil && len(b) == linkTokenBytes
}

// FinalStatus reports whether a delivery status is final (delivered,
// cancelled or failed).
func FinalStatus(s string) bool {
	return s == "delivered" || s == "cancelled" || s == "failed"
}
