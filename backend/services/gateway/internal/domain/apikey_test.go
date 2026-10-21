package domain

import (
	"net/netip"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func at(t time.Time) *time.Time { return &t }

func prefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(ss))
	for _, s := range ss {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

func TestUsableAndAllowsIP(t *testing.T) {
	t.Parallel()
	usable := []struct {
		name string
		key  KeyRecord
		want bool
	}{
		{"active, no expiry", KeyRecord{}, true},
		{"revoked", KeyRecord{RevokedAt: at(now.Add(-time.Second))}, false},
		{"revoked in the future still counts", KeyRecord{RevokedAt: at(now.Add(time.Hour))}, false},
		{"expired at exactly now", KeyRecord{ExpiresAt: at(now)}, false},
		{"expired in the past", KeyRecord{ExpiresAt: at(now.Add(-time.Nanosecond))}, false},
		{"future expiry", KeyRecord{ExpiresAt: at(now.Add(time.Nanosecond))}, true},
		{"future expiry but revoked", KeyRecord{ExpiresAt: at(now.Add(time.Hour)), RevokedAt: at(now)}, false},
	}
	for _, tc := range usable {
		if got := tc.key.Usable(now); got != tc.want {
			t.Errorf("Usable(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}

	allows := []struct {
		name    string
		allowed []netip.Prefix
		ip      string
		want    bool
	}{
		{"empty allowlist allows IPv4", nil, "198.51.100.1", true},
		{"empty allowlist allows IPv6", nil, "2001:db8::1", true},
		{"IPv4 in /24", prefixes("203.0.113.0/24"), "203.0.113.7", true},
		{"IPv4 outside /24", prefixes("203.0.113.0/24"), "203.0.114.7", false},
		{"IPv6 in /32", prefixes("2001:db8::/32"), "2001:db8:1::5", true},
		{"IPv6 outside /32", prefixes("2001:db8::/32"), "2001:db9::5", false},
		{"IPv6 single host", prefixes("2001:db8::1/128"), "2001:db8::1", true},
		{"IPv4-mapped IPv6 matches IPv4 prefix", prefixes("203.0.113.0/24"), "::ffff:203.0.113.7", true},
		{"IPv4-mapped IPv6 outside IPv4 prefix", prefixes("203.0.113.0/24"), "::ffff:203.0.114.7", false},
		{"IPv4 against IPv6-only list", prefixes("2001:db8::/32"), "203.0.113.7", false},
		{"second prefix matches", prefixes("10.0.0.0/8", "203.0.113.0/24"), "203.0.113.7", true},
	}
	for _, tc := range allows {
		k := KeyRecord{AllowedIPs: tc.allowed}
		if got := k.AllowsIP(netip.MustParseAddr(tc.ip)); got != tc.want {
			t.Errorf("AllowsIP(%s: %s) = %v, want %v", tc.name, tc.ip, got, tc.want)
		}
	}
	if (KeyRecord{AllowedIPs: prefixes("10.0.0.0/8")}).AllowsIP(netip.Addr{}) {
		t.Error("an invalid address passed a non-empty allowlist")
	}
}

func TestPrincipalHas(t *testing.T) {
	t.Parallel()
	p := Principal{Scopes: []Scope{ScopeDeliveriesRead, ScopeWebhooksWrite}}
	if !p.Has(ScopeDeliveriesRead) || !p.Has(ScopeWebhooksWrite) {
		t.Error("Has is false for a granted scope")
	}
	if p.Has(ScopeDeliveriesWrite) || p.Has(ScopeCouriersWrite) {
		t.Error("Has is true for a scope that was not granted")
	}
	if (Principal{}).Has(ScopeDeliveriesRead) {
		t.Error("an empty principal has a scope")
	}
}

func TestVocabulary(t *testing.T) {
	t.Parallel()
	want := []Scope{"deliveries:read", "deliveries:write", "couriers:write", "webhooks:write"}
	if len(AllScopes) != len(want) {
		t.Fatalf("AllScopes = %v", AllScopes)
	}
	for i, s := range want {
		if AllScopes[i] != s {
			t.Errorf("AllScopes[%d] = %q, want %q", i, AllScopes[i], s)
		}
	}
	if CredentialSecretKey != "secret_key" || EnvTest != "test" || EnvLive != "live" {
		t.Error("credential type or environment constants changed")
	}
	if !EnvTest.Valid() || !EnvLive.Valid() || Environment("prod").Valid() {
		t.Error("Environment.Valid is wrong")
	}
}
