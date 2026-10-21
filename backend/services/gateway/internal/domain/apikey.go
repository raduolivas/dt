package domain

import (
	"net/netip"
	"slices"
	"time"
)

// Environment separates test from live data.
type Environment string

const (
	EnvTest Environment = "test"
	EnvLive Environment = "live"
)

// Valid reports whether e is a known environment.
func (e Environment) Valid() bool { return e == EnvTest || e == EnvLive }

// KeyRecord is the gateway's read model of a partner secret key, built from
// accounts apikey.* events. Hash is HMAC-SHA256(pepper, full key); the
// plaintext is never known here.
type KeyRecord struct {
	ID, TenantID, Lookup string
	Env                  Environment
	Hash                 []byte
	Scopes               []Scope
	AllowedIPs           []netip.Prefix
	ExpiresAt, RevokedAt *time.Time
}

// Usable reports whether the key may authenticate at now: it is not revoked
// and has no expiry or now is before it (a key expiring at now is unusable).
func (k KeyRecord) Usable(now time.Time) bool {
	if k.RevokedAt != nil {
		return false
	}
	return k.ExpiresAt == nil || now.Before(*k.ExpiresAt)
}

// AllowsIP reports whether ip may use the key. An empty allowlist allows any
// address; an IPv4-mapped IPv6 address matches IPv4 prefixes.
func (k KeyRecord) AllowsIP(ip netip.Addr) bool {
	if len(k.AllowedIPs) == 0 {
		return true
	}
	ip = ip.Unmap()
	return slices.ContainsFunc(k.AllowedIPs, func(p netip.Prefix) bool { return p.Contains(ip) })
}
