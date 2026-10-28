package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Retention (M3 design §4.2).
const (
	// RecordRetentionMonths is how long a delivery, its history and its
	// pickup confirmations are kept after it reached a final status.
	RecordRetentionMonths = 13
	// RetentionBatch is how many deliveries one delete removes.
	RetentionBatch = 500
)

// RetentionStore is what the nightly retention job needs of the store.
// Tenant data is reached one scope at a time.
type RetentionStore interface {
	// RetentionScopes returns every scope that has deliveries.
	RetentionScopes(ctx context.Context) ([]Scope, error)
	// DeleteFinalBefore deletes up to limit of the scope's deliveries that
	// reached a final status before before, with their history and pickup
	// watch, and returns how many it deleted.
	DeleteFinalBefore(ctx context.Context, s Scope, before time.Time, limit int) (int64, error)
}

func (s *Service) log() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return s.Log
}

// RunRetention is the nightly retention job for the night of now: it
// deletes the deliveries final for more than RecordRetentionMonths, scope
// by scope, in batches. It is idempotent; one scope's failure does not stop
// the others. It logs counts only, never ids.
func (s *Service) RunRetention(ctx context.Context, now time.Time) error {
	scopes, err := s.Retention.RetentionScopes(ctx)
	if err != nil {
		return fmt.Errorf("list scopes: %w", err)
	}
	before := now.UTC().AddDate(0, -RecordRetentionMonths, 0)
	var errs []error
	var deleted int64
	for _, sc := range scopes {
		for {
			n, err := s.Retention.DeleteFinalBefore(ctx, sc, before, RetentionBatch)
			deleted += n
			if err != nil {
				errs = append(errs, fmt.Errorf("delete expired deliveries: %w", err))
				break
			}
			if n < RetentionBatch {
				break
			}
		}
	}
	s.log().InfoContext(ctx, "deliveries_retention_done", "scopes", len(scopes), "deleted", deleted,
		"scopes_failed", len(errs))
	return errors.Join(errs...)
}
