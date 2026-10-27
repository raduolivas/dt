package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

// Event types on the TRACKING stream.
const (
	EventArrivalDetected  = "tracking.arrival_detected"
	EventEtaChanged       = "tracking.eta_changed"
	EventLocationReported = "courier.location_reported"
	EventSignalLost       = "tracking.courier_signal_lost"
	EventSignalRestored   = "tracking.courier_signal_restored"
)

// casAttempts is the first try plus three retries on a version conflict.
const casAttempts = 4

// ETA event rules (MVP spec §5.6): a change of at least 2 minutes, at most
// once a minute.
const (
	etaEventMinChange   = 120 * time.Second
	etaEventMinInterval = time.Minute
)

// enqueueTimeout bounds the outbox writes of one Apply.
const enqueueTimeout = 300 * time.Millisecond

// Service holds the tracking use cases.
type Service struct {
	States  StateStore
	Reads   ReadModel
	Trail   TrailWriter
	Pub     Publisher
	Events  Events
	Sampler Sampler
	// Router is optional: without one no routes are built and the ETA
	// stays unknown until a route is known.
	Router Router
	Clock  Clock
	Log    *slog.Logger
}

func (s *Service) log() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return s.Log
}

// Apply changes one delivery's live state: it loads it (from the state
// store, else rebuilt from the read model), runs mutate, gives it the next
// version and stores it with a compare-and-set, retrying on a conflict. The
// viewer snapshot is published, with a new viewer version and updated_at,
// only when its content changed; arrival and ETA events are enqueued when
// due. A mutate that returns ErrNoChange ends Apply with nil.
func (s *Service) Apply(ctx context.Context, sc Scope, deliveryID string, mutate func(*domain.State) error) error {
	return s.ApplyWith(ctx, s.Reads, sc, deliveryID, mutate)
}

// ApplyWith is Apply with the read model used for a rebuild, such as a
// consumer's transaction-bound projection.
func (s *Service) ApplyWith(ctx context.Context, reads ReadModel, sc Scope, deliveryID string, mutate func(*domain.State) error) error {
	// The router is asked outside the compare-and-set: its answers are kept
	// for the retries.
	memo := &routeMemo{}
	for range casAttempts {
		done, err := s.applyOnce(ctx, reads, memo, sc, deliveryID, mutate)
		if err != nil || done {
			return err
		}
	}
	return fmt.Errorf("apply %s: %w", deliveryID, ErrConflict)
}

type pendingEvent struct {
	typ string
	msg proto.Message
	// undo reverts the event's bookkeeping in the state, so an event that
	// could not be enqueued is due again on the next Apply.
	undo func(*domain.State)
}

func (s *Service) applyOnce(ctx context.Context, reads ReadModel, memo *routeMemo, sc Scope, id string, mutate func(*domain.State) error) (bool, error) {
	st, ok, err := s.States.Get(ctx, id)
	if err != nil {
		return false, fmt.Errorf("get state %s: %w", id, err)
	}
	rebuilt := false
	if !ok {
		if st, err = reads.LoadState(ctx, sc, id); err != nil {
			return false, err
		}
		rebuilt = true
	}
	if st.TenantID != sc.TenantID || st.Env != sc.Env {
		return false, ErrNotFound
	}
	now := s.Clock.Now()
	prevVersion, prevUpdated := st.Version, st.UpdatedAt
	before, err := viewerContent(st, now)
	if err != nil {
		return false, err
	}
	if err := mutate(&st); errors.Is(err, ErrNoChange) && !rebuilt {
		return true, nil
	} else if err != nil && !errors.Is(err, ErrNoChange) {
		return false, err
	}
	s.refreshRoute(ctx, memo, &st, now)
	st.Signal = domain.ViewerSignal(st, now)
	st.Version = domain.NextVersion(prevVersion, now)
	after, err := viewerContent(st, now)
	if err != nil {
		return false, err
	}
	// A rebuilt state has lost its viewer version: publish it again with a
	// fresh one, which is greater than any published before (it follows the
	// clock).
	viewerChanged := rebuilt || before != after
	if viewerChanged {
		st.ViewerVersion, st.UpdatedAt = st.Version, now
	} else {
		st.UpdatedAt = prevUpdated
	}
	events := dueEvents(&st, now)

	// Events are enqueued before the store, so a version conflict can at
	// most duplicate one (deliveries ignores a repeated arriving). The
	// outbox is Postgres, which must never hold up the viewer path: an
	// event that cannot be enqueued has its bookkeeping undone and is due
	// again on the next Apply, while the state is stored and published.
	// The whole enqueue is bounded, so a hung Postgres delays the store and
	// the publish by at most enqueueTimeout.
	ectx, cancel := context.WithTimeout(ctx, enqueueTimeout)
	defer cancel()
	for _, e := range events {
		if err := s.Events.Enqueue(ectx, e.typ, st.TenantID, st.Env, Subject{DeliveryID: id}, e.msg); err != nil {
			e.undo(&st)
			s.log().WarnContext(ctx, "tracking_event_enqueue_failed", "delivery_id", id, "event_type", e.typ, "error", err)
		}
	}
	stored, err := s.States.Put(ctx, st, prevVersion)
	if err != nil {
		return false, fmt.Errorf("put state %s: %w", id, err)
	}
	if !stored {
		return false, nil
	}
	// From here on Apply never fails: the state advanced, and an error would
	// make Ingest drop the trail rows of fixes that were accepted.
	s.trackActive(ctx, sc, st)
	if viewerChanged {
		// Best effort: a missed publish is repaired by the next change and
		// by viewers reading the snapshot on (re)connect.
		if err := s.Pub.Snapshot(ctx, id, domain.Snapshot(st, now)); err != nil {
			s.log().WarnContext(ctx, "snapshot_publish_failed", "delivery_id", id, "error", err)
		}
	}
	return true, nil
}

