package viewerjwt

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// tokenClaims is the JWT payload: the registered claims (iss, aud, sub = the
// delivery id, iat, exp, jti) plus the tenant, environment and link id.
type tokenClaims struct {
	jwt.RegisteredClaims
	Tenant string `json:"ten"`
	Env    string `json:"env"`
	Link   string `json:"lid"`
}

// Sign issues a viewer token for c, valid for TTL from now, signed with the
// current key (its id in the kid header). It returns the token and its
// expiry.
func (s *Signer) Sign(c Claims, now time.Time) (string, time.Time, error) {
	if err := c.check(); err != nil {
		return "", time.Time{}, err
	}
	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return "", time.Time{}, fmt.Errorf("viewerjwt: jti: %w", err)
	}
	// JWT times have second precision; exp is what the token says.
	exp := now.Add(TTL).Truncate(time.Second)
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, tokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: Issuer, Audience: jwt.ClaimStrings{Audience}, Subject: c.DeliveryID,
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(exp),
			ID: base64.RawURLEncoding.EncodeToString(jti),
		},
		Tenant: c.TenantID, Env: c.Env, Link: c.LinkID,
	})
	tok.Header["kid"] = s.current
	signed, err := tok.SignedString(s.keys[s.current])
	if err != nil {
		return "", time.Time{}, fmt.Errorf("viewerjwt: sign: %w", err)
	}
	return signed, exp, nil
}

// check reports whether c names a delivery, tenant, environment and link.
func (c Claims) check() error {
	if c.DeliveryID == "" || c.TenantID == "" || c.LinkID == "" || (c.Env != "test" && c.Env != "live") {
		return errors.New("viewerjwt: claims need a delivery, tenant, link and environment test or live")
	}
	return nil
}
