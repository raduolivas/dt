// Package courierjwt signs and verifies courier access tokens: short-lived
// EdDSA (Ed25519) JWTs the gateway issues to a courier session and later
// verifies itself against its own public keys. They are never published in
// the JWKS, and a viewer token is never accepted here (aud differs).
//
// Only EdDSA is ever accepted: "none", HMAC and RSA tokens are refused
// whatever key they name.
package courierjwt

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Token constants shared by Sign and Verify.
const (
	// Issuer is the iss of every courier token.
	Issuer = "dt-gateway"
	// Audience is the aud of every courier token.
	Audience = "courier"
	// Scope is the scp of every courier token.
	Scope = "location:write status:write"
	// TTL is a courier token's lifetime.
	TTL = 15 * time.Minute
	// Leeway is the clock skew allowed on exp.
	Leeway = 30 * time.Second
)

// ErrInvalid covers every token that must be refused: malformed, wrong
// algorithm, unknown key, bad signature, wrong audience, issuer or scope,
// expired, or missing claims.
var ErrInvalid = errors.New("courierjwt: invalid token")

// Claims are what a courier token binds: the courier (sub), its tenant and
// environment ("test" or "live"), the courier session, and how the session
// started (Source "sdk" or "link").
type Claims struct {
	CourierID, TenantID, Env, SessionID, Source string
}

var idPattern = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

// Signer signs courier tokens with its current key and verifies tokens
// signed by any configured key.
type Signer struct {
	current string
	keys    map[string]ed25519.PrivateKey
	public  map[string]ed25519.PublicKey
}

// ParseKeys builds a Signer from spec (`id:base64(32-byte Ed25519 seed)`,
// comma-separated, the same format as viewerjwt) and the id of the key that
// signs new tokens. Older keys stay configured so tokens they signed keep
// verifying until they expire. Errors never contain key material; keys are
// referenced by position or id.
func ParseKeys(spec, current string) (*Signer, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, errors.New("courierjwt: no keys configured")
	}
	if current == "" {
		return nil, errors.New("courierjwt: current key id is required")
	}
	if !idPattern.MatchString(current) {
		// Never echo the value: an operator may have pasted key material here.
		return nil, fmt.Errorf("courierjwt: current key id must match %s", idPattern)
	}
	s := &Signer{current: current, keys: map[string]ed25519.PrivateKey{}, public: map[string]ed25519.PublicKey{}}
	for i, entry := range strings.Split(spec, ",") {
		id, enc, ok := strings.Cut(strings.TrimSpace(entry), ":")
		if !ok {
			return nil, fmt.Errorf("courierjwt: key #%d: expected id:base64", i+1)
		}
		if !idPattern.MatchString(id) {
			return nil, fmt.Errorf("courierjwt: key #%d: id must match %s", i+1, idPattern)
		}
		if _, dup := s.keys[id]; dup {
			return nil, fmt.Errorf("courierjwt: key %q: duplicate id", id)
		}
		raw, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			return nil, fmt.Errorf("courierjwt: key %q: not valid base64", id)
		}
		if len(raw) != ed25519.SeedSize {
			return nil, fmt.Errorf("courierjwt: key %q: must decode to a %d-byte seed", id, ed25519.SeedSize)
		}
		priv := ed25519.NewKeyFromSeed(raw)
		clear(raw)
		s.keys[id] = priv
		s.public[id] = priv.Public().(ed25519.PublicKey)
	}
	if _, ok := s.keys[current]; !ok {
		return nil, fmt.Errorf("courierjwt: current key id %q is not in the key set", current)
	}
	return s, nil
}

// tokenClaims is the verified JWT payload. aud is accepted as a string or an
// array.
type tokenClaims struct {
	jwt.RegisteredClaims
	Tenant  string `json:"tid"`
	Env     string `json:"env"`
	Session string `json:"sid"`
	Source  string `json:"src"`
	Scope   string `json:"scp"`
}

// signedClaims is the payload Sign emits. It is its own type, not
// jwt.RegisteredClaims, so aud is the single string "courier" without
// touching the library's package-wide MarshalSingleStringAsArray switch.
type signedClaims struct {
	Iss     string `json:"iss"`
	Aud     string `json:"aud"`
	Sub     string `json:"sub"`
	Tenant  string `json:"tid"`
	Env     string `json:"env"`
	Session string `json:"sid"`
	Source  string `json:"src"`
	Scope   string `json:"scp"`
	Iat     int64  `json:"iat"`
	Exp     int64  `json:"exp"`
	Jti     string `json:"jti"`
}

