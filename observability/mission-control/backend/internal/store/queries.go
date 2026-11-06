package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	// DefaultTimelineLimit applies when the caller passes limit <= 0.
	DefaultTimelineLimit = 100
	// MaxTimelineLimit caps a timeline page.
	MaxTimelineLimit = 500
	maxSearchHits    = 50
)

// OrderHit is one order index row.
type OrderHit struct {
	TenantID    string
	Environment string
	OrderRef    string
	DeliveryID  string
	Origin      string
	CreatedAt   time.Time
}

// Interval is a time window (for example, Mission Control downtime).
type Interval struct {
	Start time.Time
	End   time.Time
}

// UpsertOrder records external_id -> delivery_id for a tenant and environment.
// A stored delivery_id is kept; only an empty one is filled in.
func (s *Store) UpsertOrder(ctx context.Context, o OrderHit) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO mc_order (tenant_id, environment, order_ref, delivery_id, created_at, origin)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (tenant_id, environment, order_ref) DO UPDATE
		SET delivery_id = EXCLUDED.delivery_id, origin = EXCLUDED.origin
		WHERE mc_order.delivery_id = ''`,
		o.TenantID, o.Environment, o.OrderRef, o.DeliveryID, o.CreatedAt, o.Origin)
	if err != nil {
		return fmt.Errorf("store: upsert order: %w", err)
	}
	return nil
}

// SearchOrder finds orders by exact order_ref, or by delivery_id when q starts
// with "del_". Several hits mean the same order number exists in several
// tenants or environments.
func (s *Store) SearchOrder(ctx context.Context, q string) ([]OrderHit, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	col := "order_ref"
	if strings.HasPrefix(q, "del_") {
		col = "delivery_id"
	}
	rows, err := s.pool.Query(ctx, `SELECT tenant_id, environment, order_ref, delivery_id, origin, created_at
		FROM mc_order WHERE `+col+` = $1 ORDER BY created_at DESC, tenant_id, environment LIMIT $2`, q, maxSearchHits)
	if err != nil {
		return nil, fmt.Errorf("store: search order: %w", err)
	}
	defer rows.Close()
	var out []OrderHit
	for rows.Next() {
		var h OrderHit
		if err := rows.Scan(&h.TenantID, &h.Environment, &h.OrderRef, &h.DeliveryID, &h.Origin, &h.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: search order: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: search order: %w", err)
	}
	return out, nil
}

const timelineCols = `id, observed_at, occurred_at, kind, source, target, outcome, reason,
	tenant_id, environment, delivery_id, order_ref, trace_id, span_id, origin, attrs`

// timelineSQL has two index-driven branches (delivery_id index; trace_id
// index via the delivery's trace ids), merged by id. An OR around an
// IN-subquery would not use the indexes.
const timelineSQL = `
	(SELECT ` + timelineCols + ` FROM mc_event WHERE delivery_id = $1 AND id > $2 ORDER BY id LIMIT $3)
	UNION
	(SELECT ` + timelineCols + ` FROM mc_event
	  WHERE trace_id IN (SELECT DISTINCT trace_id FROM mc_event WHERE delivery_id = $1 AND trace_id <> '')
	    AND id > $2 ORDER BY id LIMIT $3)
	ORDER BY id LIMIT $3`

// ExplainTimeline returns the EXPLAIN (ANALYZE) plan of the Timeline query
// (for the index test; not part of the API).
func (s *Store) ExplainTimeline(ctx context.Context, deliveryID string, after int64, limit int) (string, error) {
	if _, err := s.pool.Exec(ctx, "ANALYZE mc_event"); err != nil {
		return "", fmt.Errorf("store: explain timeline: %w", err)
	}
	rows, err := s.pool.Query(ctx, "EXPLAIN (ANALYZE) "+timelineSQL, deliveryID, after, limit+1)
	if err != nil {
		return "", fmt.Errorf("store: explain timeline: %w", err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			return "", fmt.Errorf("store: explain timeline: %w", err)
		}
		b.WriteString(l + "\n")
	}
	return b.String(), rows.Err()
}

// Timeline returns the events of a delivery plus the events that share one of
// its trace ids (spans carry no delivery id), ordered by id, after the given
// id. next is the id to pass as after for the following page, or 0 when there
// are no more events. limit is clamped to 1..MaxTimelineLimit (0 = default).
func (s *Store) Timeline(ctx context.Context, deliveryID string, after int64, limit int) ([]Event, int64, error) {
	if limit <= 0 {
		limit = DefaultTimelineLimit
	}
	limit = min(limit, MaxTimelineLimit)
	if deliveryID == "" {
		return nil, 0, nil
	}
	rows, err := s.pool.Query(ctx, timelineSQL, deliveryID, after, limit+1)
	if err != nil {
		return nil, 0, fmt.Errorf("store: timeline: %w", err)
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.ObservedAt, &e.OccurredAt, &e.Kind, &e.Source, &e.Target, &e.Outcome, &e.Reason,
			&e.TenantID, &e.Env, &e.DeliveryID, &e.OrderRef, &e.TraceID, &e.SpanID, &e.Origin, &e.Attrs); err != nil {
			return nil, 0, fmt.Errorf("store: timeline: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("store: timeline: %w", err)
	}
	var next int64
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].ID
	}
	return out, next, nil
}

// Downtime returns the Mission Control downtime windows that overlap
// [from, to], oldest first.
func (s *Store) Downtime(ctx context.Context, from, to time.Time) ([]Interval, error) {
	rows, err := s.pool.Query(ctx, `SELECT started_at, ended_at FROM mc_downtime
		WHERE ended_at >= $1 AND started_at <= $2 ORDER BY started_at`, from, to)
	if err != nil {
		return nil, fmt.Errorf("store: downtime: %w", err)
	}
	defer rows.Close()
	var out []Interval
	for rows.Next() {
		var iv Interval
		if err := rows.Scan(&iv.Start, &iv.End); err != nil {
			return nil, fmt.Errorf("store: downtime: %w", err)
		}
		out = append(out, iv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: downtime: %w", err)
	}
	return out, nil
}

// DeleteOrdersOlderThan removes order index rows created before cutoff whose
// delivery has no stored events left (run it after DeleteOlderThan), so an
// order stays searchable while its timeline exists.
func (s *Store) DeleteOrdersOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM mc_order o WHERE o.created_at < $1
		AND (o.delivery_id = '' OR NOT EXISTS (SELECT 1 FROM mc_event e WHERE e.delivery_id = o.delivery_id))`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("store: delete old orders: %w", err)
	}
	return tag.RowsAffected(), nil
}

// DeleteDowntimeOlderThan removes downtime windows that ended before cutoff.
func (s *Store) DeleteDowntimeOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM mc_downtime WHERE ended_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("store: delete old downtime: %w", err)
	}
	return tag.RowsAffected(), nil
}
