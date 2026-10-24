// Package bootstrap wires the runtime every service shares: config from the
// environment, logger, tracing, database pool and migrations, optional Redis
// and NATS, health endpoints, middleware, background workers and a server
// with graceful shutdown.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"

	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/pkg/logx"
	"github.com/raduolivas/dt/backend/pkg/migratex"
	"github.com/raduolivas/dt/backend/pkg/otelx"
)

// readyTimeout bounds /readyz so a hung dependency cannot hang the probe.
const readyTimeout = 2 * time.Second

// nonFatalCheckTimeout bounds a non-fatal readiness check. It is shorter than
// readyTimeout so the check reports (and is ignored) before the probe gives
// up on it and counts it as failing.
const nonFatalCheckTimeout = 500 * time.Millisecond

// Config is read from the environment.
type Config struct {
	Port        int    `env:"PORT" envDefault:"8080"`
	LogLevel    string `env:"LOG_LEVEL" envDefault:"info"`
	DatabaseURL string `env:"DATABASE_URL,required,notEmpty"`
	// MigrationsDatabaseURL connects as the role that owns the service's
	// schema and runs its migrations, so the runtime role (DATABASE_URL)
	// owns no table and cannot weaken row-level security. Staging and
	// production always set it; when empty, migrations run as DATABASE_URL.
	MigrationsDatabaseURL string `env:"MIGRATIONS_DATABASE_URL"`
	RedisURL              string `env:"REDIS_URL"`
	NATSURL               string `env:"NATS_URL"`
}

// Needs names the optional dependencies a service uses. Each one needs its
// URL in the environment and adds a /readyz check.
type Needs struct {
	Redis, NATS bool
	// RedisNonFatal keeps a failing Redis out of /readyz: the redis check
	// still runs (bounded to 500 ms) and logs a warn when it fails, but it
	// never makes /readyz answer 503. For services that degrade without
	// Redis instead of failing (the gateway's limiter fails open), so a
	// Redis outage does not drain every replica. Ignored unless Redis.
	RedisNonFatal bool
}

// Deps are handed to a service's Setup function.
type Deps struct {
	Log   *slog.Logger
	DB    *pgxpool.Pool
	Redis *redis.Client       // nil unless Needs.Redis
	JS    jetstream.JetStream // nil unless Needs.NATS
}

// Worker is a background task. It runs until ctx is cancelled; returning an
// error stops the whole service.
type Worker func(ctx context.Context) error

// Options describe one service.
type Options struct {
	Name  string
	Needs Needs
	// Migrations holds the service's *.sql files in its root. Optional;
	// applied before Setup.
	Migrations fs.FS
	// Setup mounts routes and returns background workers. Optional.
	Setup func(ctx context.Context, d Deps, mux *http.ServeMux) ([]Worker, error)
	// Ready is a test hook called with the bound address once listening.
	Ready func(addr string)
}

// RedisOptions parses a REDIS_URL into the client options Run uses. It turns
// on ContextTimeoutEnabled: without it go-redis ignores context deadlines for
// socket I/O and a hung Redis holds every call for its read timeout and
// retries (seconds), whatever deadline the caller set.
func RedisOptions(url string) (*redis.Options, error) {
	o, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	o.ContextTimeoutEnabled = true
	return o, nil
}

// nonFatal runs check within nonFatalCheckTimeout and reports success
// whatever happens, logging a warn (without the error, which may hold the
// URL) when it fails.
func nonFatal(log *slog.Logger, name string, check httpx.Check) httpx.Check {
	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, nonFatalCheckTimeout)
		defer cancel()
		if err := check(ctx); err != nil {
			log.WarnContext(ctx, "readiness check failing (non-fatal)", "check", name)
		}
		return nil
	}
}

