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
	// rl:relay:cou:<tenant id>:<env>:<courier id>, instead of PartnerKey.
	RelayCourier = Limit{Rate: 1, Period: time.Second, Burst: 5}
	// RelayTenant is the relay's ceiling per tenant and environment, keyed
	// rl:relay:ten:<tenant id>:<env>.
	RelayTenant = Limit{Rate: 500, Period: time.Second, Burst: 1000}
	// CourierLocations is applied per courier session to POST /courier/locations,
	// keyed rl:courier:loc:<session id>.
	CourierLocations = Limit{Rate: 1, Period: time.Second, Burst: 5}
	// CourierEvents is applied per courier session to delivery events, keyed
	// rl:courier:evt:<session id>.
	CourierEvents = Limit{Rate: 10, Period: time.Minute, Burst: 10}
	// CourierRefresh is applied per courier session to refresh-token
	// exchanges, keyed rl:courier:tok:<session id>.
	CourierRefresh = Limit{Rate: 10, Period: time.Minute, Burst: 10}
	// CourierLinkIP is applied per client IP to link-code exchanges, failed
	// guesses included, keyed rl:courier:ip:<client ip>.
	CourierLinkIP = Limit{Rate: 20, Period: time.Minute, Burst: 20}
)

// CourierSessionStore keeps courier sessions (Postgres). Only hashes of link
// codes and refresh tokens are stored. Lookups outside Update are unlocked;
// anything that changes a session re-reads it inside Update.
type CourierSessionStore interface {
	// Get returns the session with id, or ErrNotFound.
	Get(ctx context.Context, id string) (domain.CourierSession, error)
	// FindByLinkHash returns the session whose link code hashes to h, or
	// ErrNotFound.
	FindByLinkHash(ctx context.Context, h []byte) (domain.CourierSession, error)
	// FindByRefreshHash returns the session whose current or any superseded
	// refresh token hashes to h, or ErrNotFound.
	FindByRefreshHash(ctx context.Context, h []byte) (domain.CourierSession, error)
	// Update runs fn in one transaction that holds the lock of the courier's
	// sessions (tenant, environment, courier), so every change to one
	// courier's sessions is serialized. fn's error rolls everything back.
	Update(ctx context.Context, tenantID string, env domain.Environment, courierID string, fn func(CourierSessionTx) error) error
	// Touch sets the session's last_seen_at to now when it is unset or more
	// than CourierTouchInterval older.
	Touch(ctx context.Context, id string, now time.Time) error
}

// CourierSessionTx is the courier's sessions inside CourierSessionStore.Update.
type CourierSessionTx interface {
	// Lock returns session id locked for update, or ErrNotFound.
	Lock(id string) (domain.CourierSession, error)
	// Current returns the courier's session stored with state st (active or
	// pending_link; there is at most one of each) locked for update, or
	// ErrNotFound.
	Current(st domain.SessionStatus) (domain.CourierSession, error)
	// Insert stores a new session.
	Insert(s domain.CourierSession) error
	// Save stores s's state, times, end reason, refresh hash and generation.
	// A refresh hash that s replaces is kept as superseded, so a later use
	// of it is detected as reuse.
	Save(s domain.CourierSession) error
	// Started and Ended enqueue courier.session_started and
	// courier.session_ended for s in the transaction.
	Started(s domain.CourierSession) error
	Ended(s domain.CourierSession) error
}

// CourierTokenHasher computes the stored form of courier secrets:
// HMAC-SHA256 under the gateway pepper of a domain-prefixed value, so a
// refresh token's hash never equals a link code's or an API key's.
type CourierTokenHasher interface {
	HashCourierRefresh(token string) []byte
	HashCourierLink(code string) []byte
}

// CourierDenyList cuts off an ended session's access tokens at once.
type CourierDenyList interface {
	// Deny deny-lists session id for an access token's lifetime plus leeway.
	Deny(ctx context.Context, sessionID string) error
	// Denied reports whether session id is deny-listed.
	Denied(ctx context.Context, sessionID string) (bool, error)
}

// ErrCourierNotFound is a courier that does not exist in the principal's
// tenant and environment.
var ErrCourierNotFound = errors.New("gateway: courier not found")

// CourierLookup checks couriers in the deliveries service.
type CourierLookup interface {
	// CourierExists returns nil when courierID exists in p's tenant and
	// environment, ErrCourierNotFound when it does not, and any other error
	// when the lookup failed.
	CourierExists(ctx context.Context, p domain.Principal, courierID string) error
}