// signedClaims implements jwt.Claims (used only to sign; never validated).
func (c signedClaims) GetExpirationTime() (*jwt.NumericDate, error) {
	return jwt.NewNumericDate(time.Unix(c.Exp, 0)), nil
}
func (c signedClaims) GetIssuedAt() (*jwt.NumericDate, error) {
	return jwt.NewNumericDate(time.Unix(c.Iat, 0)), nil
}
func (signedClaims) GetNotBefore() (*jwt.NumericDate, error) { return nil, nil }
func (c signedClaims) GetIssuer() (string, error)            { return c.Iss, nil }
func (c signedClaims) GetSubject() (string, error)           { return c.Sub, nil }
func (c signedClaims) GetAudience() (jwt.ClaimStrings, error) {
	return jwt.ClaimStrings{c.Aud}, nil
}

// Sign issues a courier token for c, valid for TTL from now, signed with the
// current key (its id in the kid header). It returns the token and its
// expiry.
func (s *Signer) Sign(c Claims, now time.Time) (string, time.Time, error) {
	return s.sign(c, now, now.Add(TTL))
}

// SignUntil is Sign with exp capped at notAfter: a token for a session that
// ends sooner than TTL expires with the session. A notAfter that is not after
// now is an error.
func (s *Signer) SignUntil(c Claims, now, notAfter time.Time) (string, time.Time, error) {
	if !notAfter.After(now) {
		return "", time.Time{}, errors.New("courierjwt: not-after bound is not after now")
	}
	exp := now.Add(TTL)
	if notAfter.Before(exp) {
		exp = notAfter
	}
	return s.sign(c, now, exp)
}

func (s *Signer) sign(c Claims, now, exp time.Time) (string, time.Time, error) {
	if err := c.check(); err != nil {
		return "", time.Time{}, err
	}
	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return "", time.Time{}, fmt.Errorf("courierjwt: jti: %w", err)
	}
	// JWT times have second precision; exp is what the token says.
	exp = exp.Truncate(time.Second)
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, signedClaims{
		Iss: Issuer, Aud: Audience, Sub: c.CourierID, Tenant: c.TenantID, Env: c.Env,
		Session: c.SessionID, Source: c.Source, Scope: Scope,
		Iat: now.Unix(), Exp: exp.Unix(), Jti: base64.RawURLEncoding.EncodeToString(jti),
	})
	tok.Header["kid"] = s.current
	signed, err := tok.SignedString(s.keys[s.current])
	if err != nil {
		return "", time.Time{}, fmt.Errorf("courierjwt: sign: %w", err)
	}
	return signed, exp, nil
}

// Verify checks token at now against the signer's own public keys and
// returns its claims. It accepts only EdDSA tokens signed by a configured
// key, with aud exactly "courier", iss "dt-gateway", the courier scope, an
// exp no more than 30 s in the past and every claim present. Any refusal is
// ErrInvalid.
func (s *Signer) Verify(token string, now time.Time) (Claims, error) {
	var tc tokenClaims
	_, err := jwt.ParseWithClaims(token, &tc, func(t *jwt.Token) (any, error) {
		kid, ok := t.Header["kid"].(string)
		if !ok || kid == "" {
			return nil, errors.New("no kid")
		}
		k, ok := s.public[kid]
		if !ok {
			return nil, errors.New("unknown kid")
		}
		return k, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}),
		jwt.WithAudience(Audience),
		jwt.WithIssuer(Issuer),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(Leeway),
		jwt.WithTimeFunc(func() time.Time { return now }),
	)
	if err != nil {
		return Claims{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	// The audience must be exactly "courier" (a string or a one-element
	// array): a token also meant for another audience is refused.
	if len(tc.Audience) != 1 || tc.Audience[0] != Audience {
		return Claims{}, fmt.Errorf("%w: audience is not exactly %q", ErrInvalid, Audience)
	}
	if tc.Scope != Scope {
		return Claims{}, fmt.Errorf("%w: scope is not %q", ErrInvalid, Scope)
	}
	c := Claims{CourierID: tc.Subject, TenantID: tc.Tenant, Env: tc.Env, SessionID: tc.Session, Source: tc.Source}
	if err := c.check(); err != nil {
		return Claims{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return c, nil
}

// check reports whether c names a courier, tenant, session, source (sdk or
// link) and environment (test or live).
func (c Claims) check() error {
	if c.CourierID == "" || c.TenantID == "" || c.SessionID == "" ||
		(c.Env != "test" && c.Env != "live") || (c.Source != "sdk" && c.Source != "link") {
		return errors.New("courierjwt: claims need a courier, tenant, session, environment test or live, and source sdk or link")
	}
	return nil
}
