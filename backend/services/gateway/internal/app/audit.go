package app

import (
	"context"
	"log/slog"
	"net/netip"
	"time"

	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

// Audit retention fixed by spec §11.1.
const (
	// AuditRetentionPeriod is how long audit entries are kept.
	AuditRetentionPeriod = 365 * 24 * time.Hour
	// AuditRetentionInterval is how often entries past retention are deleted.
	AuditRetentionInterval = 24 * time.Hour
	// AuditRetentionBatch is how many entries one DELETE removes.
	AuditRetentionBatch = 1000
)

// AuditEntry is one authenticated non-GET request (spec §11.1): who did what
// to which target, from where, with which outcome.
type AuditEntry struct {
	TenantID  string
	Env       domain.Environment
	ActorType string // credential type, e.g. "secret_key"
	ActorID   string // credential id, e.g. ak_…
	Action    string // route name, e.g. "deliveries.create"
	Target    string // the route's path value; "" when it has none
	Status    int    // the status the client received
	IP        netip.Addr
	UserAgent string
	RequestID string
	At        time.Time
}

// AuditStore appends audit entries. The log is append-only: entries are never
// updated, only deleted by AuditRetention once past retention.
type AuditStore interface {
	Record(ctx context.Context, e AuditEntry) error
}

// AuditDeleter deletes audit entries past retention.
type AuditDeleter interface {
	// DeleteAuditBefore deletes up to limit entries that occurred before
	// before and returns how many it deleted.
	DeleteAuditBefore(ctx context.Context, before time.Time, limit int) (int64, error)
}

// AuditRetention deletes audit entries older than AuditRetentionPeriod: once
// at start, then every Interval, in batches of Batch until a batch comes back
// short. Run is a bootstrap.Worker.
type AuditRetention struct {
	Store AuditDeleter
	// Clock defaults to the system clock.
	Clock Clock
	// Interval defaults to AuditRetentionInterval.
	Interval time.Duration
	// Batch defaults to AuditRetentionBatch.
	Batch int
	// Log defaults to slog.Default().
	Log *slog.Logger
}

// Run sweeps until ctx is cancelled and then returns nil. A failed sweep is
// logged and retried at the next tick; it never stops the service.
func (a *AuditRetention) Run(ctx context.Context) error {
	interval := a.Interval
	if interval <= 0 {
		interval = AuditRetentionInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		a.sweep(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

func (a *AuditRetention) sweep(ctx context.Context) {
	log := a.Log
	if log == nil {
		log = slog.Default()
	}
	batch := a.Batch
	if batch <= 0 {
		batch = AuditRetentionBatch
	}
	now := time.Now()
	if a.Clock != nil {
		now = a.Clock.Now()
	}
	cutoff := now.Add(-AuditRetentionPeriod)
	var total int64
	for ctx.Err() == nil {
		n, err := a.Store.DeleteAuditBefore(ctx, cutoff, batch)
		if err != nil {
			if ctx.Err() == nil {
				log.WarnContext(ctx, "audit_retention_failed", "error", err.Error())
			}
			return
		}
		total += n
		if n < int64(batch) {
			break
		}
	}
	if total > 0 {
		log.InfoContext(ctx, "audit_retention_deleted", "deleted", total)
	}
}
