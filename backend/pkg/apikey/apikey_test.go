package apikey_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/raduolivas/dt/backend/pkg/apikey"
)

func TestGenerateMatchesFormat(t *testing.T) {
	re := regexp.MustCompile(`^dt_sk_(test|live)_[0-9A-Za-z]{55}$`)
	k, err := apikey.Generate(apikey.Live)
	if err != nil {
		t.Fatal(err)
	}
	if !re.MatchString(k.Plaintext) || k.DisplayPrefix != "dt_sk_live_"+k.Lookup || len(k.Lookup) != 12 || k.Env != apikey.Live {
		t.Fatal(k.DisplayPrefix)
	}
	env, lookup, err := apikey.Parse(k.Plaintext)
	if err != nil || env != apikey.Live || lookup != k.Lookup {
		t.Fatal(err)
	}
}

func TestGenerateRejectsUnknownEnvironment(t *testing.T) {
	if _, err := apikey.Generate("prod"); err == nil {
		t.Fatal("unknown environment accepted")
	}
}

func TestParseRejects(t *testing.T) {
	for _, s := range []string{"", "dt_sk_prod_" + strings.Repeat("a", 55), "dt_sk_test_" + strings.Repeat("a", 54),
		"dt_sk_test_" + strings.Repeat("a", 54) + "!", " dt_sk_test_" + strings.Repeat("a", 55),
		"dt_sk_test_" + strings.Repeat("a", 55) + "\n"} {
		if _, _, err := apikey.Parse(s); !errors.Is(err, apikey.ErrMalformed) {
			t.Errorf("%q accepted", s)
		}
	}
}

func TestVerify(t *testing.T) {
	pepper := bytes.Repeat([]byte{7}, 32)
	k, _ := apikey.Generate(apikey.Test)
	h := apikey.Hash(pepper, k.Plaintext)
	// Change the last character to a different one (a fixed "x" equals the key 1 time in ~60).
	other := "x"
	if strings.HasSuffix(k.Plaintext, "x") {
		other = "y"
	}
	if !apikey.Verify(pepper, k.Plaintext, h) || apikey.Verify(bytes.Repeat([]byte{8}, 32), k.Plaintext, h) ||
		apikey.Verify(pepper, k.Plaintext[:len(k.Plaintext)-1]+other, h) {
		t.Fatal("verify")
	}
}

func TestLoadPepperRequires32Bytes(t *testing.T) {
	if _, err := apikey.LoadPepper(base64.StdEncoding.EncodeToString(make([]byte, 31))); err == nil {
		t.Fatal("short pepper accepted")
	}
	if _, err := apikey.LoadPepper("not base64!"); err == nil {
		t.Fatal("invalid base64 accepted")
	}
	p, err := apikey.LoadPepper(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil || len(p) != 32 {
		t.Fatal(err)
	}
}

func TestLooksLikeSecretKey(t *testing.T) {
	if !apikey.LooksLikeSecretKey("dt_sk_test_abc") || apikey.LooksLikeSecretKey("dt_pk_test_abc") || apikey.LooksLikeSecretKey("") {
		t.Fatal("LooksLikeSecretKey")
	}
}

func TestGenerateIsUniformEnough(t *testing.T) {
	lookups := map[string]bool{}
	chars := map[rune]bool{}
	for range 2000 {
		k, err := apikey.Generate(apikey.Test)
		if err != nil {
			t.Fatal(err)
		}
		if lookups[k.Lookup] {
			t.Fatalf("duplicate lookup %s", k.Lookup)
		}
		lookups[k.Lookup] = true
		for _, c := range k.Plaintext[len("dt_sk_test_")+12:] {
			chars[c] = true
		}
	}
	if len(chars) != 62 {
		t.Fatalf("only %d distinct characters in secrets", len(chars))
	}
}
