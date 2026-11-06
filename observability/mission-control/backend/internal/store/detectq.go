package store

import (
	"context"
	"fmt"
	"time"
)

// Fix is the newest accepted fix of a delivery.
type Fix struct {
	EventID    int64
	OccurredAt time.Time
}

// EventsObservedSince returns the newest `limit` events observed at or after
// since, oldest first, leaving out detector findings, health events and
// self-health rows (the detectors read those through their own queries).
func (s *Store) EventsObservedSince(ctx context.Context, since time.Time, limit int) ([]Event, error) {
	return s.events(ctx, "events observed since", `SELECT * FROM (SELECT `+eventCols+` FROM mc_event
		WHERE observed_at >= $1 AND kind NOT IN ('detector','health','mc_self') ORDER BY id DESC LIMIT $2) x ORDER BY id`,
		since, limit)
}

// DeliveryStatuses returns, per delivery, the newest delivery.created /
// delivery.status_changed event, for deliveries whose newest such event is at
// or after since.
func (s *Store) DeliveryStatuses(ctx context.Context, since time.Time) ([]Event, error) {
	return s.events(ctx, "delivery statuses", `SELECT `+eventCols+` FROM (
		SELECT DISTINCT ON (delivery_id) `+eventCols+` FROM mc_event
		WHERE kind = 'message_published' AND delivery_id <> ''
		  AND attrs->>'event_type' IN ('delivery.created','delivery.status_changed')
		ORDER BY delivery_id, occurred_at DESC, id DESC) x
		WHERE occurred_at >= $1 ORDER BY id`, since)
}

// AcceptedFixSQL selects the newest accepted fix per delivery for the deliveries in $1.
//
// An accepted fix is a tracking.apply state change (ok, with the delivery id
// on the span) in a trace whose tracking.ingest request accepted at least one
// fix, and with no fix_dropped log for the same delivery in that trace. The
// ingest span itself carries no delivery id (one ingest serves all of a
// courier's deliveries), so the trace links them.
const acceptedFixSQL = `SELECT DISTINCT ON (a.delivery_id) a.delivery_id, a.id, a.occurred_at
	FROM mc_event a
	WHERE a.delivery_id = ANY($1) AND a.kind = 'state_change' AND a.outcome = 'ok'
	  AND a.attrs->>'span_name' = 'tracking.apply' AND a.trace_id <> ''
	  AND EXISTS (SELECT 1 FROM mc_event i WHERE i.trace_id = a.trace_id AND i.kind = 'request'
	      AND i.attrs->>'span_name' = 'tracking.ingest'
	      AND coalesce((i.attrs->>'tracking.fixes.accepted')::numeric, 0) > 0)
	  AND NOT EXISTS (SELECT 1 FROM mc_event d WHERE d.trace_id = a.trace_id AND d.kind = 'log'
	      AND d.outcome = 'dropped' AND d.delivery_id = a.delivery_id)
	ORDER BY a.delivery_id, a.occurred_at DESC, a.id DESC`

// LastAcceptedFixes returns the newest accepted fix of each listed delivery
// that has one.
func (s *Store) LastAcceptedFixes(ctx context.Context, deliveryIDs []string) (map[string]Fix, error) {
	out := map[string]Fix{}
	if len(deliveryIDs) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, acceptedFixSQL, deliveryIDs)
	if err != nil {
		return nil, fmt.Errorf("store: last accepted fixes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var f Fix
		if err := rows.Scan(&id, &f.EventID, &f.OccurredAt); err != nil {
			return nil, fmt.Errorf("store: last accepted fixes: %w", err)
		}
		out[id] = f
	}
	return out, rows.Err()
}

// HealthSince returns health events observed at or after since, plus the
// newest health event of every (source, consumer) (health is written on
// change, so the newest one is the state still in force), oldest first.
func (s *Store) HealthSince(ctx context.Context, since time.Time) ([]Event, error) {
	return s.events(ctx, "health since", `SELECT `+eventCols+` FROM mc_event
		WHERE kind = 'health' AND (observed_at >= $1 OR id IN (
		  SELECT max(id) FROM mc_event WHERE kind = 'health' GROUP BY source, attrs->>'consumer', attrs->>'dependency'))
		ORDER BY id`, since)
}

// ActiveFindings returns the newest stored finding of every (rule, hop,
// delivery, reason) whose newest event is not a clear.
func (s *Store) ActiveFindings(ctx context.Context) ([]Event, error) {
	return s.events(ctx, "active findings", activeFindingsSQL+` ORDER BY id`)
}

// activeFindingsSQL is a SELECT of the newest finding per key that is not cleared.
const activeFindingsSQL = `SELECT ` + eventCols + ` FROM (
	SELECT DISTINCT ON (attrs->>'rule', target, delivery_id, reason) ` + eventCols + ` FROM mc_event
	WHERE kind = 'detector'
	ORDER BY attrs->>'rule', target, delivery_id, reason, id DESC) latest
	WHERE outcome <> 'cleared'`
