package courierjwt_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/raduolivas/dt/backend/pkg/courierjwt"
	"github.com/raduolivas/dt/backend/pkg/viewerjwt"
)

var now = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

func seed(t *testing.T) string {
	t.Helper()
	b := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func signer(t *testing.T, spec, current string) *courierjwt.Signer {
	t.Helper()
	s, err := courierjwt.ParseKeys(spec, current)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var claims = courierjwt.Claims{
	CourierID: "cou_01j9z3k8m2q4r6s8t0v2w4x6y8", TenantID: "ten_01j9z3k8m2q4r6s8t0v2w4x6y8",
	Env: "test", SessionID: "cs_01j9z3k8m2q4r6s8t0v2w4x6y8", Source: "sdk",
}

func TestSignVerifyRoundTrip(t *testing.T) {
	s := signer(t, "k1:"+seed(t), "k1")
	tok, exp, err := s.Sign(claims, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(courierjwt.TTL); !exp.Equal(want) {
		t.Fatalf("exp = %v, want %v", exp, want)
	}
	got, err := s.Verify(tok, now)
	if err != nil {
		t.Fatal(err)
	}
	if got != claims {
		t.Fatalf("claims = %+v, want %+v", got, claims)
	}
	parsed, _, err := jwt.NewParser().ParseUnverified(tok, jwt.MapClaims{})
	if err != nil {
		t.Fatal(err)
	}
	m := parsed.Claims.(jwt.MapClaims)
	if m["scp"] != "location:write status:write" || m["aud"] != "courier" || m["iss"] != "dt-gateway" ||
		m["sub"] != claims.CourierID || m["src"] != "sdk" || m["jti"] == "" {
		t.Fatalf("unexpected payload %v", m)
	}
	if parsed.Header["kid"] != "k1" {
		t.Fatalf("kid = %v", parsed.Header["kid"])
	}
}

func TestVerifyRejectsViewerToken(t *testing.T) {
	sd := seed(t)
	vs, err := viewerjwt.ParseKeys("k1:"+sd, "k1")
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := vs.Sign(viewerjwt.Claims{DeliveryID: "del_1", TenantID: "ten_1", Env: "test", LinkID: "tl_1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signer(t, "k1:"+sd, "k1").Verify(tok, now); !errors.Is(err, courierjwt.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	s := signer(t, "k1:"+seed(t), "k1")
	tok, exp, err := s.Sign(claims, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(tok, exp.Add(29*time.Second)); err != nil {
		t.Fatalf("within leeway: %v", err)
	}
	if _, err := s.Verify(tok, exp.Add(31*time.Second)); !errors.Is(err, courierjwt.ErrInvalid) {
		t.Fatalf("past leeway: err = %v, want ErrInvalid", err)
	}
}

func TestVerifyRejectsNoneAndHS256(t *testing.T) {
	s := signer(t, "k1:"+seed(t), "k1")
	mk := func(m jwt.SigningMethod, key any) string {
		tok := jwt.NewWithClaims(m, jwt.MapClaims{
			"iss": "dt-gateway", "aud": "courier", "sub": claims.CourierID, "tid": claims.TenantID,
			"env": "test", "sid": claims.SessionID, "src": "sdk", "scp": "location:write status:write",
			"iat": now.Unix(), "exp": now.Add(time.Minute).Unix(), "jti": "x",
		})
		tok.Header["kid"] = "k1"
		out, err := tok.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	for name, tok := range map[string]string{
		"none":  mk(jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType),
		"HS256": mk(jwt.SigningMethodHS256, []byte("secret")),
	} {
		if _, err := s.Verify(tok, now); !errors.Is(err, courierjwt.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestVerifyRejectsUnknownKid(t *testing.T) {
	tok, _, err := signer(t, "k1:"+seed(t), "k1").Sign(claims, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signer(t, "k2:"+seed(t), "k2").Verify(tok, now); !errors.Is(err, courierjwt.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestOldKeyStillVerifies(t *testing.T) {
	old, cur := seed(t), seed(t)
	tok, _, err := signer(t, "k1:"+old, "k1").Sign(claims, now)
	if err != nil {
		t.Fatal(err)
	}
	rotated := signer(t, "k1:"+old+",k2:"+cur, "k2")
	if _, err := rotated.Verify(tok, now); err != nil {
		t.Fatalf("old key: %v", err)
	}
	newTok, _, err := rotated.Sign(claims, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rotated.Verify(newTok, now); err != nil {
		t.Fatalf("new key: %v", err)
	}
}

func TestClaimsCheck(t *testing.T) {
	s := signer(t, "k1:"+seed(t), "k1")
	cases := map[string]func(c *courierjwt.Claims){
		"empty courier": func(c *courierjwt.Claims) { c.CourierID = "" },
		"empty tenant":  func(c *courierjwt.Claims) { c.TenantID = "" },
		"empty session": func(c *courierjwt.Claims) { c.SessionID = "" },
		"bad env":       func(c *courierjwt.Claims) { c.Env = "prod" },
		"bad src":       func(c *courierjwt.Claims) { c.Source = "web" },
		"empty src":     func(c *courierjwt.Claims) { c.Source = "" },
	}
	for name, mutate := range cases {
		c := claims
		mutate(&c)
		if _, _, err := s.Sign(c, now); err == nil {
			t.Errorf("%s: Sign accepted", name)
		}
	}
	for _, src := range []string{"sdk", "link"} {
		c := claims
		c.Source = src
		if _, _, err := s.Sign(c, now); err != nil {
			t.Errorf("src %s: %v", src, err)
		}
	}
}