// Run starts the service and blocks until ctx is done or a worker fails.
func Run(ctx context.Context, opts Options) (err error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return fmt.Errorf("%s: config: %w", opts.Name, err)
	}
	if opts.Needs.Redis && cfg.RedisURL == "" {
		return fmt.Errorf("%s: config: REDIS_URL is required", opts.Name)
	}
	if opts.Needs.NATS && cfg.NATSURL == "" {
		return fmt.Errorf("%s: config: NATS_URL is required", opts.Name)
	}
	level, err := logx.ParseLevel(cfg.LogLevel)
	if err != nil {
		return fmt.Errorf("%s: config: %w", opts.Name, err)
	}
	log := logx.New(os.Stdout, level, opts.Name)

	shutdownTracing, err := otelx.Init(ctx, opts.Name)
	if err != nil {
		return fmt.Errorf("%s: tracing: %w", opts.Name, err)
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if serr := shutdownTracing(sctx); serr != nil {
			log.Warn("tracing shutdown", "error", serr)
		}
	}()

	if opts.Migrations != nil {
		migURL := cfg.MigrationsDatabaseURL
		if migURL == "" {
			log.Warn("migrations_with_runtime_role")
			migURL = cfg.DatabaseURL
		}
		if err := migratex.Up(ctx, migURL, opts.Migrations); err != nil {
			return fmt.Errorf("%s: %w", opts.Name, err)
		}
	}

	// pgxpool.New does not connect; the first query or /readyz does.
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("%s: database: invalid DATABASE_URL", opts.Name)
	}
	defer pool.Close()

	deps := Deps{Log: log, DB: pool}
	checks := map[string]httpx.Check{"db": pool.Ping}

	if opts.Needs.Redis {
		ropts, err := RedisOptions(cfg.RedisURL)
		if err != nil {
			return fmt.Errorf("%s: redis: invalid REDIS_URL", opts.Name)
		}
		// go-redis connects lazily, so an unreachable Redis shows in /readyz.
		deps.Redis = redis.NewClient(ropts)
		defer func() { _ = deps.Redis.Close() }()
		ping := func(ctx context.Context) error { return deps.Redis.Ping(ctx).Err() }
		if opts.Needs.RedisNonFatal {
			ping = nonFatal(log, "redis", ping)
		}
		checks["redis"] = ping
	}
	if opts.Needs.NATS {
		nc, err := nats.Connect(cfg.NATSURL, nats.Name(opts.Name), nats.Timeout(5*time.Second))
		if err != nil {
			return fmt.Errorf("%s: nats: connect failed", opts.Name)
		}
		defer func() { _ = nc.Drain() }() // runs after workers stop
		deps.JS, err = jetstream.New(nc)
		if err != nil {
			return fmt.Errorf("%s: nats: jetstream: %w", opts.Name, err)
		}
		checks["nats"] = func(ctx context.Context) error {
			if nc.Status() != nats.CONNECTED {
				return errors.New("nats not connected")
			}
			_, err := deps.JS.AccountInfo(ctx)
			return err
		}
	}

	mux := http.NewServeMux()
	mux.Handle("GET /livez", httpx.Livez())
	mux.Handle("GET /readyz", httpx.Readyz(checks, readyTimeout))
	var workers []Worker
	if opts.Setup != nil {
		workers, err = opts.Setup(ctx, deps, mux)
		if err != nil {
			return fmt.Errorf("%s: setup: %w", opts.Name, err)
		}
	}
	handler := httpx.RequestID(httpx.AccessLog(log)(httpx.Recover(log)(otelx.HTTP(mux, opts.Name))))

	srv, err := httpx.Listen(":"+strconv.Itoa(cfg.Port), handler, log)
	if err != nil {
		return fmt.Errorf("%s: listen: %w", opts.Name, err)
	}
	log.Info("listening", "addr", srv.Addr())
	if opts.Ready != nil {
		opts.Ready(srv.Addr())
	}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return srv.Serve(gctx) })
	for _, w := range workers {
		g.Go(func() error { return w(gctx) })
	}
	return g.Wait()
}
