package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/raduolivas/dt/backend/services/tracking/internal/app"
)

// Trail writer defaults (design §5: the Postgres write is not on the path
// to the viewer).
const (
	TrailFlushInterval = time.Second
	TrailFlushRows     = 500
	// TrailMaxBuffered bounds memory while Postgres is down: beyond it new
	// rows are dropped (and counted in a warn log).
	TrailMaxBuffered = 100_000
	// trailShutdownTimeout bounds the final flush.
	trailShutdownTimeout = 5 * time.Second
)

// sqlstateCheckViolation is raised (among others) when no partition holds a row.
const sqlstateCheckViolation = "23514"

// TrailWriter buffers trail rows and inserts them in batches: every second,
// as soon as 500 are waiting, and once more on shutdown.
type TrailWriter struct {
	pool *pgxpool.Pool
	log  *slog.Logger

	mu      sync.Mutex
	buf     []app.TrailRow
	dropped int
	kick    chan struct{}
}

var _ app.TrailWriter = (*TrailWriter)(nil)

// NewTrailWriter returns a writer over pool; call Run to start flushing.
func NewTrailWriter(pool *pgxpool.Pool, log *slog.Logger) *TrailWriter {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &TrailWriter{pool: pool, log: log, kick: make(chan struct{}, 1)}
}

// Append queues rows. It never blocks on the database.
func (w *TrailWriter) Append(rows []app.TrailRow) {
	w.mu.Lock()
	room := TrailMaxBuffered - len(w.buf)
	if room < len(rows) {
		w.dropped += len(rows) - max(room, 0)
		rows = rows[:max(room, 0)]
	}
	w.buf = append(w.buf, rows...)
	full := len(w.buf) >= TrailFlushRows
	w.mu.Unlock()
	if full {
		select {
		case w.kick <- struct{}{}:
		default:
		}
	}
}

// Run flushes until ctx ends, then flushes what is left (bounded by 5 s).
func (w *TrailWriter) Run(ctx context.Context) error {
	t := time.NewTicker(TrailFlushInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), trailShutdownTimeout)
			defer cancel()
			if err := w.Flush(fctx); err != nil {
				w.log.ErrorContext(fctx, "trail_flush_on_shutdown_failed", "error", err)
			}
			return nil
		case <-t.C:
		case <-w.kick:
		}
		if err := w.Flush(ctx); err != nil && ctx.Err() == nil {
			w.log.WarnContext(ctx, "trail_flush_failed", "error", err)
		}
	}
}

// Flush inserts every queued row now. Rows that fail stay queued for the
// next flush.
func (w *TrailWriter) Flush(ctx context.Context) error {
	w.mu.Lock()
	rows := w.buf
	w.buf = nil
	dropped := w.dropped
	w.dropped = 0
	w.mu.Unlock()
	if dropped > 0 {
		w.log.WarnContext(ctx, "trail_rows_dropped", "rows", dropped)
	}
	if len(rows) == 0 {
		return nil
	}
	groups := map[app.Scope][]app.TrailRow{}
	var order []app.Scope
	for _, r := range rows {
		s := app.Scope{TenantID: r.TenantID, Env: r.Env}
		if _, ok := groups[s]; !ok {
			order = append(order, s)
		}
		groups[s] = append(groups[s], r)
	}
	var errs []error
	var failed []app.TrailRow
	for _, s := range order {
		if err := w.insert(ctx, s, groups[s]); err != nil {
			errs = append(errs, err)
			if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && (pgErr.Code[:2] == "22" || pgErr.Code[:2] == "23") {
				// Bad data never gets better: drop it rather than retry forever.
				w.log.ErrorContext(ctx, "trail_rows_rejected", "rows", len(groups[s]), "sqlstate", pgErr.Code)
				continue
			}
			failed = append(failed, groups[s]...)
		}
	}
	if len(failed) > 0 {
		w.Append(failed)
	}
	return errors.Join(errs...)
}

// insert writes one scope's rows; when a row has no day partition it
// creates the partitions of the rows' days and tries once more.
func (w *TrailWriter) insert(ctx context.Context, s app.Scope, rows []app.TrailRow) error {
	err := w.insertOnce(ctx, s, rows)
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); !ok || pgErr.Code != sqlstateCheckViolation {
		return err
	}
	repo := New(w.pool)
	days := map[time.Time]bool{}
	for _, r := range rows {
		d := r.Fix.T.UTC().Truncate(24 * time.Hour)
		if !days[d] {
			days[d] = true
			if err := repo.EnsurePartitions(ctx, d, 0); err != nil {
				return err
			}
		}
	}
	return w.insertOnce(ctx, s, rows)
}

const insertTrail = `INSERT INTO trail_fixes (delivery_id, tenant_id, environment, courier_id, t, lat, lng,
  accuracy_m, speed_mps, heading_deg, source)
SELECT * FROM unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::timestamptz[], $6::float8[],
  $7::float8[], $8::float8[], $9::float8[], $10::float8[], $11::text[])`

func (w *TrailWriter) insertOnce(ctx context.Context, s app.Scope, rows []app.TrailRow) error {
	n := len(rows)
	var (
		del, ten, env, cou, src = make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n)
		ts                      = make([]time.Time, n)
		lat, lng, acc           = make([]float64, n), make([]float64, n), make([]float64, n)
		spd, hdg                = make([]*float64, n), make([]*float64, n)
	)
	for i, r := range rows {
		del[i], ten[i], env[i], cou[i], src[i] = r.DeliveryID, r.TenantID, r.Env, r.CourierID, r.Fix.Source
		ts[i], lat[i], lng[i], acc[i] = r.Fix.T, r.Fix.Pos.Lat, r.Fix.Pos.Lng, r.Fix.AccuracyM
		spd[i], hdg[i] = r.Fix.SpeedMps, r.Fix.HeadingDeg
	}
	return pgx.BeginFunc(ctx, w.pool, func(tx pgx.Tx) error {
		if err := setScope(ctx, tx, s); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, insertTrail, del, ten, env, cou, ts, lat, lng, acc, spd, hdg, src); err != nil {
			return fmt.Errorf("insert trail: %w", err)
		}
		return nil
	})
}
