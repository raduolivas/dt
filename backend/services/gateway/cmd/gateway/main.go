// Command gateway runs the gateway service: the public partner API edge
// (secret key authentication, scopes, rate limits, idempotency, audit log)
// over the key read model it builds from the accounts apikey.* events.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/raduolivas/dt/backend/pkg/apikey"
	"github.com/raduolivas/dt/backend/pkg/bootstrap"
	"github.com/raduolivas/dt/backend/pkg/outbox"
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

	return bootstrap.Run(ctx, bootstrap.Options{
		Name:       "gateway",
		Needs:      bootstrap.Needs{Redis: true, NATS: true},
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

			httpapi.Mount(mux, httpapi.Routes(httpapi.Deps{}), httpapi.Middleware{
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

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }
