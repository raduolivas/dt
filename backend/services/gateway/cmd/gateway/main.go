// Command gateway runs the gateway service: the public partner API edge
// (secret key authentication, scopes, rate limits, idempotency, audit log)
// over the key read model it builds from the accounts apikey.* events.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	"github.com/raduolivas/dt/backend/gen/dt/deliveries/v1/deliveriesv1connect"
	"github.com/raduolivas/dt/backend/pkg/apikey"
	"github.com/raduolivas/dt/backend/pkg/bootstrap"
	"github.com/raduolivas/dt/backend/pkg/outbox"
	"github.com/raduolivas/dt/backend/pkg/rpcauth"
	"github.com/raduolivas/dt/backend/services/gateway/internal/adapters/inbound/consumer"
	httpapi "github.com/raduolivas/dt/backend/services/gateway/internal/adapters/inbound/http"
	"github.com/raduolivas/dt/backend/services/gateway/internal/adapters/outbound/keyhash"
	"github.com/raduolivas/dt/backend/services/gateway/internal/adapters/outbound/postgres"
	redislimit "github.com/raduolivas/dt/backend/services/gateway/internal/adapters/outbound/redis"
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

	deliveries, err := newDeliveriesClient(os.Getenv("DELIVERIES_RPC_URL"), os.Getenv("INTERNAL_RPC_TOKEN"))
	if err != nil {
		return fmt.Errorf("gateway: config: %w", err)
	}

	return bootstrap.Run(ctx, bootstrap.Options{
		Name: "gateway",
		// Redis is non-fatal for /readyz: the limiter fails open without it
		// (R21), so an outage must not drain the replicas.
		Needs:      bootstrap.Needs{Redis: true, RedisNonFatal: true, NATS: true},
		Migrations: migrations.FS,
		Ready:      ready,
		Setup: func(ctx context.Context, d bootstrap.Deps, mux *http.ServeMux) ([]bootstrap.Worker, error) {
			if err := outbox.EnsureStream(ctx, d.JS, outbox.StreamAccounts); err != nil {
				return nil, err
			}
			clock := systemClock{}
			keys := postgres.New(d.DB)
			limiter := redislimit.NewLimiter(d.Redis)
			idem := postgres.NewIdempotencyStore(d.DB)
			audit := postgres.NewAuditStore(d.DB)

			httpapi.Mount(mux, httpapi.Routes(httpapi.Deps{Deliveries: deliveries, Log: d.Log}), httpapi.Middleware{
				Auth:           &app.Authenticator{Keys: keys, Hasher: keyhash.New(pepper), Clock: clock},
				TrustedProxies: trusted,
				Log:            d.Log,
				FailedAuth:     httpapi.FailedAuthLimit(limiter, d.Log),
				RateLimit:      httpapi.PartnerRateLimit(limiter, d.Log),
				Idempotency:    httpapi.Idempotency(idem, clock, d.Log),
				Audit:          httpapi.Audit(audit, clock, d.Log),
			})

			consume := func(ctx context.Context) error {
				return outbox.Consume(ctx, d.JS, d.DB, d.Log, consumer.APIKeysStream, consumer.APIKeysConfig, consumer.APIKeys(keys))
			}
			sweeper := &app.IdempotencySweeper{Store: idem, Clock: clock, Log: d.Log}
			retention := &app.AuditRetention{Store: audit, Clock: clock, Log: d.Log}
			return []bootstrap.Worker{consume, sweeper.Run, retention.Run}, nil
		},
	})
}

// deliveriesCallTimeout bounds every call to the deliveries service.
const deliveriesCallTimeout = 5 * time.Second

// newDeliveriesClient builds the deliveries RPC client. Errors never echo the
// token.
func newDeliveriesClient(baseURL, token string) (deliveriesv1connect.DeliveriesServiceClient, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("DELIVERIES_RPC_URL must be an http(s) URL")
	}
	auth, err := rpcauth.NewClientInterceptor(token)
	if err != nil {
		return nil, fmt.Errorf("INTERNAL_RPC_TOKEN: %w", err)
	}
	hc := &http.Client{Timeout: deliveriesCallTimeout}
	return deliveriesv1connect.NewDeliveriesServiceClient(
		connect.NewClient(connecthttp.NewTransport(hc, baseURL), auth)), nil
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }
