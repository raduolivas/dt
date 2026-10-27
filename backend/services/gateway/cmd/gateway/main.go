// Command gateway runs the gateway service: the public partner API edge
// (secret key authentication, scopes, rate limits, idempotency, audit log)
// over the key read model it builds from the accounts apikey.* events.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	"github.com/raduolivas/dt/backend/gen/dt/deliveries/v1/deliveriesv1connect"
	"github.com/raduolivas/dt/backend/gen/dt/tracking/v1/trackingv1connect"
	"github.com/raduolivas/dt/backend/pkg/apikey"
	"github.com/raduolivas/dt/backend/pkg/bootstrap"
	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/pkg/outbox"
	"github.com/raduolivas/dt/backend/pkg/rpcauth"
	"github.com/raduolivas/dt/backend/pkg/sealbox"
	"github.com/raduolivas/dt/backend/pkg/viewerjwt"
	"github.com/raduolivas/dt/backend/services/gateway/internal/adapters/inbound/consumer"
	httpapi "github.com/raduolivas/dt/backend/services/gateway/internal/adapters/inbound/http"
	"github.com/raduolivas/dt/backend/services/gateway/internal/adapters/outbound/keyhash"
	"github.com/raduolivas/dt/backend/services/gateway/internal/adapters/outbound/postgres"
	redislimit "github.com/raduolivas/dt/backend/services/gateway/internal/adapters/outbound/redis"
	trackingadapter "github.com/raduolivas/dt/backend/services/gateway/internal/adapters/outbound/tracking"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/migrations"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run starts the gateway and blocks until ctx is done. ready, when not nil,
// is called with the bound address once the server listens (a test seam;
// main passes nil).
func run(ctx context.Context, ready func(addr string)) error {
	// Config is validated before any I/O; errors never echo the values.
	pepper, err := apikey.LoadPepper(os.Getenv("APIKEY_PEPPER"))
	if err != nil {
		return fmt.Errorf("gateway: config: APIKEY_PEPPER: %w", err)
	}
	trusted, err := httpapi.ParseTrustedProxies(os.Getenv("TRUSTED_PROXIES"))
	if err != nil {
		return fmt.Errorf("gateway: config: %w", err)
	}

	// Seals stored idempotency responses, which can carry personal data.
	box, err := sealbox.ParseKeys(os.Getenv("DATA_KEYS"), os.Getenv("DATA_KEY_CURRENT"))
	if err != nil {
		return fmt.Errorf("gateway: config: DATA_KEYS/DATA_KEY_CURRENT: %w", err)
	}

	clientTLS, err := rpcauth.ClientTLSFromEnv("INTERNAL_RPC")
	if err != nil {
		return fmt.Errorf("gateway: config: %w", err)
	}
	deliveries, err := newDeliveriesClient(os.Getenv("DELIVERIES_RPC_URL"), os.Getenv("INTERNAL_RPC_TOKEN"), clientTLS)
	if err != nil {
		return fmt.Errorf("gateway: config: %w", err)
	}

	// Viewer tokens (Ed25519) and tracking links.
	signer, err := viewerjwt.ParseKeys(os.Getenv("VIEWER_SIGNING_KEYS"), os.Getenv("VIEWER_SIGNING_KEY_CURRENT"))
	if err != nil {
		return fmt.Errorf("gateway: config: VIEWER_SIGNING_KEYS/VIEWER_SIGNING_KEY_CURRENT: %w", err)
	}
	trackingBase, err := parseTrackingURLBase(os.Getenv("TRACKING_URL_BASE"))
	if err != nil {
		return fmt.Errorf("gateway: config: %w", err)
	}
	trackingRPC, err := newTrackingClient(os.Getenv("TRACKING_RPC_URL"), os.Getenv("INTERNAL_RPC_TOKEN"), clientTLS)
	if err != nil {
		return fmt.Errorf("gateway: config: %w", err)
	}

	// js is set by Setup; the accounts_stream check reads it per probe.
	var js atomic.Pointer[jetstream.JetStream]

	return bootstrap.Run(ctx, bootstrap.Options{
		Name: "gateway",
		// The public API: callers' trace ids are linked, never continued.
		PublicEndpoint: true,
		// Redis is non-fatal for /readyz: the limiter fails open without it
		// (R21), so an outage must not drain the replicas.
		Needs:      bootstrap.Needs{Redis: true, RedisNonFatal: true, NATS: true},
		Migrations: migrations.FS,
		Ready:      ready,
		Checks: map[string]httpx.Check{"accounts_stream": func(ctx context.Context) error {
			j := js.Load()
			if j == nil {
				return errors.New("jetstream not ready")
			}
			return outbox.StreamExists(ctx, *j, consumer.APIKeysStream)
		}},
		Setup: func(ctx context.Context, d bootstrap.Deps, mux *http.ServeMux) ([]bootstrap.Worker, error) {
			// ACCOUNTS belongs to the accounts service; the consumer waits for it.
			js.Store(&d.JS)
			clock := systemClock{}
			keys := postgres.New(d.DB)
			limiter := redislimit.NewLimiter(d.Redis)
			idem := postgres.NewIdempotencyStore(d.DB, box)
			audit := postgres.NewAuditStore(d.DB)

			links := &app.TrackingLinks{Store: postgres.NewTrackingLinkStore(d.DB), Revoker: redislimit.NewRevoker(d.Redis), Clock: clock}
			httpapi.MountViewer(mux, httpapi.ViewerDeps{
				Viewer: &app.Viewer{Links: links, Tokens: app.SignerTokens{Signer: signer, Verifier: signer.Verifier()},
					Snapshots: trackingadapter.NewSnapshots(trackingRPC), Limiter: limiter},
				Limiter: limiter, JWKS: signer.JWKS(), TrustedProxies: trusted, Clock: clock, Log: d.Log,
			})
			httpapi.Mount(mux, httpapi.Routes(httpapi.Deps{Deliveries: deliveries, Idempotency: idem, Log: d.Log,
				Links: links, TrackingURLBase: trackingBase}), httpapi.Middleware{
				Auth:           &app.Authenticator{Keys: keys, Hasher: keyhash.New(pepper), Clock: clock},
				TrustedProxies: trusted,
				Log:            d.Log,
				FailedAuth:     httpapi.FailedAuthLimit(limiter, d.Log),
				RateLimit:      httpapi.PartnerRateLimit(limiter, d.Log),
				Idempotency:    httpapi.Idempotency(idem, clock, d.Log),
				Audit:          httpapi.Audit(audit, clock, d.Log),
			})

			consume := func(ctx context.Context) error {
				if err := outbox.WaitForStream(ctx, d.JS, consumer.APIKeysStream, d.Log); err != nil {
					return err
				}
				return outbox.Consume(ctx, d.JS, d.DB, d.Log, consumer.APIKeysStream, consumer.APIKeysConfig, consumer.APIKeys(keys))
			}
			// DELIVERIES belongs to the deliveries service; wait for it.
			consumeDeliveries := func(ctx context.Context) error {
				if err := outbox.WaitForStream(ctx, d.JS, consumer.DeliveriesStream, d.Log); err != nil {
					return err
				}
				return outbox.Consume(ctx, d.JS, d.DB, d.Log, consumer.DeliveriesStream, consumer.DeliveriesConfig, consumer.Deliveries(links))
			}
			lag, err := consumer.NewLagMonitor(otel.Meter("github.com/raduolivas/dt/backend/services/gateway"),
				d.Log, consumer.APIKeysConfig.Durable)
			if err != nil {
				return nil, err
			}
			watchLag := func(ctx context.Context) error {
				return lag.Run(ctx, d.JS, consumer.APIKeysStream)
			}
			sweeper := &app.IdempotencySweeper{Store: idem, Clock: clock, Log: d.Log}
			retention := &app.AuditRetention{Store: audit, Clock: clock, Log: d.Log}
			return []bootstrap.Worker{consume, consumeDeliveries, watchLag, sweeper.Run, retention.Run}, nil
		},
	})
}

