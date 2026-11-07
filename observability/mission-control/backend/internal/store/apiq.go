package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrNotFound is returned when a single row does not exist.
var ErrNotFound = errors.New("store: not found")

const eventCols = `id, observed_at, occurred_at, kind, source, target, outcome, reason,
	tenant_id, environment, delivery_id, order_ref, trace_id, span_id, origin, attrs`

func scanEvents(rows pgx.Rows, op string) ([]Event, error) {
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.ObservedAt, &e.OccurredAt, &e.Kind, &e.Source, &e.Target, &e.Outcome, &e.Reason,
			&e.TenantID, &e.Env, &e.DeliveryID, &e.OrderRef, &e.TraceID, &e.SpanID, &e.Origin, &e.Attrs); err != nil {
			return nil, fmt.Errorf("store: %s: %w", op, err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: %s: %w", op, err)
	}
	return out, nil
}

func (s *Store) events(ctx context.Context, op, sql string, args ...any) ([]Event, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("store: %s: %w", op, err)
	}
	return scanEvents(rows, op)
}

// deliveryEventIDs selects the ids of a delivery's own events plus the events
// sharing one of its trace ids ($1 = delivery id). Two index-driven branches
// (see timelineSQL).
const deliveryEventIDs = `SELECT id FROM mc_event WHERE delivery_id = $1
	UNION SELECT id FROM mc_event WHERE trace_id IN
	  (SELECT DISTINCT trace_id FROM mc_event WHERE delivery_id = $1 AND trace_id <> '')`

func clampLimit(limit int) int {
	if limit <= 0 {
		return DefaultTimelineLimit
	}
	return min(limit, MaxTimelineLimit)
}

// EventByID returns one event, or ErrNotFound.
func (s *Store) EventByID(ctx context.Context, id int64) (Event, error) {
	evs, err := s.events(ctx, "event by id", `SELECT `+eventCols+` FROM mc_event WHERE id = $1`, id)
	if err != nil {
		return Event{}, err
	}
	if len(evs) == 0 {
		return Event{}, ErrNotFound
	}
	return evs[0], nil
}

// EventsAfter returns up to limit events with id > after, oldest first (SSE
// replay without a delivery filter).
func (s *Store) EventsAfter(ctx context.Context, after int64, limit int) ([]Event, error) {
	return s.events(ctx, "events after", `SELECT `+eventCols+` FROM mc_event WHERE id > $1 ORDER BY id LIMIT $2`,
		after, clampLimit(limit))
}

// MinEventID is the oldest stored event id, or 0 when there are no events.
func (s *Store) MinEventID(ctx context.Context) (int64, error) {
	var id int64
	if err := s.pool.QueryRow(ctx, `SELECT coalesce(min(id), 0) FROM mc_event`).Scan(&id); err != nil {
		return 0, fmt.Errorf("store: min event id: %w", err)
	}
	return id, nil
}

// LogsForSpan returns the log lines of one span, oldest first.
func (s *Store) LogsForSpan(ctx context.Context, traceID, spanID string) ([]Event, error) {
	if traceID == "" || spanID == "" {
		return []Event{}, nil
	}
	return s.events(ctx, "logs for span", `SELECT `+eventCols+` FROM mc_event
		WHERE trace_id = $1 AND span_id = $2 AND kind = 'log' ORDER BY id LIMIT $3`, traceID, spanID, MaxTimelineLimit)
}

// ComponentEvents returns the newest non-log events where component is the
// source or the target, newest first. With deliveryID set, only that
// delivery's events (and its trace siblings).
func (s *Store) ComponentEvents(ctx context.Context, component, deliveryID string, limit int) ([]Event, error) {
	limit = clampLimit(limit)
	if deliveryID == "" {
		return s.events(ctx, "component events", `SELECT `+eventCols+` FROM mc_event
			WHERE id IN (SELECT id FROM mc_event WHERE source = $1 UNION SELECT id FROM mc_event WHERE target = $1)
			  AND kind NOT IN ('log','mc_self','detector') ORDER BY id DESC LIMIT $2`, component, limit)
	}
	return s.events(ctx, "component events", `SELECT `+eventCols+` FROM mc_event
		WHERE id IN (`+deliveryEventIDs+`) AND (source = $2 OR target = $2)
		  AND kind NOT IN ('log','mc_self','detector') ORDER BY id DESC LIMIT $3`, deliveryID, component, limit)
}

