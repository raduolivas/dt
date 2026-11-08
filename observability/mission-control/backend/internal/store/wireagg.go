package store

import (
	"context"
	"fmt"
	"time"
)

// WireAgg is aggregate request/message traffic on one (source, target) pair.
type WireAgg struct {
	Source, Target string
	Count, Failed  int64
	P95ms          float64
}

// WireAggregates aggregates traffic events observed since the given time per
// (source, target). It uses the mc_event (source, target, observed_at) index
// and ignores rows without a target; p95 uses numeric attrs.duration_ms only.
func (s *Store) WireAggregates(ctx context.Context, since time.Time) ([]WireAgg, error) {
	rows, err := s.pool.Query(ctx, `SELECT source, target, count(*), count(*) FILTER (WHERE outcome = 'failed'),
		coalesce(percentile_cont(0.95) WITHIN GROUP (ORDER BY
		  CASE WHEN jsonb_typeof(attrs->'duration_ms') = 'number' THEN (attrs->>'duration_ms')::float8 END), 0)
		FROM mc_event
		WHERE observed_at >= $1 AND target <> '' AND kind IN ('request','message_published','message_consumed')
		GROUP BY source, target`, since)
	if err != nil {
		return nil, fmt.Errorf("store: wire aggregates: %w", err)
	}
	defer rows.Close()
	var out []WireAgg
	for rows.Next() {
		var a WireAgg
		if err := rows.Scan(&a.Source, &a.Target, &a.Count, &a.Failed, &a.P95ms); err != nil {
			return nil, fmt.Errorf("store: wire aggregates: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
