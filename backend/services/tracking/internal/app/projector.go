package app

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

// proofWindow is how far back a proof point looks for a fix.
const proofWindow = 2 * time.Minute

// proofStatus maps a status change to its proof point, if it has one.
var proofStatus = map[domain.Status]string{
	domain.StatusAtPickup:  "at_pickup",
	domain.StatusPickedUp:  "pickup_confirmed",
	domain.StatusArriving:  "arriving",
	domain.StatusDelivered: "delivered",
	domain.StatusFailed:    "failed",
}

// DeliveryCreated is the delivery.created payload.
type DeliveryCreated struct {
	ID         string
	Model      domain.Model
	Status     domain.Status
	Pickup     domain.LatLng
	PickupName string
	Dropoff    domain.LatLng
	CreatedAt  time.Time
	ReadyAt    *time.Time
}

// StatusChanged is the delivery.status_changed payload.
type StatusChanged struct {
	ID       string
	From, To domain.Status
	Actor    string
	At       time.Time
}

// DeliveryUpdated is the delivery.updated payload, reduced to what tracking
// keeps: a new dropoff location and/or a new (possibly cleared) ready_at.
type DeliveryUpdated struct {
	ID             string
	Dropoff        *domain.LatLng
	ReadyAtChanged bool
	ReadyAt        *time.Time
}

// stale reports whether an event of version v is older than the record's
// last applied one. Events of the same version belong to the same change.
func stale(rec DeliveryRecord, v uint64) bool { return v < rec.EventVersion }

// OnDeliveryCreated stores the new delivery and its live state.
func (s *Service) OnDeliveryCreated(ctx context.Context, p Projection, sc Scope, v uint64, ev DeliveryCreated) error {
	if _, ok, err := p.Delivery(ctx, ev.ID); err != nil || ok {
		return err // a redelivery under another event id: already stored
	}
	rec := DeliveryRecord{
		ID: ev.ID, TenantID: sc.TenantID, Env: sc.Env, Model: ev.Model, Status: ev.Status,
		Pickup: ev.Pickup, PickupName: ev.PickupName, Dropoff: ev.Dropoff,
		CreatedAt: ev.CreatedAt, ReadyAt: ev.ReadyAt,
		Timeline:     []domain.TimelineEntry{{Status: ev.Status, At: ev.CreatedAt}},
		EventVersion: v, UpdatedAt: ev.CreatedAt,
	}
	if err := p.PutDelivery(ctx, rec); err != nil {
		return err
	}
	return s.sync(ctx, p, sc, rec, nil)
}

// OnStatusChanged records the new status (visible-since, final_at), updates
// the courier's active set, the live state and, for a proof-point status,
// the proof point with the latest fix of the last 2 minutes.
func (s *Service) OnStatusChanged(ctx context.Context, p Projection, sc Scope, v uint64, ev StatusChanged) error {
	rec, ok, err := p.Delivery(ctx, ev.ID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("status_changed for unknown delivery %s: %w", ev.ID, ErrNotFound)
	}
	if stale(rec, v) || rec.Status == ev.To {
		return nil
	}
	if !domain.CourierVisible(rec.Model, rec.Status) && domain.CourierVisible(rec.Model, ev.To) {
		at := ev.At
		rec.CourierVisibleSince = &at
	}
	rec.Status = ev.To
	rec.Timeline = append(slices.Clone(rec.Timeline), domain.TimelineEntry{Status: ev.To, At: ev.At})
	if ev.To.IsFinal() {
		at := ev.At
		rec.FinalAt = &at
	}
	rec.EventVersion, rec.UpdatedAt = max(rec.EventVersion, v), ev.At
	if err := p.PutDelivery(ctx, rec); err != nil {
		return err
	}
	if ev.To.IsFinal() && rec.CourierID != nil {
		if err := s.States.SetCourierActive(ctx, *rec.CourierID, rec.ID, false); err != nil {
			return fmt.Errorf("active set: %w", err)
		}
	}
	// The proof position is evidence: only the current courier's fixes
	// count, never the previous courier's after a reassignment.
	var last *domain.Fix
	if err := s.sync(ctx, p, sc, rec, func(st *domain.State) {
		last = nil
		if st.LastFix != nil && rec.CourierID != nil && st.Courier != nil && st.Courier.ID == *rec.CourierID {
			c := *st.LastFix
			last = &c
		}
	}); err != nil {
		return err
	}
	ps, ok := proofStatus[ev.To]
	if !ok {
		return nil
	}
	var trail []domain.Fix
	if rec.CourierID != nil {
		trail, err = p.RecentTrail(ctx, sc, ev.ID, *rec.CourierID, ev.At.Add(-proofWindow), ev.At)
		if err != nil {
			return err
		}
	}
	if last != nil {
		trail = append(trail, *last)
	}
	return p.PutProofPoint(ctx, ProofPoint{
		DeliveryID: ev.ID, Status: ps, At: ev.At, Actor: ev.Actor, Fix: domain.ProofPosition(trail, ev.At),
	})
}

// OnDeliveryUpdated applies a new dropoff location or ready_at.
func (s *Service) OnDeliveryUpdated(ctx context.Context, p Projection, sc Scope, v uint64, ev DeliveryUpdated) error {
	rec, ok, err := p.Delivery(ctx, ev.ID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("delivery.updated for unknown delivery %s: %w", ev.ID, ErrNotFound)
	}
	if stale(rec, v) || (ev.Dropoff == nil && !ev.ReadyAtChanged) {
		return nil
	}
	if ev.Dropoff != nil {
		rec.Dropoff = *ev.Dropoff
	}
	if ev.ReadyAtChanged {
		rec.ReadyAt = ev.ReadyAt
	}
	rec.EventVersion = max(rec.EventVersion, v)
	if err := p.PutDelivery(ctx, rec); err != nil {
		return err
	}
	return s.sync(ctx, p, sc, rec, nil)
}