// severities maps a minimum level to the stored severity texts (upper case).
var severities = map[string][]string{
	"debug": {"TRACE", "DEBUG", "INFO", "WARN", "WARNING", "ERROR", "FATAL"},
	"info":  {"INFO", "WARN", "WARNING", "ERROR", "FATAL"},
	"warn":  {"WARN", "WARNING", "ERROR", "FATAL"},
	"error": {"ERROR", "FATAL"},
}

// ValidLevel reports whether level is "" or a known minimum level.
func ValidLevel(level string) bool {
	_, ok := severities[level]
	return level == "" || ok
}

// ComponentLogs returns log lines of a service (source), oldest first, after
// the given id, optionally for one delivery and at or above a level.
func (s *Store) ComponentLogs(ctx context.Context, source, deliveryID, level string, after int64, limit int) ([]Event, error) {
	limit = clampLimit(limit)
	sev := severities[level] // nil = no filter
	if deliveryID == "" {
		return s.events(ctx, "component logs", `SELECT `+eventCols+` FROM mc_event
			WHERE kind = 'log' AND source = $1 AND id > $2
			  AND ($3::text[] IS NULL OR upper(attrs->>'severity') = ANY($3))
			ORDER BY id LIMIT $4`, source, after, sev, limit)
	}
	return s.events(ctx, "component logs", `SELECT `+eventCols+` FROM mc_event
		WHERE id IN (`+deliveryEventIDs+`) AND kind = 'log' AND source = $2 AND id > $3
		  AND ($4::text[] IS NULL OR upper(attrs->>'severity') = ANY($4))
		ORDER BY id LIMIT $5`, deliveryID, source, after, sev, limit)
}

// MaxLogTail caps the tail option of ComponentLogsTail.
const MaxLogTail = 1000

// ComponentLogsTail returns the newest n log lines (n clamped to 1..MaxLogTail)
// of a service, optionally for one delivery and at or above a level, oldest
// first. It reads by id descending with a limit, so it uses the id index.
func (s *Store) ComponentLogsTail(ctx context.Context, source, deliveryID, level string, n int) ([]Event, error) {
	n = max(1, min(n, MaxLogTail))
	sev := severities[level]
	if deliveryID == "" {
		return s.events(ctx, "component logs tail", `SELECT * FROM (SELECT `+eventCols+` FROM mc_event
			WHERE kind = 'log' AND source = $1
			  AND ($2::text[] IS NULL OR upper(attrs->>'severity') = ANY($2))
			ORDER BY id DESC LIMIT $3) t ORDER BY id`, source, sev, n)
	}
	return s.events(ctx, "component logs tail", `SELECT * FROM (SELECT `+eventCols+` FROM mc_event
		WHERE id IN (`+deliveryEventIDs+`) AND kind = 'log' AND source = $2
		  AND ($3::text[] IS NULL OR upper(attrs->>'severity') = ANY($3))
		ORDER BY id DESC LIMIT $4) t ORDER BY id`, deliveryID, source, sev, n)
}

// DeliveryEventsByType returns the delivery's own events whose attrs
// event_type is one of types, oldest first.
func (s *Store) DeliveryEventsByType(ctx context.Context, deliveryID string, types []string) ([]Event, error) {
	return s.events(ctx, "delivery events by type", `SELECT `+eventCols+` FROM mc_event
		WHERE delivery_id = $1 AND attrs->>'event_type' = ANY($2) ORDER BY id LIMIT 1000`, deliveryID, types)
}

// DeliveryDetectors returns the newest 200 stored detector findings of a
// delivery, oldest first.
func (s *Store) DeliveryDetectors(ctx context.Context, deliveryID string) ([]Event, error) {
	return s.events(ctx, "delivery detectors", `SELECT * FROM (SELECT `+eventCols+` FROM mc_event
		WHERE delivery_id = $1 AND kind = 'detector' ORDER BY id DESC LIMIT 200) x ORDER BY id`, deliveryID)
}

// LatestFinding returns the newest stored finding of a rule for a delivery
// (a clear included); ok is false when there is none.
func (s *Store) LatestFinding(ctx context.Context, deliveryID, rule string) (Event, bool, error) {
	evs, err := s.events(ctx, "latest finding", `SELECT `+eventCols+` FROM mc_event
		WHERE delivery_id = $1 AND kind = 'detector' AND attrs->>'rule' = $2 ORDER BY id DESC LIMIT 1`, deliveryID, rule)
	if err != nil || len(evs) == 0 {
		return Event{}, false, err
	}
	return evs[0], true, nil
}

