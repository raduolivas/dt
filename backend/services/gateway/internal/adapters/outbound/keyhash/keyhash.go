// Package keyhash implements app.Hasher with pkg/apikey: HMAC-SHA256 under
// the pepper shared with accounts, compared in constant time.
package keyhash

import (
	"slices"

	"github.com/raduolivas/dt/backend/pkg/apikey"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
)

// Hasher verifies keys against their stored hash. The pepper is never logged
// or exposed.
type Hasher struct{ pepper []byte }

var (
	_ app.Hasher     = (*Hasher)(nil)
	_ app.LinkHasher = (*Hasher)(nil)
)

// New returns a Hasher for pepper (validate it with apikey.LoadPepper).
func New(pepper []byte) *Hasher { return &Hasher{pepper: slices.Clone(pepper)} }

// Verify reports whether HMAC-SHA256(pepper, plaintext) equals hash.
func (h *Hasher) Verify(plaintext string, hash []byte) bool {
	return apikey.Verify(h.pepper, plaintext, hash)
}

// Hash returns HMAC-SHA256(pepper, plaintext): the stored form of a tracking
// link token.
func (h *Hasher) Hash(plaintext string) []byte {
	return apikey.Hash(h.pepper, plaintext)
}
