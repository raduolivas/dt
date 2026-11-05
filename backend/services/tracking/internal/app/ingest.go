package app

import (
	"context"
	"errors"
	"slices"
	"sort"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

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
	ctx, span := otel.Tracer(tracerName).Start(ctx, "tracking.ingest", trace.WithAttributes(
		attribute.String("dt.tenant_id", sc.TenantID), attribute.String("dt.environment", sc.Env)))
	defer func() {
		span.SetAttributes(attribute.Int("tracking.fixes.accepted", accepted), attribute.Int("tracking.fixes.dropped", dropped))
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "ingest failed")
		}
		span.End()
	}()
	ids, err := s.States.ActiveDeliveriesForCourier(ctx, sc.TenantID, sc.Env, courierID)
	if err != nil {
		return 0, 0, err
	}
	if len(fixes) == 0 {
		return 0, 0, nil
	}
	if len(ids) == 0 {
		s.reportDropped(ctx, "", map[string]int{ReasonNoActiveDelivery: len(fixes)})
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
	// dropReasons[id][i] is why delivery id dropped fix i ("" when it did
	// not); a fix only counts as dropped when no delivery kept it.
	dropReasons := make(map[string][]string, len(ids))
	for _, id := range ids {
		var keptHere []bool
		var rowsHere []TrailRow
		var reasonsHere []string
		err := s.Apply(ctx, sc, id, func(st *domain.State) error {
			// Reset per attempt: Apply may run the mutation again.
			keptHere, rowsHere, reasonsHere = make([]bool, len(sorted)), nil, make([]string, len(sorted))
			if reason := staleReason(st, courierID); reason != "" {
				// A stale active set: the delivery was finished or reassigned.
				for i := range reasonsHere {
					reasonsHere[i] = reason
				}
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
					reasonsHere[i] = v.Reason
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
			reasons := make([]string, len(sorted))
			for i := range reasons {
				reasons[i] = ReasonStateUpdateFailed
			}
			dropReasons[id] = reasons
			continue
		}
		dropReasons[id] = reasonsHere
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
	for _, id := range ids {
		counts := map[string]int{}
		for i, r := range dropReasons[id] {
			if r != "" && !kept[i] {
				counts[r]++
			}
		}
		s.reportDropped(ctx, id, counts)
	}
	if len(rows) > 0 {
		s.Trail.Append(rows)
	}
	if accepted > 0 {
		s.reportLocation(ctx, sc, courierID, accepted)
	}
	if len(failed) > 0 && len(failed) == attempted {
		// Nothing could be applied anywhere: report the outage.
		return 0, len(fixes), errors.Join(failed...)
	}
	return accepted, len(fixes) - accepted, nil
}

// Reasons a fix is dropped by Ingest. The domain verdict reasons (invalid,
// future, speed, jump) are used as they are.
const (
	ReasonNoActiveDelivery  = "no_active_delivery"
	ReasonDeliveryFinal     = "delivery_final"
	ReasonDeliveryReassign  = "delivery_reassigned"
	ReasonStateUpdateFailed = "state_update_failed"
)

// staleReason is why a delivery in the courier's active set no longer takes
// the courier's fixes, or "" when it does.
func staleReason(st *domain.State, courierID string) string {
	switch {
	case st.Status.IsFinal():
		return ReasonDeliveryFinal
	case st.Courier == nil || st.Courier.ID != courierID:
		return ReasonDeliveryReassign
	}
	return ""
}

// reportDropped records, per reason, how many fixes a delivery dropped: a
// span event and a log line, neither with coordinates.
func (s *Service) reportDropped(ctx context.Context, deliveryID string, counts map[string]int) {
	reasons := make([]string, 0, len(counts))
	for r := range counts {
		reasons = append(reasons, r)
	}
	sort.Strings(reasons)
	span := trace.SpanFromContext(ctx)
	for _, r := range reasons {
		span.AddEvent("fix_dropped", trace.WithAttributes(
			attribute.String("dt.delivery_id", deliveryID), attribute.String("reason", r), attribute.Int("count", counts[r])))
		s.log().WarnContext(ctx, "ingest_fixes_dropped", "delivery_id", deliveryID, "reason", r, "count", counts[r])
	}
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
