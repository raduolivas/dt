// Command deliveries runs the deliveries service: an internal Connect RPC
// API, the tenant settings consumer, the tracking arrivals consumer (which
// applies arriving), the pickup confirmation sweeper, the nightly retention
// job (02:00 UTC) and the outbox relay that publishes the deliveries.*
// events.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/raduolivas/dt/backend/pkg/bootstrap"
	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/pkg/ids"
	"github.com/raduolivas/dt/backend/pkg/nightly"
	"github.com/raduolivas/dt/backend/pkg/outbox"
	"github.com/raduolivas/dt/backend/pkg/rpcauth"
	"github.com/raduolivas/dt/backend/pkg/sealbox"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/adapters/inbound/consumer"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/adapters/inbound/rpc"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/adapters/outbound/postgres"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/app"
	"github.com/raduolivas/dt/backend/services/deliveries/migrations"
)

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
		return errors.New("deliveries: config: INTERNAL_RPC_TOKEN must be at least 32 bytes")
	}
	box, err := sealbox.ParseKeys(os.Getenv("DATA_KEYS"), os.Getenv("DATA_KEY_CURRENT"))
	if err != nil {
		return fmt.Errorf("deliveries: config: DATA_KEYS/DATA_KEY_CURRENT: %w", err)
	}

	// js is set by Setup; the accounts_stream check reads it per probe.
	var js atomic.Pointer[jetstream.JetStream]

	serverTLS, err := rpcauth.ServerTLSFromEnv("INTERNAL_RPC")
	if err != nil {
		return fmt.Errorf("deliveries: config: %w", err)
	}

	holder, err := nightly.NewHolder()
	if err != nil {
		return fmt.Errorf("deliveries: %w", err)
	}

	return bootstrap.Run(ctx, bootstrap.Options{
		TLS:        serverTLS,
		Name:       "deliveries",
		Needs:      bootstrap.Needs{NATS: true},
		Migrations: migrations.FS,
		Checks: map[string]httpx.Check{"accounts_stream": func(ctx context.Context) error {
			j := js.Load()
			if j == nil {
				return errors.New("jetstream not ready")
			}
			return outbox.StreamExists(ctx, *j, outbox.StreamAccounts.Name)
		}},
		Setup: func(ctx context.Context, d bootstrap.Deps, mux *http.ServeMux) ([]bootstrap.Worker, error) {
			// deliveries owns DELIVERIES; ACCOUNTS belongs to the accounts service.
			js.Store(&d.JS)
			if err := outbox.EnsureStream(ctx, d.JS, outbox.StreamDeliveries); err != nil {
				return nil, err
			}
			repo := postgres.New(d.DB, box)
			svc := &app.Service{Repo: repo, Clock: systemClock{}, IDs: idGen{}, Retention: repo, Log: d.Log}
			if err := rpc.Mount(mux, svc, token, d.Log); err != nil {
				return nil, err
			}
			relay := &outbox.Relay{Pool: d.DB, JS: d.JS, Log: d.Log}
			sweeper := &app.PickupSweeper{Repo: repo, Clock: systemClock{}, Log: d.Log}
			settings := func(ctx context.Context) error {
				if err := outbox.WaitForStream(ctx, d.JS, outbox.StreamAccounts.Name, d.Log); err != nil {
					return err
				}
				return outbox.Consume(ctx, d.JS, d.DB, d.Log, consumer.SettingsStream,
					consumer.SettingsConfig, consumer.Settings(repo.ApplySettings, d.Log))
			}
			// TRACKING belongs to tracking: wait for it like ACCOUNTS.
			arrivals := func(ctx context.Context) error {
				if err := outbox.WaitForStream(ctx, d.JS, outbox.StreamTracking.Name, d.Log); err != nil {
					return err
				}
				return outbox.Consume(ctx, d.JS, d.DB, d.Log, consumer.TrackingStream,
					consumer.TrackingConfig, consumer.Tracking(svc, d.Log))
			}
			retention := &nightly.Job{Name: "deliveries_retention", Lease: nightly.NewPostgresLease(d.DB, "deliveries_retention"),
				Holder: holder, Do: svc.RunRetention, Log: d.Log}
			return []bootstrap.Worker{relay.Run, settings, arrivals, sweeper.Run, retention.Run}, nil
		},
	})
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

type idGen struct{}

func (idGen) New(prefix string) string { return ids.New(prefix) }
