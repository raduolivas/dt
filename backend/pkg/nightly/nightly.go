// Package nightly runs a service's maintenance job (retention) once a night
// at 02:00 UTC, on the one instance that takes the job's lease.
package nightly

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// HourUTC is the hour of the day, UTC, the job runs at.
	HourUTC = 2
	// RunTimeout bounds one run.
	RunTimeout = 50 * time.Minute
	// LeaseTTL is how long the lease is held. It is not released after the
	// run and outlives it, so the other instances, waking at the same time,
	// do not run the job again that night.
	LeaseTTL = time.Hour
)

// Lease is a lock with a time to live, held by one instance at a time.
type Lease interface {
	// Hold takes the lease for holder, or renews it when holder has it, for
	// ttl. It reports whether holder has the lease.
	Hold(ctx context.Context, holder string, ttl time.Duration) (bool, error)
}

// Job is a nightly job. Run is a bootstrap.Worker.
type Job struct {
	// Name names the job in logs.
	Name   string
	Lease  Lease
	Holder string // unique per process
	// Do does the work for the night of now. Jobs are idempotent: a run
	// that fails is simply done again the next night.
	Do func(ctx context.Context, now time.Time) error
	// Now defaults to time.Now.
	Now func() time.Time
	// Log defaults to slog.Default().
	Log *slog.Logger
}

// NewHolder returns a lease holder name unique to this process.
func NewHolder() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("nightly: holder id: %w", err)
	}
	host, _ := os.Hostname()
	return host + "-" + hex.EncodeToString(b), nil
}

// Next returns the first 02:00 UTC strictly after now.
func Next(now time.Time) time.Time {
	now = now.UTC()
	at := time.Date(now.Year(), now.Month(), now.Day(), HourUTC, 0, 0, 0, time.UTC)
	if !at.After(now) {
		at = at.AddDate(0, 0, 1)
	}
	return at
}

func (j *Job) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

func (j *Job) log() *slog.Logger {
	if j.Log != nil {
		return j.Log
	}
	return slog.Default()
}

// Tick takes the lease and, when this instance has it, runs the job once
// (bounded by RunTimeout). It reports whether the job ran.
func (j *Job) Tick(ctx context.Context) (bool, error) {
	held, err := j.Lease.Hold(ctx, j.Holder, LeaseTTL)
	if err != nil {
		return false, fmt.Errorf("%s lease: %w", j.Name, err)
	}
	if !held {
		return false, nil
	}
	rctx, cancel := context.WithTimeout(ctx, RunTimeout)
	defer cancel()
	return true, j.Do(rctx, j.now())
}

// Run ticks at every 02:00 UTC until ctx ends, then returns nil. A failed
// run is logged (errors only, never ids) and retried the next night.
func (j *Job) Run(ctx context.Context) error {
	for {
		t := time.NewTimer(time.Until(Next(j.now())))
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
		if _, err := j.Tick(ctx); err != nil && ctx.Err() == nil {
			j.log().WarnContext(ctx, "nightly_job_failed", "job", j.Name, "error", err.Error())
		}
	}
}

// SchemaSQL creates the job_leases table PostgresLease uses. Services copy
// it verbatim into a migration; tests may execute it directly.
const SchemaSQL = `CREATE TABLE job_leases (name text PRIMARY KEY, holder text NOT NULL, expires_at timestamptz NOT NULL);
`

// PostgresLease is a Lease on one row of job_leases, timed by the database
// clock.
type PostgresLease struct {
	pool *pgxpool.Pool
	name string
}

var _ Lease = (*PostgresLease)(nil)

// NewPostgresLease returns the lease named name.
func NewPostgresLease(pool *pgxpool.Pool, name string) *PostgresLease {
	return &PostgresLease{pool: pool, name: name}
}

const holdLease = `INSERT INTO job_leases (name, holder, expires_at)
VALUES ($1, $2, now() + make_interval(secs => $3))
ON CONFLICT (name) DO UPDATE SET holder = EXCLUDED.holder, expires_at = EXCLUDED.expires_at
WHERE job_leases.expires_at <= now() OR job_leases.holder = EXCLUDED.holder
RETURNING true`

// Hold implements Lease.
func (l *PostgresLease) Hold(ctx context.Context, holder string, ttl time.Duration) (bool, error) {
	var ok bool
	err := l.pool.QueryRow(ctx, holdLease, l.name, holder, ttl.Seconds()).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("hold lease: %w", err)
	}
	return ok, nil
}
