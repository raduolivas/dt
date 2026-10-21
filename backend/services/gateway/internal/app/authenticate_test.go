package app_test

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raduolivas/dt/backend/pkg/apikey"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

var (
	now    = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	pepper = []byte("test-only-pepper-0123456789abcdef")
	client = netip.MustParseAddr("203.0.113.7")
)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

// keys is an in-memory KeyStore.
type keys struct {
	byLookup map[string]domain.KeyRecord
	err      error
}

func (k *keys) FindByLookup(_ context.Context, lookup string) (domain.KeyRecord, error) {
	if k.err != nil {
		return domain.KeyRecord{}, k.err
	}
	r, ok := k.byLookup[lookup]
	if !ok {
		return domain.KeyRecord{}, app.ErrNotFound
	}
	return r, nil
}

// countingHasher verifies with the real HMAC and counts calls.
type countingHasher struct {
	mu    sync.Mutex
	calls int
}

func (h *countingHasher) Verify(plaintext string, hash []byte) bool {
	h.mu.Lock()
	h.calls++
	h.mu.Unlock()
	return apikey.Verify(pepper, plaintext, hash)
}

func (h *countingHasher) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls
}

func at(t time.Time) *time.Time { return &t }

// newKey mints a key and its stored record.
func newKey(t *testing.T, env apikey.Environment, id string) (string, domain.KeyRecord) {
	t.Helper()
	k, err := apikey.Generate(env)
	if err != nil {
		t.Fatal(err)
	}
	return k.Plaintext, domain.KeyRecord{
		ID: id, TenantID: "ten_01", Lookup: k.Lookup, Env: domain.Environment(env),
		Hash: apikey.Hash(pepper, k.Plaintext), Scopes: []domain.Scope{domain.ScopeDeliveriesRead},
	}
}

func newAuth(recs ...domain.KeyRecord) (*app.Authenticator, *countingHasher) {
	m := map[string]domain.KeyRecord{}
	for _, r := range recs {
		m[r.Lookup] = r
	}
	h := &countingHasher{}
	return &app.Authenticator{Keys: &keys{byLookup: m}, Hasher: h, Clock: fixedClock{now}}, h
}

func TestAuthenticateSucceeds(t *testing.T) {
	t.Parallel()
	raw, rec := newKey(t, apikey.Live, "ak_ok")
	rec.Scopes = []domain.Scope{domain.ScopeDeliveriesRead, domain.ScopeWebhooksWrite}
	rec.ExpiresAt = at(now.Add(time.Hour))
	rec.AllowedIPs = []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}
	a, h := newAuth(rec)

	p, err := a.Authenticate(context.Background(), raw, client)
	if err != nil {
		t.Fatal(err)
	}
	want := domain.Principal{
		TenantID: "ten_01", CredentialID: "ak_ok", Env: domain.EnvLive, Credential: domain.CredentialSecretKey,
		Scopes: []domain.Scope{domain.ScopeDeliveriesRead, domain.ScopeWebhooksWrite},
	}
	if p.TenantID != want.TenantID || p.CredentialID != want.CredentialID || p.Env != want.Env ||
		p.Credential != want.Credential || !slices.Equal(p.Scopes, want.Scopes) {
		t.Fatalf("principal = %+v, want %+v", p, want)
	}
	if h.count() != 1 {
		t.Errorf("hasher calls = %d, want 1", h.count())
	}
	// The principal's scopes do not alias the stored record.
	p.Scopes[0] = "mutated"
	if rec.Scopes[0] != domain.ScopeDeliveriesRead {
		t.Error("principal scopes alias the key record")
	}
}

