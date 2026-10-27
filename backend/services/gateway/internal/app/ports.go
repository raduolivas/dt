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

// Limit is a GCRA rate limit: Rate requests per Period sustained, with room
// for Burst requests at once.
type Limit struct {
	Rate   int
	Period time.Duration
	Burst  int
}

// Decision is a Limiter's answer for one request.
type Decision struct {
	Allowed bool
	// Limit is the burst the caller was measured against (RateLimit-Limit).
	Limit int
	// Remaining is how many more requests would be allowed right now.
	Remaining int
	// RetryAfter is how long until a denied request would be allowed; zero
	// when Allowed.
	RetryAfter time.Duration
	// ResetAfter is how long until the bucket is full again.
	ResetAfter time.Duration
}

// Limiter counts a request against the bucket named by key.
type Limiter interface {
	Allow(ctx context.Context, key string, l Limit) (Decision, error)
}

// Limits fixed by spec §5.7.
var (
	// PartnerKey is applied per secret key, keyed rl:sk:<credential_id>.
	PartnerKey = Limit{Rate: 50, Period: time.Second, Burst: 100}
	// FailedAuth is applied per client IP to failed authentications, keyed
	// rl:authfail:<client ip>.
	FailedAuth = Limit{Rate: 20, Period: time.Minute, Burst: 20}
	// RelayCourier is applied per courier to the location relay, keyed
	// rl:relay:cou:<courier id>, instead of PartnerKey.
	RelayCourier = Limit{Rate: 1, Period: time.Second, Burst: 5}
	// RelayTenant is the relay's ceiling per tenant and environment, keyed
	// rl:relay:ten:<tenant id>:<env>.
	RelayTenant = Limit{Rate: 500, Period: time.Second, Burst: 1000}
)
