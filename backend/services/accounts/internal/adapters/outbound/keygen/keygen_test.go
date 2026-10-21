package keygen_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/raduolivas/dt/backend/pkg/apikey"
	"github.com/raduolivas/dt/backend/services/accounts/internal/adapters/outbound/keygen"
	"github.com/raduolivas/dt/backend/services/accounts/internal/domain"
)

var keyRegex = regexp.MustCompile(`^dt_sk_(test|live)_[0-9A-Za-z]{55}$`)

func TestGenerate(t *testing.T) {
	pepper := []byte("test-only-pepper-0123456789abcdef")
	g := keygen.New(pepper)
	for _, env := range []domain.Environment{domain.EnvTest, domain.EnvLive} {
		plain, lookup, prefix, hash, err := g.Generate(env)
		if err != nil {
			t.Fatal(err)
		}
		if !keyRegex.MatchString(plain) || !strings.HasPrefix(plain, "dt_sk_"+string(env)+"_") {
			t.Fatalf("plaintext %q has the wrong format", plain)
		}
		if prefix != "dt_sk_"+string(env)+"_"+lookup || !strings.HasPrefix(plain, prefix) || len(lookup) != 12 {
			t.Fatalf("prefix %q / lookup %q", prefix, lookup)
		}
		if !apikey.Verify(pepper, plain, hash) {
			t.Fatal("hash is not HMAC-SHA256(pepper, plaintext)")
		}
	}
	if _, _, _, _, err := g.Generate(domain.Environment("staging")); err == nil {
		t.Fatal("unknown environment accepted")
	}
}