// OnPersonalDataErased records the erasure time. Replacing the raw trail
// with the simplified route is the retention job's (Task 11).
func (s *Service) OnPersonalDataErased(ctx context.Context, p Projection, _ Scope, v uint64, id string, at time.Time) error {
	rec, ok, err := p.Delivery(ctx, id)
	if err != nil || !ok || rec.ErasedAt != nil {
		return err
	}
	rec.ErasedAt = &at
	rec.EventVersion = max(rec.EventVersion, v)
	return p.PutDelivery(ctx, rec)
}

// OnCourierAssigned sets the delivery's courier. A different courier clears
// the last fix (it was the previous courier's position) and moves the
// delivery between the couriers' active sets.
func (s *Service) OnCourierAssigned(ctx context.Context, p Projection, sc Scope, v uint64, deliveryID, courierID string, at time.Time) error {
	rec, ok, err := p.Delivery(ctx, deliveryID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("courier.assigned for unknown delivery %s: %w", deliveryID, ErrNotFound)
	}
	if stale(rec, v) || (rec.CourierID != nil && *rec.CourierID == courierID) {
		return nil
	}
	prev := rec.CourierID
	cid := courierID
	rec.CourierID = &cid
	if domain.CourierVisible(rec.Model, rec.Status) {
		a := at
		rec.CourierVisibleSince = &a
	}
	rec.EventVersion, rec.UpdatedAt = max(rec.EventVersion, v), at
	if err := p.PutDelivery(ctx, rec); err != nil {
		return err
	}
	if prev != nil {
		if err := s.States.SetCourierActive(ctx, *prev, rec.ID, false); err != nil {
			return fmt.Errorf("active set: %w", err)
		}
	}
	if rec.FinalAt == nil {
		if err := s.States.SetCourierActive(ctx, courierID, rec.ID, true); err != nil {
			return fmt.Errorf("active set: %w", err)
		}
	}
	return s.sync(ctx, p, sc, rec, func(st *domain.State) {
		st.LastFix = nil
		st.SignalLostSent = false
	})
}

// OnCourierUpserted stores the courier and refreshes it in the live state of
// its active deliveries.
func (s *Service) OnCourierUpserted(ctx context.Context, p Projection, sc Scope, c domain.Courier) error {
	if err := p.PutCourier(ctx, c); err != nil {
		return err
	}
	ids, err := p.CourierDeliveries(ctx, c.ID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		err := s.ApplyWith(ctx, p, sc, id, func(st *domain.State) error {
			if st.Courier == nil || st.Courier.ID != c.ID {
				return ErrNoChange
			}
			cc := c
			st.Courier = &cc
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// OnTenantSettings stores the tenant's default preparation time and
// refreshes the ETA of its active deliveries in both environments.
func (s *Service) OnTenantSettings(ctx context.Context, sp SettingsProjection, tenantID string, prep *int, at time.Time) error {
	stored, err := sp.SetTenantPrep(ctx, tenantID, prep, at)
	if err != nil || !stored {
		return err
	}
	for _, env := range []string{"test", "live"} {
		sc := Scope{TenantID: tenantID, Env: env}
		p, err := sp.Bind(ctx, sc)
		if err != nil {
			return err
		}
		ids, err := sp.ActiveDeliveries(ctx, sc)
		if err != nil {
			return err
		}
		for _, id := range ids {
			err := s.ApplyWith(ctx, p, sc, id, func(st *domain.State) error {
				if eqIntPtr(st.DefaultPrepMinutes, prep) {
					return ErrNoChange
				}
				st.DefaultPrepMinutes = clonePtr(prep)
				return nil
			})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// sync copies the record's fields into the delivery's live state (keeping
// the live fields: last fix, route, ETA bookkeeping) and runs extra, if any,
// in the same mutation.
func (s *Service) sync(ctx context.Context, p Projection, sc Scope, rec DeliveryRecord, extra func(*domain.State)) error {
	var courier *domain.Courier
	if rec.CourierID != nil {
		c, ok, err := p.Courier(ctx, *rec.CourierID)
		if err != nil {
			return err
		}
		if !ok {
			c = domain.Courier{ID: *rec.CourierID}
		}
		courier = &c
	}
	return s.ApplyWith(ctx, p, sc, rec.ID, func(st *domain.State) error {
		ApplyRecord(st, rec, courier)
		if extra != nil {
			extra(st)
		}
		return nil
	})
}

// ApplyRecord copies a read-model record (and its courier) into st.
func ApplyRecord(st *domain.State, rec DeliveryRecord, courier *domain.Courier) {
	st.DeliveryID, st.TenantID, st.Env = rec.ID, rec.TenantID, rec.Env
	st.Model, st.Status = rec.Model, rec.Status
	st.Timeline = slices.Clone(rec.Timeline)
	st.Pickup, st.PickupName, st.Dropoff = rec.Pickup, rec.PickupName, rec.Dropoff
	st.CreatedAt, st.ReadyAt = rec.CreatedAt, clonePtr(rec.ReadyAt)
	st.CourierVisibleSince = clonePtr(rec.CourierVisibleSince)
	if courier != nil {
		c := *courier
		st.Courier = &c
	} else {
		st.Courier = nil
	}
}

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func eqIntPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
