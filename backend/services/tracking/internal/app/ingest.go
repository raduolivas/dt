package app

import (
	"context"
	"errors"
	"slices"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

// Ingest attaches a courier's fixes to each of the courier's active
// deliveries (purpose limitation, design §5): each fix is judged against
// each delivery's last accepted fix, moves its live position when accepted
// and goes to its trail unless dropped. A fix counts as accepted when at
// least one delivery kept it. Without an active delivery every fix is
// dropped. A delivery whose state cannot be updated is skipped (logged);
// Ingest fails only when every delivery failed. It returns
// ErrCourierNotFound when the courier is not the scope's.
func (s *Service) Ingest(ctx context.Context, sc Scope, courierID string, fixes []domain.Fix, source string) (accepted, dropped int, err error) {
	ids, err := s.States.ActiveDeliveriesForCourier(ctx, sc.TenantID, sc.Env, courierID)
	if err != nil {
		return 0, 0, err
	}
	if len(ids) == 0 || len(fixes) == 0 {
		return 0, len(fixes), nil
	}
	sorted := slices.Clone(fixes)
	for i := range sorted {
		sorted[i].Source = source
	}
	slices.SortStableFunc(sorted, func(a, b domain.Fix) int { return a.T.Compare(b.T) })

	kept := make([]bool, len(sorted))
	var rows []TrailRow
	var failed []error
	attempted := 0
	for _, id := range ids {
		var keptHere []bool
		var rowsHere []TrailRow
		err := s.Apply(ctx, sc, id, func(st *domain.State) error {
			// Reset per attempt: Apply may run the mutation again.
			keptHere, rowsHere = make([]bool, len(sorted)), nil
			if st.Status.IsFinal() || st.Courier == nil || st.Courier.ID != courierID {
				// A stale active set: the delivery was finished or reassigned.
				return ErrNoChange
			}
			now := s.Clock.Now()
			moved := false
			for i, f := range sorted {
				v, _ := domain.CheckFix(st.LastFix, f, now)
				switch v.Action {
				case domain.Accept:
					if st.ApplyFix(f, now) {
						moved = true
					}
				case domain.TrailOnly:
				default:
					continue
				}
				keptHere[i] = true
				rowsHere = append(rowsHere, TrailRow{
					DeliveryID: id, TenantID: sc.TenantID, Env: sc.Env, CourierID: courierID, Fix: f,
				})
			}
			if !moved {
				return ErrNoChange
			}
			return nil
		})
		if errors.Is(err, ErrNotFound) {
			continue
		}
		attempted++
		if err != nil {
			// One delivery's failure must not cost the others their fixes:
			// skip it (its fixes count as dropped unless another delivery
			// kept them) and go on.
			s.log().WarnContext(ctx, "ingest_delivery_failed", "delivery_id", id, "courier_id", courierID, "error", err)
			failed = append(failed, err)
			continue
		}
		for i, k := range keptHere {
			kept[i] = kept[i] || k
		}
		rows = append(rows, rowsHere...)
	}
	for _, k := range kept {
		if k {
			accepted++
		}
	}
	if len(rows) > 0 {
		s.Trail.Append(rows)
	}
	if accepted > 0 {
		s.reportLocation(ctx, sc, courierID, accepted)
	}
	if len(failed) > 0 && len(failed) == attempted {
		// Nothing could be applied anywhere: report the outage.
		return 0, 0, errors.Join(failed...)
	}
	return accepted, len(fixes) - accepted, nil
}

// reportLocation enqueues courier.location_reported at most once a minute
// per courier. It never fails the ingest.
func (s *Service) reportLocation(ctx context.Context, sc Scope, courierID string, n int) {
	ok, err := s.Sampler.Allow(ctx, courierID)
	if err != nil || !ok {
		if err != nil {
			s.log().WarnContext(ctx, "location_sample_failed", "courier_id", courierID, "error", err)
		}
		return
	}
	ectx, cancel := context.WithTimeout(ctx, enqueueTimeout)
	defer cancel()
	err = s.Events.Enqueue(ectx, EventLocationReported, sc.TenantID, sc.Env, Subject{CourierID: courierID},
		&eventsv1.CourierLocationReported{CourierId: courierID, Fixes: toInt32(n)})
	if err != nil {
		s.log().WarnContext(ctx, "location_reported_enqueue_failed", "courier_id", courierID, "error", err)
	}
}
