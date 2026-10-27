// Command tracking runs the tracking service: the read model consumers of
// the DELIVERIES and ACCOUNTS streams, the internal Connect RPC API (on its
// own mTLS port), the trail writer, the outbox relay that publishes the
// tracking.* events and the signal sweeper. Its main port (PORT) carries the
// health endpoints and the public viewer endpoints (snapshot, WebSocket and
// SSE streams), so it is never mTLS; viewer routes are never on the
// internal RPC port.
package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/raduolivas/dt/backend/pkg/bootstrap"
	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/pkg/otelx"
	"github.com/raduolivas/dt/backend/pkg/outbox"
	"github.com/raduolivas/dt/backend/pkg/rpcauth"
	"github.com/raduolivas/dt/backend/pkg/viewerjwt"
	"github.com/raduolivas/dt/backend/services/tracking/internal/adapters/inbound/consumer"
	httpapi "github.com/raduolivas/dt/backend/services/tracking/internal/adapters/inbound/http"
	"github.com/raduolivas/dt/backend/services/tracking/internal/adapters/inbound/rpc"
	"github.com/raduolivas/dt/backend/services/tracking/internal/adapters/outbound/osrm"
	"github.com/raduolivas/dt/backend/services/tracking/internal/adapters/outbound/postgres"
	"github.com/raduolivas/dt/backend/services/tracking/internal/adapters/outbound/redis"
	"github.com/raduolivas/dt/backend/services/tracking/internal/app"
	"github.com/raduolivas/dt/backend/services/tracking/migrations"
)

// defaultInternalRPCPort is the internal RPC port when INTERNAL_RPC_PORT is
// unset (the next free one after accounts' 8084).
const defaultInternalRPCPort = 8085

// partitionsAhead is how many days of trail partitions exist beyond today.
const partitionsAhead = 3

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	// Secrets are validated before anything starts; errors never echo them.
	token := os.Getenv("INTERNAL_RPC_TOKEN")
	if len(token) < rpcauth.MinTokenLen {
		return errors.New("tracking: config: INTERNAL_RPC_TOKEN must be at least 32 bytes")
	}
	rpcPort := defaultInternalRPCPort
	if v := os.Getenv("INTERNAL_RPC_PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p < 1 || p > 65535 {
			return errors.New("tracking: config: INTERNAL_RPC_PORT must be a port number")
		}
		rpcPort = p
	}
	rpcTLS, err := rpcauth.ServerTLSFromEnv("INTERNAL_RPC")
	if err != nil {
		return fmt.Errorf("tracking: config: %w", err)
	}
	jwksURL, err := jwksURLFromEnv()
	if err != nil {
		return err
	}
	holder, err := instanceID()
	if err != nil {
		return err
	}

	// js is set by Setup; the stream checks read it per probe.
	var js atomic.Pointer[jetstream.JetStream]
	streamCheck := func(name string) httpx.Check {
		return func(ctx context.Context) error {
			j := js.Load()
			if j == nil {
				return errors.New("jetstream not ready")
			}
			return outbox.StreamExists(ctx, *j, name)
		}
	}

	return bootstrap.Run(ctx, bootstrap.Options{
		Name:  "tracking",
		Needs: bootstrap.Needs{NATS: true, Redis: true},
		// The main port serves the customer viewer endpoints to anyone.
		PublicEndpoint: true,
		Migrations:     migrations.FS,
		Checks: map[string]httpx.Check{
			"deliveries_stream": streamCheck(outbox.StreamDeliveries.Name),
			"accounts_stream":   streamCheck(outbox.StreamAccounts.Name),
		},
		Setup: func(ctx context.Context, d bootstrap.Deps, mux *http.ServeMux) ([]bootstrap.Worker, error) {
			// tracking owns TRACKING; DELIVERIES and ACCOUNTS belong to
			// deliveries and accounts.
			js.Store(&d.JS)
			if err := outbox.EnsureStream(ctx, d.JS, outbox.StreamTracking); err != nil {
				return nil, err
			}
			repo := postgres.New(d.DB)
			if err := repo.EnsurePartitions(ctx, time.Now(), partitionsAhead); err != nil {
				return nil, err
			}
			states := redis.NewStates(d.Redis, repo.CourierActiveDeliveries)
			trail := postgres.NewTrailWriter(d.DB, d.Log)
			svc := &app.Service{
				States: states, Reads: repo, Partner: repo, Trail: trail, Pub: redis.NewPublisher(d.Redis),
				Events: repo, Sampler: states, Clock: systemClock{}, Log: d.Log,
			}
			// Without OSRM URLs no routes are built (and the ETA stays
			// unknown): the service still tracks and shows the courier.
			if urls := osrmURLs(); len(urls) > 0 {
				svc.Router = osrm.New(urls, osrm.DefaultTimeout)
			} else {
				d.Log.Warn("OSRM_CAR_URL and OSRM_BIKE_URL are unset: routes and ETAs are off")
			}

			rpcSrv, err := internalServer(svc, token, rpcPort, rpcTLS, d.Log)
			if err != nil {
				return nil, err
			}
			relay := &outbox.Relay{Pool: d.DB, JS: d.JS, Log: d.Log}
			deliveries := func(ctx context.Context) error {
				if err := outbox.WaitForStream(ctx, d.JS, outbox.StreamDeliveries.Name, d.Log); err != nil {
					return err
				}
				return outbox.Consume(ctx, d.JS, d.DB, d.Log, consumer.DeliveriesStream,
					consumer.DeliveriesConfig, consumer.Deliveries(repo, svc, d.Log))
			}
			accounts := func(ctx context.Context) error {
				if err := outbox.WaitForStream(ctx, d.JS, outbox.StreamAccounts.Name, d.Log); err != nil {
					return err
				}
				return outbox.Consume(ctx, d.JS, d.DB, d.Log, consumer.AccountsStream,
					consumer.AccountsConfig, consumer.Settings(repo, svc, d.Log))
			}
			// Viewer endpoints: tokens verified against the gateway's JWKS.
			hub := httpapi.NewHub(httpapi.Deps{
				Verifier: viewerjwt.NewVerifier(jwksURL, nil), Snapshots: svc,
				Links: redis.NewViewerLinks(d.Redis), Feed: redis.NewFeed(d.Redis, d.Log), Log: d.Log,
			})
			hub.Mount(mux)
			sweeper := &app.Sweeper{Svc: svc, Lease: redis.NewLease(d.Redis, redis.SweeperLeaseKey), ID: holder}
			return []bootstrap.Worker{rpcSrv.Serve, relay.Run, trail.Run, deliveries, accounts, hub.Run, sweeper.Run}, nil
		},
	})
}

// internalServer binds the internal RPC listener: mTLS when configured,
// with the same middleware as the main port.
func internalServer(svc *app.Service, token string, port int, tlsCfg *tls.Config, log *slog.Logger) (*httpx.Server, error) {
	mux := http.NewServeMux()
	if err := rpc.Mount(mux, svc, token, log); err != nil {
		return nil, err
	}
	handler := httpx.RequestID(httpx.AccessLog(log)(httpx.Recover(log)(otelx.HTTP(mux, "tracking"))))
	srv, err := httpx.Listen(":"+strconv.Itoa(port), handler, log)
	if err != nil {
		return nil, fmt.Errorf("internal rpc listen: %w", err)
	}
	if tlsCfg != nil {
		srv.UseTLS(tlsCfg)
	}
	log.Info("internal rpc listening", "addr", srv.Addr(), "tls", tlsCfg != nil)
	return srv, nil
}

// jwksURLFromEnv reads JWKS_URL, the gateway's viewer token keys.
func jwksURLFromEnv() (string, error) {
	v := os.Getenv("JWKS_URL")
	u, err := url.Parse(v)
	if v == "" || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", errors.New("tracking: config: JWKS_URL must be the gateway's http(s) JWKS URL")
	}
	return v, nil
}

// instanceID names this process as a lease holder.
func instanceID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("tracking: instance id: %w", err)
	}
	host, _ := os.Hostname()
	return host + "-" + hex.EncodeToString(b), nil
}

// osrmURLs returns the configured OSRM base URLs by profile.
func osrmURLs() map[string]string {
	urls := map[string]string{}
	if u := os.Getenv("OSRM_CAR_URL"); u != "" {
		urls["car"] = u
	}
	if u := os.Getenv("OSRM_BIKE_URL"); u != "" {
		urls["bike"] = u
	}
	return urls
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }
