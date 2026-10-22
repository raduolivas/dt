// Command deliveries runs the deliveries service: an internal Connect RPC
// API, the tenant settings consumer, the pickup confirmation sweeper and the
// outbox relay that publishes the deliveries.* events.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/raduolivas/dt/backend/pkg/bootstrap"
	"github.com/raduolivas/dt/backend/pkg/ids"
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

	return bootstrap.Run(ctx, bootstrap.Options{
		Name:       "deliveries",
		Needs:      bootstrap.Needs{NATS: true},
		Migrations: migrations.FS,
		Setup: func(ctx context.Context, d bootstrap.Deps, mux *http.ServeMux) ([]bootstrap.Worker, error) {
			for _, cfg := range []jetstream.StreamConfig{outbox.StreamDeliveries, outbox.StreamAccounts} {
				if err := outbox.EnsureStream(ctx, d.JS, cfg); err != nil {
					return nil, err
				}
			}
			repo := postgres.New(d.DB, box)
			svc := &app.Service{Repo: repo, Clock: systemClock{}, IDs: idGen{}}
			if err := rpc.Mount(mux, svc, token, d.Log); err != nil {
				return nil, err
			}
			relay := &outbox.Relay{Pool: d.DB, JS: d.JS, Log: d.Log}
			sweeper := &app.PickupSweeper{Repo: repo, Clock: systemClock{}, Log: d.Log}
			settings := func(ctx context.Context) error {
				return outbox.Consume(ctx, d.JS, d.DB, d.Log, consumer.SettingsStream,
					consumer.SettingsConfig, consumer.Settings(repo.ApplySettings))
			}
			return []bootstrap.Worker{relay.Run, settings, sweeper.Run}, nil
		},
	})
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

type idGen struct{}

func (idGen) New(prefix string) string { return ids.New(prefix) }
