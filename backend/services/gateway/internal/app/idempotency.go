package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"time"

	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

// Idempotency limits fixed by spec §5.1.
const (
	// IdempotencyTTL is how long a record is kept after it is created.
	IdempotencyTTL = 24 * time.Hour
	// IdempotencyLockTTL is how long an in-progress record blocks duplicates.
	// After it the owner is presumed dead and the next request takes over.
	IdempotencyLockTTL = 60 * time.Second
	// IdempotencySweepInterval is how often expired records are deleted.
	IdempotencySweepInterval = 10 * time.Minute
	// IdempotencySweepBatch is how many expired records one DELETE removes.
	IdempotencySweepBatch = 1000
	// MaxIdempotentResponseBytes is the largest response body that is stored
	// for replay; a larger response is sent but not stored.
	MaxIdempotentResponseBytes = 64 << 10
	// MaxIdempotencyKeyLen is the longest Idempotency-Key accepted.
	MaxIdempotencyKeyLen = 255
)

// ErrIdempotencyLeaseLost is returned by Complete and Abandon when the record
// is no longer in progress under the caller's lease: it was completed, or its
// lock expired and another request took it over.
var ErrIdempotencyLeaseLost = errors.New("gateway: idempotency lease lost")

// IdempotencyScope names one idempotency record: keys are scoped per tenant
// and environment, so tenants (and test vs live) never collide.
type IdempotencyScope struct {
	TenantID string
	Env      domain.Environment
	Key      string
}

// StoredResponse is the response kept for replay. Header holds only the
// allowlisted headers the edge chooses to replay (Content-Type).
type StoredResponse struct {
	Status int
	Header map[string][]string
	Body   []byte
}

// OutcomeKind is what Begin decided for a request.
type OutcomeKind int

const (
	// OutcomeStarted means the caller owns the record and runs the request.
	OutcomeStarted OutcomeKind = iota + 1
	// OutcomeReplay means the request already completed: answer Stored.
	OutcomeReplay
	// OutcomeMismatch means the key was used with a different request.
	OutcomeMismatch
	// OutcomeInProgress means another request with the key is still running.
	OutcomeInProgress
)

func (k OutcomeKind) String() string {
	switch k {
	case OutcomeStarted:
		return "started"
	case OutcomeReplay:
		return "replay"
	case OutcomeMismatch:
		return "mismatch"
	case OutcomeInProgress:
		return "in_progress"
	}
	return "unknown"
}

// IdempotencyOutcome is Begin's answer.
type IdempotencyOutcome struct {
	Kind OutcomeKind
	// Stored is the response to replay (OutcomeReplay only).
	Stored StoredResponse
	// Lease identifies the caller's ownership (OutcomeStarted only): the
	// record's locked_until. Complete and Abandon act only while it holds,
	// so a request whose lock was taken over cannot overwrite or free the
	// new owner's record.
	Lease time.Time
}

// IdempotencyStore keeps idempotency records.
type IdempotencyStore interface {
	// Begin claims scope for a request whose hash is hash, at now. A new
	// key, an expired record or an in-progress record whose lock has passed
	// is (re)claimed: OutcomeStarted with a lease until now +
	// IdempotencyLockTTL. Otherwise a different hash is OutcomeMismatch, a
	// completed record OutcomeReplay and a live lock OutcomeInProgress.
	Begin(ctx context.Context, scope IdempotencyScope, hash []byte, now time.Time) (IdempotencyOutcome, error)
	// Complete stores resp for replay. ErrIdempotencyLeaseLost if lease no
	// longer owns the record.
	Complete(ctx context.Context, scope IdempotencyScope, lease time.Time, resp StoredResponse) error
	// Abandon deletes the record so the client may retry (5xx, panics,
	// unstorable responses). ErrIdempotencyLeaseLost if lease no longer owns
	// the record.
	Abandon(ctx context.Context, scope IdempotencyScope, lease time.Time) error
}

// ValidIdempotencyKey reports whether k is 1–255 printable ASCII characters
// (0x20–0x7E).
func ValidIdempotencyKey(k string) bool {
	if len(k) == 0 || len(k) > MaxIdempotencyKeyLen {
		return false
	}
	for i := range len(k) {
		if k[i] < 0x20 || k[i] > 0x7e {
			return false
		}
	}
	return true
}

// IdempotencyRequestHash is SHA-256(method + "\n" + path + "\n" + rawQuery +
// "\n" + body): the fingerprint that tells a replay from a key reused for
// another request.
func IdempotencyRequestHash(method, path, rawQuery string, body []byte) []byte {
	h := sha256.New()
	h.Write([]byte(method + "\n" + path + "\n" + rawQuery + "\n"))
	h.Write(body)
	return h.Sum(nil)
}

// ExpiredIdempotencyDeleter deletes expired idempotency records.
type ExpiredIdempotencyDeleter interface {
	// DeleteExpired deletes up to limit records that expired before before
	// and returns how many it deleted.
	DeleteExpired(ctx context.Context, before time.Time, limit int) (int64, error)
}

// IdempotencySweeper deletes expired idempotency records: once at start, then
// every Interval, in batches of Batch until a batch comes back short. Run is
// a bootstrap.Worker.
type IdempotencySweeper struct {
	Store ExpiredIdempotencyDeleter
	// Clock defaults to the system clock.
	Clock Clock
	// Interval defaults to IdempotencySweepInterval.
	Interval time.Duration
	// Batch defaults to IdempotencySweepBatch.
	Batch int
	// Log defaults to slog.Default().
	Log *slog.Logger
}

// Run sweeps until ctx is cancelled and then returns nil. A failed sweep is
// logged and retried at the next tick; it never stops the service.
func (s *IdempotencySweeper) Run(ctx context.Context) error {
	interval := s.Interval
	if interval <= 0 {
		interval = IdempotencySweepInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		s.sweep(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

func (s *IdempotencySweeper) sweep(ctx context.Context) {
	log := s.Log
	if log == nil {
		log = slog.Default()
	}
	batch := s.Batch
	if batch <= 0 {
		batch = IdempotencySweepBatch
	}
	now := time.Now()
	if s.Clock != nil {
		now = s.Clock.Now()
	}
	var total int64
	for ctx.Err() == nil {
		n, err := s.Store.DeleteExpired(ctx, now, batch)
		if err != nil {
			if ctx.Err() == nil {
				log.WarnContext(ctx, "idempotency_sweep_failed", "error", err.Error())
			}
			return
		}
		total += n
		if n < int64(batch) {
			break
		}
	}
	if total > 0 {
		log.InfoContext(ctx, "idempotency_swept", "deleted", total)
	}
}
