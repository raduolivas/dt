// Package detect names the hop, the order, the reason and the evidence when
// activity goes missing or fails (spec §7). Findings are stored as
// kind=detector events so the API, the live stream and replay show them.
//
// Dedup: a finding with the same (detector, hop, delivery, reason) is stored
// and emitted once. It is stored again only when its severity changes, and a
// finding that stops holding is stored as severity "cleared" (outcome
// "cleared" on the event) so the UI and the API stop showing it. If it holds
// again later it is a new finding.
package detect

import (
	"context"
	"time"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
)

// Severities. "cleared" is stored when a finding stops holding.
const (
	SevWarning = "warning"
	SevFailure = "failure"
	SevCleared = "cleared"
)

// Finding is one detector result. Evidence holds mc_event ids.
type Finding struct {
	Detector, Hop, DeliveryID, Reason, Severity string
	Since                                       time.Time
	Evidence                                    []int64
	// Attrs are extra details stored in the event's attrs (counts, status, consumer).
	Attrs map[string]any
}

// Source is what the engine reads and writes (storeSource over *store.Store).
type Source interface {
	// Window returns non-detector, non-health events observed since, oldest first.
	Window(ctx context.Context, since time.Time) ([]store.Event, error)
	// Statuses returns the newest delivery.created / delivery.status_changed event per delivery changed since.
	Statuses(ctx context.Context, since time.Time) ([]store.Event, error)
	// LastFixes returns the newest accepted fix per delivery id.
	LastFixes(ctx context.Context, deliveryIDs []string) (map[string]store.Fix, error)
	// Health returns health events observed since plus the newest per (source, consumer).
	Health(ctx context.Context, since time.Time) ([]store.Event, error)
	Self(ctx context.Context) ([]store.SelfRow, error)
	// Active returns the newest stored finding of every key that is not cleared.
	Active(ctx context.Context) ([]store.Event, error)
	Insert(ctx context.Context, evs []store.Event) error
}

// storeSource adapts *store.Store to Source.
type storeSource struct{ st *store.Store }

func (s storeSource) Window(ctx context.Context, since time.Time) ([]store.Event, error) {
	return s.st.EventsObservedSince(ctx, since, windowLimit)
}
func (s storeSource) Statuses(ctx context.Context, since time.Time) ([]store.Event, error) {
	return s.st.DeliveryStatuses(ctx, since)
}
func (s storeSource) LastFixes(ctx context.Context, ids []string) (map[string]store.Fix, error) {
	return s.st.LastAcceptedFixes(ctx, ids)
}
func (s storeSource) Health(ctx context.Context, since time.Time) ([]store.Event, error) {
	return s.st.HealthSince(ctx, since)
}
func (s storeSource) Self(ctx context.Context) ([]store.SelfRow, error) { return s.st.SelfRows(ctx) }
func (s storeSource) Active(ctx context.Context) ([]store.Event, error) {
	return s.st.ActiveFindings(ctx)
}
func (s storeSource) Insert(ctx context.Context, evs []store.Event) error {
	return s.st.InsertEvents(ctx, evs)
}

// windowLimit caps the events read per tick (newest are kept by the query).
const windowLimit = 50000
