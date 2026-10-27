package viewerjwt

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Verifier cache and refetch rules.
const (
	// CacheTTL is how long fetched keys are used before they are fetched again.
	CacheTTL = 5 * time.Minute
	// RefetchInterval is the least time between two fetches, so a stream of
	// tokens with made-up key ids cannot hammer the JWKS endpoint.
	RefetchInterval = 10 * time.Second
	// DefaultFetchTimeout bounds a JWKS fetch when NewVerifier gets no client.
	DefaultFetchTimeout = 3 * time.Second
	// maxJWKSBytes bounds the JWKS document.
	maxJWKSBytes = 64 << 10
)

var (
	// ErrInvalid covers every token that must be refused: malformed, wrong
	// algorithm, unknown key, bad signature, wrong audience or issuer,
	// expired, or missing claims.
	ErrInvalid = errors.New("viewerjwt: invalid viewer token")
	// ErrUnavailable means no keys could be loaded to check the token with
	// (the JWKS endpoint is unreachable and nothing is cached). The token
	// may be fine; the caller should answer "retry", not "unauthorized".
	ErrUnavailable = errors.New("viewerjwt: verification keys unavailable")
)

// Verifier checks viewer tokens against a JWKS, fetched over HTTP and cached
// (or, from Signer.Verifier, against fixed keys).
type Verifier struct {
	url    string
	client *http.Client
	static bool

	mu          sync.Mutex
	clock       func() time.Time
	keys        map[string]ed25519.PublicKey
	fetchedAt   time.Time // last successful fetch
	attemptedAt time.Time // last fetch attempt
}

// NewVerifier returns a Verifier that loads keys from jwksURL with client
// (nil means an http.Client with DefaultFetchTimeout). Keys are cached for CacheTTL; a token with
// an unknown kid triggers a refetch, at most once per RefetchInterval. When a
// fetch fails the last good keys stay in use.
func NewVerifier(jwksURL string, client *http.Client) *Verifier {
	if client == nil {
		client = &http.Client{Timeout: DefaultFetchTimeout}
	}
	return &Verifier{url: jwksURL, client: client, clock: time.Now}
}

// SetClock replaces the clock the key cache runs on (a test seam).
func (v *Verifier) SetClock(now func() time.Time) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.clock = now
}

// Verify checks token at now and returns its claims. It accepts only EdDSA
// tokens signed by a known key, with aud "viewer", iss "dt-gateway", an exp
// no more than 30 s in the past and every claim present. Any refusal is
// ErrInvalid; ErrUnavailable means the keys could not be loaded.
func (v *Verifier) Verify(ctx context.Context, token string, now time.Time) (Claims, error) {
	var tc tokenClaims
	_, err := jwt.ParseWithClaims(token, &tc, func(t *jwt.Token) (any, error) {
		kid, ok := t.Header["kid"].(string)
		if !ok || kid == "" {
			return nil, errors.New("no kid")
		}
		return v.key(ctx, kid)
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}),
		jwt.WithAudience(Audience),
		jwt.WithIssuer(Issuer),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(Leeway),
		jwt.WithTimeFunc(func() time.Time { return now }),
	)
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			return Claims{}, ErrUnavailable
		}
		return Claims{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	c := Claims{DeliveryID: tc.Subject, TenantID: tc.Tenant, Env: tc.Env, LinkID: tc.Link}
	if err := c.check(); err != nil {
		return Claims{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return c, nil
}

// key returns the public key named kid, fetching the JWKS when the cache is
// stale or does not know kid (rate limited by RefetchInterval).
func (v *Verifier) key(ctx context.Context, kid string) (ed25519.PublicKey, error) {
	v.mu.Lock()
	k, known := v.keys[kid]
	if v.static {
		v.mu.Unlock()
		if !known {
			return nil, errors.New("unknown kid")
		}
		return k, nil
	}
	now := v.clock()
	fresh := !v.fetchedAt.IsZero() && now.Sub(v.fetchedAt) < CacheTTL
	if known && fresh {
		v.mu.Unlock()
		return k, nil
	}
	canFetch := v.attemptedAt.IsZero() || now.Sub(v.attemptedAt) >= RefetchInterval
	if canFetch {
		v.attemptedAt = now
	}
	v.mu.Unlock()

	if canFetch {
		keys, err := v.fetch(ctx)
		v.mu.Lock()
		switch {
		case err == nil:
			v.keys, v.fetchedAt = keys, v.clock()
		case ctx.Err() != nil && v.attemptedAt.Equal(now):
			// The caller gave up, not the endpoint: free the refetch slot.
			v.attemptedAt = time.Time{}
		}
		v.mu.Unlock()
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	if v.keys == nil {
		return nil, ErrUnavailable
	}
	if k, ok := v.keys[kid]; ok {
		return k, nil
	}
	return nil, errors.New("unknown kid")
}

func (v *Verifier) fetch(ctx context.Context) (map[string]ed25519.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.url, nil)
	if err != nil {
		return nil, fmt.Errorf("viewerjwt: jwks request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	res, err := v.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("viewerjwt: jwks fetch: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("viewerjwt: jwks fetch: status %d", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, maxJWKSBytes+1))
	if err != nil {
		return nil, fmt.Errorf("viewerjwt: jwks read: %w", err)
	}
	if len(b) > maxJWKSBytes {
		return nil, errors.New("viewerjwt: jwks too large")
	}
	return parseJWKS(b)
}
