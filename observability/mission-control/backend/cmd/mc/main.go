// Command mc runs Mission Control: it ingests DT telemetry read-only and
// serves the troubleshooting API and UI on loopback. It never writes to DT
// outside simulation mode. This skeleton serves /livez and /readyz.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/pkg/logx"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/config"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
)

const (
	readyTimeout      = 2 * time.Second
	heartbeatInterval = 10 * time.Second
	pruneInterval     = time.Hour
	storeOpenWait     = 60 * time.Second
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
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logx.New(os.Stdout, slog.LevelInfo, "mission-control")

	st, err := openStore(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.RecordStartup(ctx, time.Now().UTC()); err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.Handle("GET /livez", httpx.Livez())
	// Ready means our own Postgres answers; NATS and DT health are reported elsewhere (ruling R5).
	mux.Handle("GET /readyz", httpx.Readyz(map[string]httpx.Check{"postgres": st.Ping}, readyTimeout))
	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	go background(ctx, st, cfg, log)

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("mission control listening", "addr", cfg.HTTPAddr, "env", cfg.Env, "simulation", cfg.Simulation)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// openStore retries while Postgres starts, so compose start order is not a race.
func openStore(ctx context.Context, url string, log *slog.Logger) (*store.Store, error) {
	deadline := time.Now().Add(storeOpenWait)
	for {
		st, err := store.Open(ctx, url)
		if err == nil {
			return st, nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil, err
		}
		log.Warn("store not ready, retrying", "error", err.Error())
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// background writes the heartbeat and prunes events past retention.
func background(ctx context.Context, st *store.Store, cfg config.Config, log *slog.Logger) {
	hb := time.NewTicker(heartbeatInterval)
	defer hb.Stop()
	prune := time.NewTicker(pruneInterval)
	defer prune.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-hb.C:
			if err := st.Heartbeat(ctx, now.UTC()); err != nil {
				log.Warn("heartbeat failed", "error", err.Error())
			}
		case now := <-prune.C:
			if n, err := st.DeleteOlderThan(ctx, now.Add(-cfg.Retention)); err != nil {
				log.Warn("retention prune failed", "error", err.Error())
			} else if n > 0 {
				log.Info("retention prune", "deleted", n)
			}
		}
	}
}
