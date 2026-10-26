package viewerjwt_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/raduolivas/dt/backend/pkg/viewerjwt"
)

var now = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func seed(t *testing.T) string {
	t.Helper()
	b := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func signer(t *testing.T, spec, current string) *viewerjwt.Signer {
	t.Helper()
	s, err := viewerjwt.ParseKeys(spec, current)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var claims = viewerjwt.Claims{
	DeliveryID: "del_01j9z3k8m2q4r6s8t0v2w4x6y8", TenantID: "ten_01j9z3k8m2q4r6s8t0v2w4x6y8",
	Env: "test", LinkID: "tl_01j9z3k8m2q4r6s8t0v2w4x6y8",
}

// jwksServer serves s.JWKS() (swappable) and counts fetches.
type jwksServer struct {
	mu    sync.Mutex
	body  []byte
	hits  atomic.Int32
	srv   *httptest.Server
	fails atomic.Bool
}

func newJWKS(t *testing.T, s *viewerjwt.Signer) *jwksServer {
	t.Helper()
	j := &jwksServer{body: s.JWKS()}
	j.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		j.hits.Add(1)
		if j.fails.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		j.mu.Lock()
		defer j.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(j.body)
	}))
	t.Cleanup(j.srv.Close)
	return j
}

func (j *jwksServer) set(s *viewerjwt.Signer) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.body = s.JWKS()
}

func TestSignVerifyRoundTrip(t *testing.T) {
	t.Parallel()
	s := signer(t, "v1:"+seed(t), "v1")
	tok, exp, err := s.Sign(claims, now)
	if err != nil {
		t.Fatal(err)
	}
	if !exp.Equal(now.Add(viewerjwt.TTL)) || viewerjwt.TTL != 15*time.Minute {
		t.Fatalf("exp = %v, want now + 15m", exp)
	}
	j := newJWKS(t, s)
	v := viewerjwt.NewVerifier(j.srv.URL, j.srv.Client())
	got, err := v.Verify(context.Background(), tok, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got != claims {
		t.Fatalf("claims = %+v, want %+v", got, claims)
	}
	// The local verifier needs no fetch.
	if got, err := s.Verifier().Verify(context.Background(), tok, now); err != nil || got != claims {
		t.Fatalf("local verify = %+v, %v", got, err)
	}

	// The header names the key and EdDSA; the payload carries aud and iss.
	parsed, _, err := jwt.NewParser().ParseUnverified(tok, jwt.MapClaims{})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Header["alg"] != "EdDSA" || parsed.Header["kid"] != "v1" {
		t.Fatalf("header = %v", parsed.Header)
	}
	mc := parsed.Claims.(jwt.MapClaims)
	if aud, _ := mc.GetAudience(); len(aud) != 1 || aud[0] != viewerjwt.Audience {
		t.Fatalf("aud = %v", aud)
	}
	if iss, _ := mc.GetIssuer(); iss != viewerjwt.Issuer {
		t.Fatalf("iss = %v", iss)
	}

	// Within the 30 s leeway after exp the token still verifies; past it, not.
	if _, err := v.Verify(context.Background(), tok, exp.Add(29*time.Second)); err != nil {
		t.Fatalf("within leeway: %v", err)
	}
	if _, err := v.Verify(context.Background(), tok, exp.Add(31*time.Second)); !errors.Is(err, viewerjwt.ErrInvalid) {
		t.Fatalf("past leeway: %v", err)
	}
}

// forge signs arbitrary claims with key under kid and alg EdDSA.
func forge(t *testing.T, key ed25519.PrivateKey, kid string, c jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, c)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestVerifyRejectsWrongAudAlgNoneAndExpired(t *testing.T) {
	t.Parallel()
	sd := seed(t)
	s := signer(t, "v1:"+sd, "v1")
	raw, _ := base64.StdEncoding.DecodeString(sd)
	key := ed25519.NewKeyFromSeed(raw)
	j := newJWKS(t, s)
	v := viewerjwt.NewVerifier(j.srv.URL, j.srv.Client())

	good := func() jwt.MapClaims {
		return jwt.MapClaims{
			"iss": viewerjwt.Issuer, "aud": viewerjwt.Audience, "sub": claims.DeliveryID,
			"ten": claims.TenantID, "env": claims.Env, "lid": claims.LinkID,
			"iat": now.Unix(), "exp": now.Add(15 * time.Minute).Unix(),
		}
	}
	with := func(k string, val any) jwt.MapClaims {
		c := good()
		if val == nil {
			delete(c, k)
		} else {
			c[k] = val
		}
		return c
	}
	// The forged baseline verifies, so each case below fails for its own reason.
	if _, err := v.Verify(context.Background(), forge(t, key, "v1", good()), now); err != nil {
		t.Fatalf("baseline: %v", err)
	}

	none, err := jwt.NewWithClaims(jwt.SigningMethodNone, good()).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	hs := jwt.NewWithClaims(jwt.SigningMethodHS256, good())
	hs.Header["kid"] = "v1"
	// HS256 keyed with the public key bytes: the classic alg-confusion attack.
	hsTok, err := hs.SignedString([]byte(key.Public().(ed25519.PublicKey)))
	if err != nil {
		t.Fatal(err)
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)

	cases := map[string]string{
		"wrong aud":        forge(t, key, "v1", with("aud", "partner")),
		"no aud":           forge(t, key, "v1", with("aud", nil)),
		"wrong iss":        forge(t, key, "v1", with("iss", "someone-else")),
		"expired":          forge(t, key, "v1", with("exp", now.Add(-time.Minute).Unix())),
		"no exp":           forge(t, key, "v1", with("exp", nil)),
		"no lid":           forge(t, key, "v1", with("lid", nil)),
		"no sub":           forge(t, key, "v1", with("sub", nil)),
		"alg none":         none,
		"alg HS256":        hsTok,
		"other key":        forge(t, other, "v1", good()),
		"unknown kid":      forge(t, key, "v9", good()),
		"no kid":           forge(t, key, "", good()),
		"garbage":          "not.a.jwt",
		"empty":            "",
		"secret key value": "dt_sk_test_abcdefghijklmnopqrstuvwxyz012345",
	}
	for name, tok := range cases {
		if _, err := v.Verify(context.Background(), tok, now); !errors.Is(err, viewerjwt.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestVerifierRefetchesUnknownKid(t *testing.T) {
	t.Parallel()
	s1, s2 := seed(t), seed(t)
	old := signer(t, "v1:"+s1, "v1")
	rotated := signer(t, "v1:"+s1+",v2:"+s2, "v2")
	j := newJWKS(t, old)

	clock := now
	v := viewerjwt.NewVerifier(j.srv.URL, j.srv.Client())
	v.SetClock(func() time.Time { return clock })

	tok1, _, _ := old.Sign(claims, now)
	if _, err := v.Verify(context.Background(), tok1, now); err != nil {
		t.Fatal(err)
	}
	if n := j.hits.Load(); n != 1 {
		t.Fatalf("fetches = %d, want 1", n)
	}
	// Cached: no refetch for a known kid.
	if _, err := v.Verify(context.Background(), tok1, now); err != nil || j.hits.Load() != 1 {
		t.Fatalf("cached verify: %v, fetches %d", err, j.hits.Load())
	}

	// The gateway rotates to v2. A v2 token makes the verifier refetch.
	j.set(rotated)
	tok2, _, _ := rotated.Sign(claims, now)
	clock = now.Add(11 * time.Second)
	if _, err := v.Verify(context.Background(), tok2, now); err != nil {
		t.Fatalf("after rotation: %v", err)
	}
	if n := j.hits.Load(); n != 2 {
		t.Fatalf("fetches = %d, want 2", n)
	}

	// Unknown kids refetch at most once per 10 s.
	_, junkKey, _ := ed25519.GenerateKey(rand.Reader)
	junk := forge(t, junkKey, "nope", jwt.MapClaims{"aud": viewerjwt.Audience})
	for range 5 {
		if _, err := v.Verify(context.Background(), junk, now); !errors.Is(err, viewerjwt.ErrInvalid) {
			t.Fatalf("junk kid: %v", err)
		}
	}
	if n := j.hits.Load(); n != 2 {
		t.Fatalf("fetches after junk within 10 s = %d, want 2", n)
	}
	clock = clock.Add(10*time.Second + time.Millisecond)
	_, _ = v.Verify(context.Background(), junk, now)
	if n := j.hits.Load(); n != 3 {
		t.Fatalf("fetches after 10 s = %d, want 3", n)
	}

	// The cache lasts 5 minutes; then a known kid is fetched again.
	clock = clock.Add(5*time.Minute + time.Second)
	if _, err := v.Verify(context.Background(), tok2, now); err != nil {
		t.Fatal(err)
	}
	if n := j.hits.Load(); n != 4 {
		t.Fatalf("fetches after cache expiry = %d, want 4", n)
	}

	// A failing JWKS endpoint keeps the last good keys (fail safe, not open:
	// only already-known keys verify).
	j.fails.Store(true)
	clock = clock.Add(6 * time.Minute)
	if _, err := v.Verify(context.Background(), tok2, now); err != nil {
		t.Fatalf("stale keys while JWKS is down: %v", err)
	}
}

func TestVerifierWithoutKeysFailsClosed(t *testing.T) {
	t.Parallel()
	s := signer(t, "v1:"+seed(t), "v1")
	j := newJWKS(t, s)
	j.fails.Store(true)
	v := viewerjwt.NewVerifier(j.srv.URL, j.srv.Client())
	tok, _, _ := s.Sign(claims, now)
	if _, err := v.Verify(context.Background(), tok, now); err == nil {
		t.Fatal("verified without any keys")
	} else if errors.Is(err, viewerjwt.ErrInvalid) {
		t.Fatal("an unreachable JWKS must not look like a bad token")
	}
}

func TestParseKeysErrorsNeverEchoKeyMaterial(t *testing.T) {
	t.Parallel()
	sd := seed(t)
	cases := map[string][2]string{
		"empty":           {"", "v1"},
		"no current":      {"v1:" + sd, ""},
		"current missing": {"v1:" + sd, "v2"},
		"bad entry":       {sd, "v1"},
		"bad id":          {"V 1:" + sd, "v1"},
		"bad base64":      {"v1:" + sd + "!!", "v1"},
		"short seed":      {"v1:" + base64.StdEncoding.EncodeToString([]byte("short")), "v1"},
		"duplicate":       {"v1:" + sd + ",v1:" + sd, "v1"},
		"current is key":  {"v1:" + sd, sd},
	}
	for name, c := range cases {
		_, err := viewerjwt.ParseKeys(c[0], c[1])
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		if strings.Contains(err.Error(), sd) || strings.Contains(err.Error(), sd[:16]) {
			t.Errorf("%s: error echoes key material: %v", name, err)
		}
	}
}

func TestJWKSPublishesEveryPublicKeyOnly(t *testing.T) {
	t.Parallel()
	s1, s2 := seed(t), seed(t)
	s := signer(t, "v1:"+s1+", v2:"+s2, "v2")
	body := string(s.JWKS())
	for _, want := range []string{`"kid":"v1"`, `"kid":"v2"`, `"kty":"OKP"`, `"crv":"Ed25519"`, `"alg":"EdDSA"`, `"use":"sig"`} {
		if !strings.Contains(body, want) {
			t.Errorf("JWKS lacks %s: %s", want, body)
		}
	}
	for _, sd := range []string{s1, s2} {
		raw, _ := base64.StdEncoding.DecodeString(sd)
		if strings.Contains(body, base64.RawURLEncoding.EncodeToString(raw)) || strings.Contains(body, `"d"`) {
			t.Fatalf("JWKS leaks private key material: %s", body)
		}
	}
	tok, _, _ := s.Sign(claims, now)
	parsed, _, _ := jwt.NewParser().ParseUnverified(tok, jwt.MapClaims{})
	if parsed.Header["kid"] != "v2" {
		t.Fatalf("signed with kid %v, want the current v2", parsed.Header["kid"])
	}
}
