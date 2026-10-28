package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

// Retention (M3 design §4.2).
const (
	// RawTrailRetention is how long raw fixes are kept before the trail is
	// replaced by its simplified route (the tenant setting is M7).
	RawTrailRetention = 30 * 24 * time.Hour
	// RecordRetentionMonths is how long simplified routes, proof points and
	// delivery records are kept after the delivery is final.
	RecordRetentionMonths = 13
	// SimplifyToleranceM is the Douglas-Peucker tolerance of a simplified
	// route, in metres.
	SimplifyToleranceM = 5
	// PartitionsAhead is how many days of trail partitions exist beyond
	// today.
	PartitionsAhead = 3
)

// RunRetention is the nightly retention job for the night of now. It
// creates the trail partitions PartitionsAhead days ahead; deletes any raw
// fix of an erased delivery; stores a
// simplified route for every delivery with raw fixes older than
// RawTrailRetention and none yet; then, only if every such trail was
// simplified (no delivery loses its route), drops the day partitions whose
// fixes are all older than RawTrailRetention; and deletes the records of
// deliveries final for more than RecordRetentionMonths. It is idempotent.
// It logs counts only, never ids or positions.
func (s *Service) RunRetention(ctx context.Context, now time.Time) error {
	now = now.UTC()
	r := s.Retention
	var errs []error
	if err := r.EnsurePartitions(ctx, now, PartitionsAhead); err != nil {
		errs = append(errs, fmt.Errorf("ensure partitions: %w", err))
	}
	scopes, err := r.RetentionScopes(ctx)
	if err != nil {
		return errors.Join(append(errs, fmt.Errorf("list scopes: %w", err))...)
	}

	rawCutoff := now.Add(-RawTrailRetention)
	simplified, unsimplified := 0, 0
	var erasedFixes int64
	for _, sc := range scopes {
		// Raw fixes of an erased delivery never stay (a fix flushed while
		// the erasure committed); erasures older than the raw retention
		// have none left.
		e, err := r.DeleteErasedTrail(ctx, sc, rawCutoff)
		erasedFixes += e
		if err != nil {
			errs = append(errs, fmt.Errorf("delete erased trails: %w", err))
		}
		n, err := s.simplifyBefore(ctx, sc, rawCutoff, now)
		simplified += n
		if err != nil {
			unsimplified++
			errs = append(errs, err)
		}
	}
	dropped := 0
	if unsimplified == 0 {
		// A partition is dropped once its last fix is past the cutoff.
		if dropped, err = r.DropTrailPartitionsBefore(ctx, rawCutoff.Truncate(24*time.Hour)); err != nil {
			errs = append(errs, fmt.Errorf("drop trail partitions: %w", err))
		}
	}

	finalCutoff := now.AddDate(0, -RecordRetentionMonths, 0)
	var deleted int64
	failedScopes := 0
	for _, sc := range scopes {
		n, err := r.DeleteFinalBefore(ctx, sc, finalCutoff)
		deleted += n
		if err != nil {
			failedScopes++
			errs = append(errs, fmt.Errorf("delete expired records: %w", err))
		}
	}

	s.log().InfoContext(ctx, "tracking_retention_done", "scopes", len(scopes), "simplified", simplified,
		"erased_fixes_deleted", erasedFixes, "partitions_dropped", dropped, "deliveries_deleted", deleted,
		"scopes_not_simplified", unsimplified, "scopes_not_deleted", failedScopes)
	return errors.Join(errs...)
}

// simplifyBefore stores the simplified route of each of the scope's
// deliveries with raw fixes before cutoff and no route yet. It returns how
// many it stored; an error means some trail of the scope is not simplified.
func (s *Service) simplifyBefore(ctx context.Context, sc Scope, cutoff, now time.Time) (int, error) {
	ids, err := s.Retention.UnsimplifiedBefore(ctx, sc, cutoff)
	if err != nil {
		return 0, fmt.Errorf("list trails to simplify: %w", err)
	}
	n := 0
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		pts, err := s.Retention.TrailPoints(ctx, sc, id)
		if err != nil {
			return n, fmt.Errorf("read trail: %w", err)
		}
		if len(pts) == 0 {
			continue
		}
		if err := s.Retention.PutSimplifiedRoute(ctx, sc, id, domain.Simplify(pts, SimplifyToleranceM), now); err != nil {
			return n, fmt.Errorf("store simplified route: %w", err)
		}
		n++
	}
	return n, nil
}

// replaceTrail replaces the delivery's raw fixes with its simplified route
// (erasure). Repeating it is harmless: with a route already stored, any raw
// fix left is deleted and the route is kept.
func replaceTrail(ctx context.Context, p Projection, id string, at time.Time) error {
	pts, err := p.TrailPoints(ctx, id)
	if err != nil {
		return fmt.Errorf("read trail: %w", err)
	}
	if len(pts) == 0 {
		return nil
	}
	if err := p.ReplaceTrail(ctx, id, domain.Simplify(pts, SimplifyToleranceM), at); err != nil {
		return fmt.Errorf("replace trail: %w", err)
	}
	return nil
}
