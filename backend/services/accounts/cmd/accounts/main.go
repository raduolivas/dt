// Command accounts runs the accounts service: tenants and partner API keys,
// an internal Connect RPC API, and the outbox relay that publishes the
// accounts.* events.
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

	"github.com/raduolivas/dt/backend/pkg/apikey"
	"github.com/raduolivas/dt/backend/pkg/bootstrap"
	"github.com/raduolivas/dt/backend/pkg/outbox"
	"github.com/raduolivas/dt/backend/pkg/rpcauth"
	"github.com/raduolivas/dt/backend/services/accounts/internal/adapters/inbound/rpc"
	"github.com/raduolivas/dt/backend/services/accounts/internal/adapters/outbound/keygen"
	"github.com/raduolivas/dt/backend/services/accounts/internal/adapters/outbound/postgres"
	"github.com/raduolivas/dt/backend/services/accounts/internal/app"
	"github.com/raduolivas/dt/backend/services/accounts/migrations"
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
	pepper, err := apikey.LoadPepper(os.Getenv("APIKEY_PEPPER"))
	if err != nil {
		return fmt.Errorf("accounts: config: APIKEY_PEPPER: %w", err)
	}
	token := os.Getenv("INTERNAL_RPC_TOKEN")
	if len(token) < rpcauth.MinTokenLen {
		return errors.New("accounts: config: INTERNAL_RPC_TOKEN must be at least 32 bytes")
	}

	serverTLS, err := rpcauth.ServerTLSFromEnv("INTERNAL_RPC")
	if err != nil {
		return fmt.Errorf("accounts: config: %w", err)
	}

	return bootstrap.Run(ctx, bootstrap.Options{
		TLS:        serverTLS,
		Name:       "accounts",
		Needs:      bootstrap.Needs{NATS: true},
		Migrations: migrations.FS,
		Setup: func(ctx context.Context, d bootstrap.Deps, mux *http.ServeMux) ([]bootstrap.Worker, error) {
			if err := outbox.EnsureStream(ctx, d.JS, outbox.StreamAccounts); err != nil {
				return nil, err
			}
			svc := &app.Service{
				Repo:  postgres.New(d.DB, time.Now),
				Keys:  keygen.New(pepper),
				Clock: systemClock{},
			}
			if err := rpc.Mount(mux, svc, token, d.Log); err != nil {
				return nil, err
			}
			relay := &outbox.Relay{Pool: d.DB, JS: d.JS, Log: d.Log}
			return []bootstrap.Worker{relay.Run}, nil
		},
	})
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }
