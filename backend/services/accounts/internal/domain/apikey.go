package domain

import (
	"fmt"
	"net/netip"
	"slices"
	"time"
)

// Environment is the key environment.
type Environment string

const (
	EnvTest Environment = "test"
	EnvLive Environment = "live"
)

// Valid reports whether e is a known environment.
func (e Environment) Valid() bool { return e == EnvTest || e == EnvLive }

// APIKey is a partner secret key. Plaintext is never held; only the hash.
type APIKey struct {
	ID, TenantID, Name, Lookup, DisplayPrefix string
	Env                                       Environment
	Hash                                      []byte
	Scopes                                    []string
	AllowedIPs                                []netip.Prefix
	CreatedBy                                 Actor
	CreatedAt                                 time.Time
	ExpiresAt                                 *time.Time
	RevokedAt                                 *time.Time
}

// RollOverlap is how long a rolled key keeps working.
const RollOverlap = 24 * time.Hour

// AllScopes are every scope a key may carry.
var AllScopes = []string{"deliveries:read", "deliveries:write", "couriers:write", "webhooks:write"}

// Roll starts the overlap window: ExpiresAt becomes min(existing, now+24h).
// It returns the new expiry, or ErrKeyRevoked for a revoked key.
func (k *APIKey) Roll(now time.Time) (time.Time, error) {
	if k.RevokedAt != nil {
		return time.Time{}, ErrKeyRevoked
	}
	exp := now.Add(RollOverlap)
	if k.ExpiresAt != nil && k.ExpiresAt.Before(exp) {
		exp = *k.ExpiresAt
	}
	k.ExpiresAt = &exp
	return exp, nil
}

// Revoke revokes the key; it reports whether anything changed (idempotent).
func (k *APIKey) Revoke(now time.Time) bool {
	if k.RevokedAt != nil {
		return false
	}
	k.RevokedAt = &now
	return true
}

// ValidateScopes returns the scopes deduplicated and sorted; empty means all.
func ValidateScopes(s []string) ([]string, error) {
	if len(s) == 0 {
		return slices.Clone(AllScopes), nil
	}
	out := slices.Clone(s)
	for _, sc := range out {
		if !slices.Contains(AllScopes, sc) {
			return nil, fmt.Errorf("%w: %q", ErrInvalidScope, sc)
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}
