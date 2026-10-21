package keyhash_test

import (
	"testing"

	"github.com/raduolivas/dt/backend/pkg/apikey"
	"github.com/raduolivas/dt/backend/services/gateway/internal/adapters/outbound/keyhash"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
)

func TestHasherVerifiesWithPepper(t *testing.T) {
	t.Parallel()
	pepper := []byte("test-only-pepper-0123456789abcdef")
	k, err := apikey.Generate(apikey.Test)
	if err != nil {
		t.Fatal(err)
	}
	hash := apikey.Hash(pepper, k.Plaintext)
	var h app.Hasher = keyhash.New(pepper)
	if !h.Verify(k.Plaintext, hash) {
		t.Error("Verify rejected the right key")
	}
	if h.Verify(k.Plaintext+"x", hash) {
		t.Error("Verify accepted a different key")
	}
	if keyhash.New([]byte("another-test-only-pepper-0123456")).Verify(k.Plaintext, hash) {
		t.Error("Verify accepted a hash made with another pepper")
	}
	// The hasher keeps its own copy of the pepper.
	pepper[0] ^= 0xff
	if !h.Verify(k.Plaintext, hash) {
		t.Error("mutating the caller's pepper changed the hasher")
	}
}
