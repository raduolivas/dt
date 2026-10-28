// Package viewerjwt signs and verifies the customer viewer tokens: short-lived
// EdDSA (Ed25519) JWTs the gateway issues in exchange for a tracking link
// token, and the tracking service verifies against the gateway's JWKS.
//
// Only EdDSA is ever accepted: "none", HMAC and RSA tokens are refused
// whatever key they name.
package viewerjwt

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Token constants shared by the signer and the verifiers.
const (
	// Issuer is the iss of every viewer token.
	Issuer = "dt-gateway"
	// Audience is the aud of every viewer token.
	Audience = "viewer"
	// TTL is a viewer token's lifetime.
	TTL = 15 * time.Minute
	// Leeway is the clock skew allowed on exp.
	Leeway = 30 * time.Second
)

// Claims are what a viewer token binds: the delivery, its tenant and
// environment ("test" or "live"), and the tracking link it was exchanged for
// (so rotating the link revokes the token).
type Claims struct {
	DeliveryID, TenantID, Env, LinkID string
}

var idPattern = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

// Signer signs viewer tokens with its current key and publishes the public
// half of every configured key.
type Signer struct {
	current string
	keys    map[string]ed25519.PrivateKey
	public  map[string]ed25519.PublicKey
	jwks    []byte
}

// ParseKeys builds a Signer from spec (`id:base64(32-byte Ed25519 seed)`,
// comma-separated) and the id of the key that signs new tokens. Older keys
// stay in the JWKS so tokens they signed keep verifying until they expire.
// Errors never contain key material; keys are referenced by position or id.
func ParseKeys(spec, current string) (*Signer, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, errors.New("viewerjwt: no keys configured")
	}
	if current == "" {
		return nil, errors.New("viewerjwt: current key id is required")
	}
	if !idPattern.MatchString(current) {
		// Never echo the value: an operator may have pasted key material here.
		return nil, fmt.Errorf("viewerjwt: current key id must match %s", idPattern)
	}
	s := &Signer{current: current, keys: map[string]ed25519.PrivateKey{}, public: map[string]ed25519.PublicKey{}}
	var ids []string
	for i, entry := range strings.Split(spec, ",") {
		id, enc, ok := strings.Cut(strings.TrimSpace(entry), ":")
		if !ok {
			return nil, fmt.Errorf("viewerjwt: key #%d: expected id:base64", i+1)
		}
		if !idPattern.MatchString(id) {
			return nil, fmt.Errorf("viewerjwt: key #%d: id must match %s", i+1, idPattern)
		}
		if _, dup := s.keys[id]; dup {
			return nil, fmt.Errorf("viewerjwt: key %q: duplicate id", id)
		}
		raw, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			return nil, fmt.Errorf("viewerjwt: key %q: not valid base64", id)
		}
		if len(raw) != ed25519.SeedSize {
			return nil, fmt.Errorf("viewerjwt: key %q: must decode to a %d-byte seed", id, ed25519.SeedSize)
		}
		priv := ed25519.NewKeyFromSeed(raw)
		clear(raw)
		s.keys[id] = priv
		s.public[id] = priv.Public().(ed25519.PublicKey)
		ids = append(ids, id)
	}
	if _, ok := s.keys[current]; !ok {
		return nil, fmt.Errorf("viewerjwt: current key id %q is not in the key set", current)
	}
	set := jwkSet{Keys: make([]jwk, 0, len(ids))}
	for _, id := range ids {
		set.Keys = append(set.Keys, jwk{Kty: "OKP", Crv: "Ed25519", Alg: "EdDSA", Use: "sig", Kid: id,
			X: base64.RawURLEncoding.EncodeToString(s.public[id])})
	}
	b, err := json.Marshal(set)
	if err != nil {
		return nil, fmt.Errorf("viewerjwt: jwks: %w", err)
	}
	s.jwks = b
	return s, nil
}

// JWKS is the JSON Web Key Set of every configured public key.
func (s *Signer) JWKS() []byte { return append([]byte(nil), s.jwks...) }

// Verifier verifies tokens against the signer's own public keys, without
// fetching anything (the gateway's refresh path).
func (s *Signer) Verifier() *Verifier {
	v := &Verifier{static: true, clock: time.Now}
	v.keys = make(map[string]ed25519.PublicKey, len(s.public))
	for id, k := range s.public {
		v.keys[id] = k
	}
	return v
}

// jwk is one Ed25519 public key (RFC 8037).
type jwk struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	Alg string `json:"alg,omitempty"`
	Use string `json:"use,omitempty"`
	Kid string `json:"kid"`
	X   string `json:"x"`
}

type jwkSet struct {
	Keys []jwk `json:"keys"`
}

// parseJWKS reads the Ed25519 signing keys of a JWKS document. Keys of other
// types, curves or algorithms are skipped.
func parseJWKS(b []byte) (map[string]ed25519.PublicKey, error) {
	var set jwkSet
	if err := json.Unmarshal(b, &set); err != nil {
		return nil, fmt.Errorf("viewerjwt: jwks: %w", err)
	}
	out := map[string]ed25519.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "OKP" || k.Crv != "Ed25519" || k.Kid == "" || (k.Alg != "" && k.Alg != "EdDSA") ||
			(k.Use != "" && k.Use != "sig") {
			continue
		}
		x, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil || len(x) != ed25519.PublicKeySize {
			continue
		}
		out[k.Kid] = ed25519.PublicKey(x)
	}
	return out, nil
}