// deliveriesCallTimeout bounds every call to the deliveries service.
const deliveriesCallTimeout = 5 * time.Second

// newDeliveriesClient builds the deliveries RPC client (see rpcClient).
func newDeliveriesClient(baseURL, token string, tlsCfg *tls.Config) (deliveriesv1connect.DeliveriesServiceClient, error) {
	c, err := rpcClient("DELIVERIES_RPC_URL", baseURL, token, tlsCfg, deliveriesCallTimeout)
	if err != nil {
		return nil, err
	}
	return deliveriesv1connect.NewDeliveriesServiceClient(c), nil
}

// trackingCallTimeout bounds every call to the tracking service.
const trackingCallTimeout = 3 * time.Second

// newTrackingClient builds the tracking RPC client (see rpcClient).
func newTrackingClient(baseURL, token string, tlsCfg *tls.Config) (trackingv1connect.TrackingServiceClient, error) {
	c, err := rpcClient("TRACKING_RPC_URL", baseURL, token, tlsCfg, trackingCallTimeout)
	if err != nil {
		return nil, err
	}
	return trackingv1connect.NewTrackingServiceClient(c), nil
}

// rpcClient builds an internal RPC client for the URL in env var name.
// Errors never echo the token. With a TLS config the URL must be https and
// the client presents its certificate (mTLS); without one it must be plain
// http.
func rpcClient(name, baseURL, token string, tlsCfg *tls.Config, timeout time.Duration) (*connect.Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New(name + " must be an http(s) URL")
	}
	if tlsCfg != nil && u.Scheme != "https" {
		return nil, errors.New(name + " must be https when INTERNAL_RPC_TLS_* is set")
	}
	auth, err := rpcauth.NewClientInterceptor(token)
	if err != nil {
		return nil, fmt.Errorf("INTERNAL_RPC_TOKEN: %w", err)
	}
	hc := &http.Client{Timeout: timeout}
	if tlsCfg != nil {
		hc.Transport = &http.Transport{TLSClientConfig: tlsCfg, ForceAttemptHTTP2: true,
			MaxIdleConnsPerHost: 16, IdleConnTimeout: 90 * time.Second}
	}
	return connect.NewClient(connecthttp.NewTransport(hc, baseURL), auth), nil
}

// parseTrackingURLBase checks TRACKING_URL_BASE: an absolute http(s) URL
// without query or fragment; a trailing slash is dropped.
func parseTrackingURLBase(s string) (string, error) {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" ||
		u.Fragment != "" || u.User != nil {
		return "", errors.New("TRACKING_URL_BASE must be an http(s) URL without query, fragment or credentials")
	}
	return strings.TrimRight(s, "/"), nil
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }
