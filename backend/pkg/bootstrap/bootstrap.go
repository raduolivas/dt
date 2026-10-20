// Package bootstrap wires the runtime every service shares: config from the
// environment, logger, database pool, health endpoints, middleware and a
// server with graceful shutdown.
package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/pkg/logx"
)

// readyTimeout bounds /readyz so a hung dependency cannot hang the probe.
const readyTimeout = 2 * time.Second

// Config is read from the environment.
type Config struct {
	Port        int    `env:"PORT" envDefault:"8080"`
	LogLevel    string `env:"LOG_LEVEL" envDefault:"info"`
	DatabaseURL string `env:"DATABASE_URL,required,notEmpty"`
}

// Deps are handed to a service's Register function.
type Deps struct {
	Log *slog.Logger
	DB  *pgxpool.Pool
}

// Options describe one service.
type Options struct {
	Name string
	// Register mounts the service's routes. It may be nil.
	Register func(mux *http.ServeMux, d Deps)
	// Ready is a test hook called with the bound address once listening.
	Ready func(addr string)
}

// Run starts the service and blocks until ctx is done.
func Run(ctx context.Context, opts Options) error {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return fmt.Errorf("%s: config: %w", opts.Name, err)
	}
	level, err := logx.ParseLevel(cfg.LogLevel)
	if err != nil {
		return fmt.Errorf("%s: config: %w", opts.Name, err)
	}
	log := logx.New(os.Stdout, level, opts.Name)

	// pgxpool.New does not connect; the first query or /readyz does.
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("%s: database: invalid DATABASE_URL", opts.Name)
	}
	defer pool.Close()

	mux := http.NewServeMux()
	mux.Handle("GET /livez", httpx.Livez())
	mux.Handle("GET /readyz", httpx.Readyz(map[string]httpx.Check{"db": pool.Ping}, readyTimeout))
	if opts.Register != nil {
		opts.Register(mux, Deps{Log: log, DB: pool})
	}
	handler := httpx.RequestID(httpx.AccessLog(log)(httpx.Recover(log)(mux)))

	srv, err := httpx.Listen(":"+strconv.Itoa(cfg.Port), handler, log)
	if err != nil {
		return fmt.Errorf("%s: listen: %w", opts.Name, err)
	}
	log.Info("listening", "addr", srv.Addr())
	if opts.Ready != nil {
		opts.Ready(srv.Addr())
	}
	return srv.Serve(ctx)
}