func TestAuthenticateFailuresAreIndistinguishable(t *testing.T) {
	t.Parallel()
	good, goodRec := newKey(t, apikey.Test, "ak_good")
	revoked, revokedRec := newKey(t, apikey.Test, "ak_revoked")
	revokedRec.RevokedAt = at(now.Add(-time.Minute))
	expired, expiredRec := newKey(t, apikey.Test, "ak_expired")
	expiredRec.ExpiresAt = at(now)
	mismatch, mismatchRec := newKey(t, apikey.Test, "ak_mismatch")
	mismatchRec.Env = domain.EnvLive // stored env differs from the parsed one
	unknown, _ := newKey(t, apikey.Test, "ak_unknown")
	allowlisted, allowlistedRec := newKey(t, apikey.Test, "ak_allowlisted")
	allowlistedRec.AllowedIPs = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}

	// Same env and lookup as the good key, different secret: a hash mismatch.
	wrongHash := good[:len(good)-1] + flip(good[len(good)-1])

	a, _ := newAuth(goodRec, revokedRec, expiredRec, mismatchRec, allowlistedRec)
	cases := []struct{ name, raw string }{
		{"empty", ""},
		{"malformed", "not a key"},
		{"malformed after the prefix", "dt_sk_test_short"},
		{"wrong env segment", strings.Replace(good, "dt_sk_test_", "dt_sk_prod_", 1)},
		{"unknown lookup", unknown},
		{"wrong hash", wrongHash},
		{"revoked", revoked},
		{"expired", expired},
		{"env mismatch", mismatch},
	}
	for _, tc := range cases {
		_, err := a.Authenticate(context.Background(), tc.raw, client)
		if !errors.Is(err, app.ErrUnauthenticated) {
			t.Errorf("%s: err = %v, want ErrUnauthenticated", tc.name, err)
			continue
		}
		if err != app.ErrUnauthenticated { //nolint:errorlint // the exact value is the point
			t.Errorf("%s: err = %#v, want the ErrUnauthenticated value itself", tc.name, err)
		}
	}

	// A valid key from outside its allowlist: only after the hash verifies.
	if _, err := a.Authenticate(context.Background(), allowlisted, client); err != app.ErrIPNotAllowed { //nolint:errorlint // exact value
		t.Errorf("outside allowlist: err = %v, want ErrIPNotAllowed", err)
	}
	if _, err := a.Authenticate(context.Background(), allowlisted, netip.MustParseAddr("10.1.2.3")); err != nil {
		t.Errorf("inside allowlist: err = %v", err)
	}
	// A wrong secret for an allowlisted key is unauthenticated, not ip_not_allowed.
	wrongAllowlisted := allowlisted[:len(allowlisted)-1] + flip(allowlisted[len(allowlisted)-1])
	if _, err := a.Authenticate(context.Background(), wrongAllowlisted, client); err != app.ErrUnauthenticated { //nolint:errorlint // exact value
		t.Errorf("wrong hash outside allowlist: err = %v, want ErrUnauthenticated", err)
	}
	// An expired key from outside its allowlist is unauthenticated too.
	expiredRec.AllowedIPs = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	b, _ := newAuth(expiredRec)
	if _, err := b.Authenticate(context.Background(), expired, client); err != app.ErrUnauthenticated { //nolint:errorlint // exact value
		t.Errorf("expired outside allowlist: err = %v, want ErrUnauthenticated", err)
	}
}

func TestAuthenticateUnknownLookupStillHashes(t *testing.T) {
	t.Parallel()
	known, rec := newKey(t, apikey.Test, "ak_known")
	unknown, _ := newKey(t, apikey.Test, "ak_unknown")
	cases := []struct {
		name string
		raw  string
		want int
	}{
		{"unknown lookup", unknown, 1},
		{"malformed after the prefix", "dt_sk_live_tooShort", 1},
		{"known lookup", known, 1},
		{"revoked", known, 1},
	}
	for _, tc := range cases {
		r := rec
		if tc.name == "revoked" {
			r.RevokedAt = at(now)
		}
		a, h := newAuth(r)
		_, _ = a.Authenticate(context.Background(), tc.raw, client)
		if got := h.count(); got != tc.want {
			t.Errorf("%s: hasher calls = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestAuthenticateStoreFailureIsNotUnauthenticated(t *testing.T) {
	t.Parallel()
	raw, _ := newKey(t, apikey.Test, "ak_1")
	boom := errors.New("connection refused")
	a := &app.Authenticator{Keys: &keys{err: boom}, Hasher: &countingHasher{}, Clock: fixedClock{now}}
	_, err := a.Authenticate(context.Background(), raw, client)
	if !errors.Is(err, boom) || errors.Is(err, app.ErrUnauthenticated) {
		t.Fatalf("err = %v, want the store error, not ErrUnauthenticated", err)
	}
}

// flip changes one base62 character to another.
func flip(c byte) string {
	if c == 'a' {
		return "b"
	}
	return "a"
}