// DeliverySpan returns the first and last occurred_at of the delivery's own
// events; ok is false when it has none.
func (s *Store) DeliverySpan(ctx context.Context, deliveryID string) (first, last time.Time, ok bool, err error) {
	var f, l *time.Time
	if err = s.pool.QueryRow(ctx, `SELECT min(occurred_at), max(occurred_at) FROM mc_event WHERE delivery_id = $1`,
		deliveryID).Scan(&f, &l); err != nil {
		return first, last, false, fmt.Errorf("store: delivery span: %w", err)
	}
	if f == nil || l == nil {
		return first, last, false, nil
	}
	return *f, *l, true, nil
}

// RecentOrders returns the newest order index rows.
func (s *Store) RecentOrders(ctx context.Context, limit int) ([]OrderHit, error) {
	rows, err := s.pool.Query(ctx, `SELECT tenant_id, environment, order_ref, delivery_id, origin, created_at
		FROM mc_order ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: recent orders: %w", err)
	}
	defer rows.Close()
	out := []OrderHit{}
	for rows.Next() {
		var h OrderHit
		if err := rows.Scan(&h.TenantID, &h.Environment, &h.OrderRef, &h.DeliveryID, &h.Origin, &h.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: recent orders: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// RecentDetectors returns the findings that are active (the newest event of
// their rule, hop, delivery and reason is not a "cleared"), newest first. A
// finding is stored once and stays active until cleared, however old it is;
// since only drops findings whose newest event was observed before it (pass
// the zero time for all).
func (s *Store) RecentDetectors(ctx context.Context, since time.Time, limit int) ([]Event, error) {
	return s.events(ctx, "recent detectors", `SELECT `+eventCols+` FROM (`+activeFindingsSQL+`) a
		WHERE observed_at >= $1 ORDER BY id DESC LIMIT $2`, since, limit)
}

// LastAcceptedFix is the newest accepted fix of one delivery (see
// LastAcceptedFixes); ok is false when there is none.
func (s *Store) LastAcceptedFix(ctx context.Context, deliveryID string) (Fix, bool, error) {
	m, err := s.LastAcceptedFixes(ctx, []string{deliveryID}, time.Time{})
	f, ok := m[deliveryID]
	return f, ok, err
}

// LatestHealth returns the newest health event of each listed source.
func (s *Store) LatestHealth(ctx context.Context, sources []string) (map[string]Event, error) {
	evs, err := s.events(ctx, "latest health", `SELECT DISTINCT ON (source) `+eventCols+` FROM mc_event
		WHERE kind = 'health' AND source = ANY($1) ORDER BY source, id DESC`, sources)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Event, len(evs))
	for _, e := range evs {
		out[e.Source] = e
	}
	return out, nil
}

// Activity returns the last observed_at per component id (as source or
// target) among traffic events since the given time. Health, detector and
// self events are not activity.
func (s *Store) Activity(ctx context.Context, since time.Time) (map[string]time.Time, error) {
	rows, err := s.pool.Query(ctx, `SELECT c, max(t) FROM (
		SELECT source c, observed_at t FROM mc_event WHERE observed_at >= $1 AND kind NOT IN ('health','detector','mc_self')
		UNION ALL
		SELECT target c, observed_at t FROM mc_event WHERE observed_at >= $1 AND target <> '' AND kind NOT IN ('health','detector','mc_self')
	) x GROUP BY c`, since)
	if err != nil {
		return nil, fmt.Errorf("store: activity: %w", err)
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var c string
		var t time.Time
		if err := rows.Scan(&c, &t); err != nil {
			return nil, fmt.Errorf("store: activity: %w", err)
		}
		out[c] = t
	}
	return out, rows.Err()
}

// MaxEventID is the newest stored event id, or 0 when there are none.
func (s *Store) MaxEventID(ctx context.Context) (int64, error) {
	var id int64
	if err := s.pool.QueryRow(ctx, `SELECT coalesce(max(id), 0) FROM mc_event`).Scan(&id); err != nil {
		return 0, fmt.Errorf("store: max event id: %w", err)
	}
	return id, nil
}

// FirstStart is when Mission Control first started (recorded by
// RecordStartup); ok is false when it never recorded one.
func (s *Store) FirstStart(ctx context.Context) (t time.Time, ok bool, err error) {
	err = s.pool.QueryRow(ctx, `SELECT at FROM mc_meta WHERE key = 'first_start'`).Scan(&t)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, false, nil
	}
	if err != nil {
		return t, false, fmt.Errorf("store: first start: %w", err)
	}
	return t, true, nil
}
