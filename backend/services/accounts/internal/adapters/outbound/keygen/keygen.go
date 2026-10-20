// Package keygen implements app.KeyGenerator with pkg/apikey: keys come from
// crypto/rand and are hashed with HMAC-SHA256 under the shared pepper.
package keygen

import (
	"slices"

	"github.com/raduolivas/dt/backend/pkg/apikey"
	"github.com/raduolivas/dt/backend/services/accounts/internal/domain"
)

// Generator mints secret keys. The pepper is never logged or exposed.
type Generator struct{ pepper []byte }

// New returns a Generator for pepper (validate it with apikey.LoadPepper).
func New(pepper []byte) *Generator { return &Generator{pepper: slices.Clone(pepper)} }

// Generate returns a new key's plaintext, lookup, display prefix and hash.
func (g *Generator) Generate(env domain.Environment) (plaintext, lookup, displayPrefix string, hash []byte, err error) {
	k, err := apikey.Generate(apikey.Environment(env))
	if err != nil {
		return "", "", "", nil, err
	}
	return k.Plaintext, k.Lookup, k.DisplayPrefix, apikey.Hash(g.pepper, k.Plaintext), nil
}