// viewerContent is the viewer snapshot at now without its version and
// updated_at, plus the stored signal, for change detection. The snapshot's
// signal follows the clock, so before and after a mutation (both at now)
// it is the same; the stored signal is what was last published, so a
// signal that changed with time alone still counts as a change.
func viewerContent(st domain.State, now time.Time) (string, error) {
	st.ViewerVersion, st.UpdatedAt = 0, time.Time{}
	b, err := json.Marshal(domain.Snapshot(st, now))
	if err != nil {
		return "", fmt.Errorf("marshal snapshot: %w", err)
	}
	return string(b) + "|" + string(st.Signal), nil
}

// trackActive keeps the delivery's dt:active entry in step with st. A
// failure is logged: the sweeper drops stale entries, and the next Apply
// writes the entry again.
func (s *Service) trackActive(ctx context.Context, sc Scope, st domain.State) {
	since, active := domain.SweepActive(st)
	if active && since.IsZero() {
		since = st.UpdatedAt
	}
	if err := s.States.SetActive(ctx, sc, st.DeliveryID, since, active); err != nil {
		s.log().WarnContext(ctx, "active_set_update_failed", "delivery_id", st.DeliveryID, "error", err)
	}
}

// dueEvents refreshes the stored ETA and returns the arrival and ETA events
// that are due, marking them sent in st.
func dueEvents(st *domain.State, now time.Time) []pendingEvent {
	var out []pendingEvent
	eta, _, es := domain.ComputeETA(*st, now)
	// Without a route the drive time is unknown (0), not imminent.
	if len(st.Route.ToDropoff) == 0 {
		eta = nil
	}
	st.EtaSeconds, st.EtaState, st.EtaComputedAt = eta, es, now
	if eta != nil && etaEventDue(st, *eta, now) {
		v := *eta
		prevSec, prevAt := st.LastEtaEventSec, st.LastEtaEventAt
		st.LastEtaEventSec, st.LastEtaEventAt = &v, now
		out = append(out, pendingEvent{EventEtaChanged, &eventsv1.EtaChanged{
			DeliveryId: st.DeliveryID, EtaSeconds: toInt32(v), EtaState: string(es), At: timestamppb.New(now),
		}, func(st *domain.State) { st.LastEtaEventSec, st.LastEtaEventAt = prevSec, prevAt }})
	}
	out = append(out, signalEvents(st, now)...)
	if domain.ArrivalDue(*st, now) {
		st.ArrivalSent = true
		ev := &eventsv1.ArrivalDetected{DeliveryId: st.DeliveryID, At: timestamppb.New(now)}
		if st.LastFix != nil {
			ev.DistanceM = domain.DistanceM(st.LastFix.Pos, st.Dropoff)
		}
		if eta != nil {
			ev.EtaSeconds = toInt32(*eta)
		}
		out = append(out, pendingEvent{EventArrivalDetected, ev, func(st *domain.State) { st.ArrivalSent = false }})
	}
	return out
}

// etaEventDue compares arrival times (not seconds from now, which shrink as
// time passes): the first known ETA, then a move of 2 minutes or more, at
// most once a minute.
func etaEventDue(st *domain.State, eta int, now time.Time) bool {
	if st.LastEtaEventSec == nil {
		return true
	}
	if now.Sub(st.LastEtaEventAt) < etaEventMinInterval {
		return false
	}
	arrive := now.Add(time.Duration(eta) * time.Second)
	last := st.LastEtaEventAt.Add(time.Duration(*st.LastEtaEventSec) * time.Second)
	d := arrive.Sub(last)
	return d >= etaEventMinChange || d <= -etaEventMinChange
}

// toInt32 clamps v into int32 for proto fields.
func toInt32(v int) int32 {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < math.MinInt32 {
		return math.MinInt32
	}
	return int32(v)
}

// signalEvents returns tracking.courier_signal_lost when the shown signal
// is lost and the gap has not been reported, and
// tracking.courier_signal_restored when fixes return after a reported gap
// (the signal is live or stale again). It uses st.Signal, set by the caller.
func signalEvents(st *domain.State, now time.Time) []pendingEvent {
	if st.Courier == nil {
		return nil
	}
	courierID := st.Courier.ID
	switch {
	case st.Signal == domain.SignalLost && !st.SignalLostSent:
		st.SignalLostSent = true
		ev := &eventsv1.CourierSignalLost{DeliveryId: st.DeliveryID, CourierId: courierID}
		if st.LastFix != nil {
			ev.LastFixAt = timestamppb.New(st.LastFix.T)
		}
		return []pendingEvent{{EventSignalLost, ev, func(st *domain.State) { st.SignalLostSent = false }}}
	case st.SignalLostSent && (st.Signal == domain.SignalLive || st.Signal == domain.SignalStale):
		st.SignalLostSent = false
		return []pendingEvent{{EventSignalRestored, &eventsv1.CourierSignalRestored{
			DeliveryId: st.DeliveryID, CourierId: courierID, At: timestamppb.New(now),
		}, func(st *domain.State) { st.SignalLostSent = true }}}
	}
	return nil
}
