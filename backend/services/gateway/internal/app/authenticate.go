package app

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"

	"github.com/raduolivas/dt/backend/pkg/apikey"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

var (
	// ErrUnauthenticated is the one answer to every bad credential: malformed,
	// unknown, wrong secret, revoked, expired or in the wrong environment.
	// It is returned unwrapped so callers cannot tell the cases apart.
	ErrUnauthenticated = errors.New("gateway: unauthenticated")
	// ErrIPNotAllowed means a valid key was used from outside its allowlist.
	ErrIPNotAllowed = errors.New("gateway: ip not allowed")
)

// dummyHash is verified against when there is no stored hash, so a request
// costs one HMAC whether or not its lookup exists.
var dummyHash = make([]byte, 32)

// Authenticator authenticates partner secret keys against the read model.
type Authenticator struct {
	Keys   KeyStore
	Hasher Hasher
	Clock  Clock
}

// Authenticate resolves raw (a dt_sk_… key) used from ip to a principal. It
// returns ErrUnauthenticated for any bad credential and ErrIPNotAllowed for a
// valid key outside its allowlist; any other error means the key store failed.
func (a *Authenticator) Authenticate(ctx context.Context, raw string, ip netip.Addr) (domain.Principal, error) {
	env, lookup, err := apikey.Parse(raw)
	if err != nil {
		if apikey.LooksLikeSecretKey(raw) {
			a.Hasher.Verify(raw, dummyHash)
		}
		return domain.Principal{}, ErrUnauthenticated
	}
	rec, err := a.Keys.FindByLookup(ctx, lookup)
	if errors.Is(err, ErrNotFound) {
		a.Hasher.Verify(raw, dummyHash)
		return domain.Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return domain.Principal{}, fmt.Errorf("gateway: find key: %w", err)
	}
	// Verify first, then check state, so every outcome costs the same HMAC.
	ok := a.Hasher.Verify(raw, rec.Hash)
	if !ok || domain.Environment(env) != rec.Env || !rec.Usable(a.Clock.Now()) {
		return domain.Principal{}, ErrUnauthenticated
	}
	if !rec.AllowsIP(ip) {
		return domain.Principal{}, ErrIPNotAllowed
	}
	return domain.Principal{
		TenantID:     rec.TenantID,
		CredentialID: rec.ID,
		Env:          rec.Env,
		Credential:   domain.CredentialSecretKey,
		Scopes:       slices.Clone(rec.Scopes),
	}, nil
}
