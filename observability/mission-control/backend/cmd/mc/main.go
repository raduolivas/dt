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
	"sync"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/pkg/logx"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/config"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/ingest"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/normalize"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/topology"
)

const (
	readyTimeout  = 2 * time.Second
	pruneInterval = time.Hour
	storeOpenWait = 60 * time.Second
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, stop); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, stop context.CancelFunc) error {
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
	mux := http.NewServeMux()
	mux.Handle("GET /livez", httpx.Livez())
	// Ready means our own Postgres answers; NATS and DT health are reported elsewhere (ruling R5).
	mux.Handle("GET /readyz", httpx.Readyz(map[string]httpx.Check{"postgres": st.Ping}, readyTimeout))
	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	self := ingest.NewSelf(st, log)
	norm := &normalize.Normalizer{Topology: topology.Default(), Thresholds: cfg.Thresholds, OnRedacted: self.Redacted}
	writer := ingest.NewWriter(st, self, ingest.DefaultBuffer)
	otlpSrv := &http.Server{Addr: cfg.OTLPAddr, Handler: ingest.OTLPHandler(writer, norm, self, log), ReadHeaderTimeout: 5 * time.Second}

	var wg sync.WaitGroup
	spawn := func(f func()) {
		wg.Add(1)
		go func() { defer wg.Done(); f() }()
	}
	spawn(func() {
		if err := self.Run(ctx); err != nil {
			log.Error("self-health stopped", "error", err.Error())
		}
	})
	spawn(func() { writer.Run(ctx) })
	spawn(func() { prune(ctx, st, cfg, log) })
	spawn(func() { ingest.PollHealth(ctx, healthTargets(cfg), ingest.PollInterval, writer.Sink("health")) })
	spawn(func() {
		ingest.PollConsumers(ctx, cfg.NATSMonitorURL, ingest.PollInterval, writer.Sink("nats.monitor"))
	})
	spawn(func() { runNATS(ctx, cfg, st, norm, self, writer, log) })
	// The server goroutines end through Shutdown below; wg covers the rest.
	defer wg.Wait()
	defer stop() // LIFO: cancel, wait for goroutines, then st.Close

	errc := make(chan error, 2)
	go func() { errc <- srv.ListenAndServe() }()
	go func() { errc <- otlpSrv.ListenAndServe() }()
	log.Info("mission control listening", "addr", cfg.HTTPAddr, "env", cfg.Env, "simulation", cfg.Simulation)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = otlpSrv.Shutdown(shutdownCtx)
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	stop()
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

// prune deletes events past retention once an hour.
func prune(ctx context.Context, st *store.Store, cfg config.Config, log *slog.Logger) {
	t := time.NewTicker(pruneInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if n, err := st.DeleteOlderThan(ctx, now.Add(-cfg.Retention)); err != nil {
				log.Warn("retention prune failed", "error", err.Error())
			} else if n > 0 {
				log.Info("retention prune", "deleted", n)
			}
		}
	}
}

// healthTargets are the DT services Mission Control probes (/livez, /readyz).
// deliveries and accounts serve their health only over mTLS; Mission Control
// holds no DT client certificate, so they are not probed (their signals come
// from spans and consumer state instead).
func healthTargets(cfg config.Config) map[string]string {
	return map[string]string{
		"gateway":  cfg.GatewayURL,
		"tracking": "http://tracking:8082",
		"webhooks": "http://webhooks:8083",
	}
}

// runNATS connects to NATS and runs the consumers. If NATS is unreachable it
// keeps retrying (logging once a minute) and reports the state as a health
// event; startup never fails on it (ruling R5).
func runNATS(ctx context.Context, cfg config.Config, st *store.Store, norm *normalize.Normalizer, self *ingest.Self, w *ingest.Writer, log *slog.Logger) {
	emit := func(outcome, reason string) {
		now := time.Now().UTC()
		w.Add("nats", store.Event{ObservedAt: now, OccurredAt: now, Kind: "health", Source: "nats", Target: "mission-control",
			Outcome: outcome, Reason: reason, Origin: "live"})
	}
	var lastWarn time.Time
	for ctx.Err() == nil {
		nc, err := nats.Connect(cfg.NATSURL, nats.Name("mission-control"), nats.Timeout(3*time.Second), nats.MaxReconnects(-1),
			nats.DisconnectErrHandler(func(_ *nats.Conn, _ error) { emit("failed", "nats_disconnected") }),
			nats.ReconnectHandler(func(_ *nats.Conn) { emit("ok", "") }))
		if err != nil {
			if time.Since(lastWarn) >= time.Minute {
				lastWarn = time.Now()
				log.Warn("NATS not reachable, retrying", "error", err.Error())
				emit("failed", "nats_unreachable")
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		js, err := jetstream.New(nc)
		if err != nil {
			nc.Close()
			log.Error("jetstream init failed", "error", err.Error())
			return
		}
		log.Info("NATS connected")
		emit("ok", "")
		_ = ingest.RunNATS(ctx, js, st, norm, ingest.NATSOptions{Self: self, Log: log, Retention: cfg.Retention})
		nc.Close()
		return
	}
}
