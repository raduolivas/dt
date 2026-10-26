package app

import (
	"context"
	"fmt"

	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

// ViewerSnapshot returns the customer's snapshot of a delivery (the
// visibility filter applied), for the gateway's session exchange. It reads
// the live state; a state that is not stored (Redis evicted or flushed) is
// rebuilt through Apply, so it is stored and published with a fresh viewer
// version like any rebuild. A delivery outside sc is ErrNotFound.
func (s *Service) ViewerSnapshot(ctx context.Context, sc Scope, deliveryID string) (domain.TrackingSnapshot, error) {
	st, ok, err := s.States.Get(ctx, deliveryID)
	if err != nil {
		return domain.TrackingSnapshot{}, fmt.Errorf("get state %s: %w", deliveryID, err)
	}
	if !ok {
		rebuild := func(*domain.State) error { return ErrNoChange }
		if err := s.Apply(ctx, sc, deliveryID, rebuild); err != nil {
			return domain.TrackingSnapshot{}, err
		}
		if st, ok, err = s.States.Get(ctx, deliveryID); err != nil {
			return domain.TrackingSnapshot{}, fmt.Errorf("get state %s: %w", deliveryID, err)
		}
		if !ok {
			return domain.TrackingSnapshot{}, fmt.Errorf("state %s missing after rebuild", deliveryID)
		}
	}
	if st.TenantID != sc.TenantID || st.Env != sc.Env {
		return domain.TrackingSnapshot{}, ErrNotFound
	}
	return domain.Snapshot(st, s.Clock.Now()), nil
}
