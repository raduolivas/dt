// Package store is Mission Control's Postgres store: events, orders,
// self-health and downtime windows (spec §5).
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/raduolivas/dt/backend/pkg/migratex"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store/migrations"
)

// selfSourceMC is the mc_self row holding Mission Control's own heartbeat.
const selfSourceMC = "mc"

// Event mirrors mc_event. Attrs is allow-listed JSON; nil stores {}.
type Event struct {
	ID         int64
	ObservedAt time.Time
	OccurredAt time.Time
	Kind       string
	Source     string
	Target     string
	Outcome    string
	Reason     string
	TenantID   string
	Env        string // column environment
	DeliveryID string
	OrderRef   string
	TraceID    string
	SpanID     string
	Origin     string
	Attrs      []byte
}

// Store wraps the connection pool.
type Store struct{ pool *pgxpool.Pool }

// Open applies the migrations, then connects. Errors never include the URL.
func Open(ctx context.Context, url string) (*Store, error) {
	if err := migratex.Up(ctx, url, migrations.FS); err != nil {
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, errors.New("store: connect: invalid database URL")
	}
	s := &Store{pool: pool}
	if err := s.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the pool.
func (s *Store) Close() { s.pool.Close() }

// Ping reports whether Postgres answers.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("store: ping: %w", err)
	}
	return nil
}

const insertEvent = `INSERT INTO mc_event
 (observed_at, occurred_at, kind, source, target, outcome, reason, tenant_id, environment,
  delivery_id, order_ref, trace_id, span_id, origin, attrs)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`

// InsertEvents writes the events in one batch (one transaction).
func (s *Store) InsertEvents(ctx context.Context, events []Event) error {
	if len(events) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for _, e := range events {
		attrs := e.Attrs
		if len(attrs) == 0 {
			attrs = []byte("{}")
		}
		b.Queue(insertEvent, e.ObservedAt, e.OccurredAt, e.Kind, e.Source, e.Target, e.Outcome, e.Reason,
			e.TenantID, e.Env, e.DeliveryID, e.OrderRef, e.TraceID, e.SpanID, e.Origin, attrs)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: insert events: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	res := tx.SendBatch(ctx, b)
	for range events {
		if _, err := res.Exec(); err != nil {
			_ = res.Close()
			return fmt.Errorf("store: insert events: %w", err)
		}
	}
	if err := res.Close(); err != nil {
		return fmt.Errorf("store: insert events: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: insert events: %w", err)
	}
	return nil
}

// DeleteOlderThan removes events observed before cutoff and reports how many.
func (s *Store) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM mc_event WHERE observed_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("store: delete old events: %w", err)
	}
	return tag.RowsAffected(), nil
}

// CountEvents returns the number of stored events.
func (s *Store) CountEvents(ctx context.Context) (int64, error) {
	return s.count(ctx, `SELECT count(*) FROM mc_event`)
}

// CountDowntime returns the number of recorded downtime windows.
func (s *Store) CountDowntime(ctx context.Context) (int64, error) {
	return s.count(ctx, `SELECT count(*) FROM mc_downtime`)
}

func (s *Store) count(ctx context.Context, q string) (int64, error) {
	var n int64
	if err := s.pool.QueryRow(ctx, q).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count: %w", err)
	}
	return n, nil
}

// Heartbeat records that Mission Control was alive at now.
func (s *Store) Heartbeat(ctx context.Context, now time.Time) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO mc_self (source, last_seen) VALUES ($1, $2)
		ON CONFLICT (source) DO UPDATE SET last_seen = EXCLUDED.last_seen`, selfSourceMC, now)
	if err != nil {
		return fmt.Errorf("store: heartbeat: %w", err)
	}
	return nil
}

// RecordStartup writes the gap between the last heartbeat and now as a
// downtime window. With no earlier heartbeat (first start) it writes nothing.
func (s *Store) RecordStartup(ctx context.Context, now time.Time) error {
	var last time.Time
	err := s.pool.QueryRow(ctx, `SELECT last_seen FROM mc_self WHERE source = $1`, selfSourceMC).Scan(&last)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: record startup: %w", err)
	}
	if !last.Before(now) {
		return nil
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO mc_downtime (started_at, ended_at) VALUES ($1, $2)`, last, now); err != nil {
		return fmt.Errorf("store: record startup: %w", err)
	}
	return nil
}
