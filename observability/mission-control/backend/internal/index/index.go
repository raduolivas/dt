// Package index maintains the order index (external_id -> delivery_id per
// tenant and environment) from delivery.created events (spec §4.3).
package index

import (
	"context"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
)

// Index writes order rows.
type Index struct{ St *store.Store }

// New returns an Index over st.
func New(st *store.Store) *Index { return &Index{St: st} }

// OnEvent upserts mc_order when e is a delivery.created event. normalize sets
// OrderRef only on delivery.created, so an order_ref with a delivery_id is the
// signal; other events are ignored.
func (i *Index) OnEvent(ctx context.Context, e store.Event) error {
	if e.OrderRef == "" || e.DeliveryID == "" {
		return nil
	}
	return i.St.UpsertOrder(ctx, store.OrderHit{
		TenantID: e.TenantID, Environment: e.Env, OrderRef: e.OrderRef,
		DeliveryID: e.DeliveryID, Origin: e.Origin, CreatedAt: e.OccurredAt,
	})
}
