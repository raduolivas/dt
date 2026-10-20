package app

import (
	"context"
	"errors"
	"time"

	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

// ErrNotFound is returned by a KeyStore for an unknown lookup.
var ErrNotFound = errors.New("gateway: key not found")

// KeyStore reads the key read model.
type KeyStore interface {
	// FindByLookup returns the key with lookup, or ErrNotFound.
	FindByLookup(ctx context.Context, lookup string) (domain.KeyRecord, error)
}

// Hasher checks a plaintext key against its stored hash in constant time.
type Hasher interface {
	Verify(plaintext string, hash []byte) bool
}

// Clock abstracts time for tests.
type Clock interface{ Now() time.Time }
