package viewerjwt

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// tokenClaims is the verified JWT payload: the registered claims (iss, aud,
// sub = the delivery id, iat, exp, jti) plus tid (tenant), env and lid
// (link). aud is accepted as a string or an array.
type tokenClaims struct {
	jwt.RegisteredClaims
	Tenant string `json:"tid"`
	Env    string `json:"env"`
	Link   string `json:"lid"`
}

// signedClaims is the payload Sign emits. It is its own type, not
// jwt.RegisteredClaims, so aud is the single string "viewer" without
// touching the library's package-wide MarshalSingleStringAsArray switch.
type signedClaims struct {
	Iss    string `json:"iss"`
	Aud    string `json:"aud"`
	Sub    string `json:"sub"`
	Tenant string `json:"tid"`
	Env    string `json:"env"`
	Link   string `json:"lid"`
	Iat    int64  `json:"iat"`
	Exp    int64  `json:"exp"`
	Jti    string `json:"jti"`
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
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, signedClaims{
		Iss: Issuer, Aud: Audience, Sub: c.DeliveryID, Tenant: c.TenantID, Env: c.Env, Link: c.LinkID,
		Iat: now.Unix(), Exp: exp.Unix(), Jti: base64.RawURLEncoding.EncodeToString(jti),
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
