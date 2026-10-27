package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/raduolivas/dt/backend/services/deliveries/internal/adapters/outbound/postgres/db"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/app"
)

var _ app.RetentionStore = (*Repo)(nil)

// RetentionScopes implements app.RetentionStore.
func (r *Repo) RetentionScopes(ctx context.Context) ([]app.Scope, error) {
	rows, err := db.New(r.pool).RetentionScopes(ctx)
	if err != nil {
		return nil, fmt.Errorf("retention scopes: %w", err)
	}
	out := make([]app.Scope, len(rows))
	for i, row := range rows {
		out[i] = app.Scope{TenantID: row.TenantID, Env: row.Environment}
	}
	return out, nil
}

// DeleteFinalBefore implements app.RetentionStore, in one transaction.
func (r *Repo) DeleteFinalBefore(ctx context.Context, s app.Scope, before time.Time, limit int) (int64, error) {
	var n int64
	err := r.inTenant(ctx, s, func(_ pgx.Tx, q *db.Queries) error {
		ids, err := q.FinalDeliveriesBefore(ctx, db.FinalDeliveriesBeforeParams{Before: before,
			RowLimit: int32(min(max(limit, 0), 1<<20))}) //nolint:gosec // clamped
		if err != nil {
			return fmt.Errorf("list expired deliveries: %w", err)
		}
		if len(ids) == 0 {
			return nil
		}
		if err := q.DeletePickupWatches(ctx, ids); err != nil {
			return fmt.Errorf("delete pickup watches: %w", err)
		}
		if err := q.DeleteHistories(ctx, ids); err != nil {
			return fmt.Errorf("delete history: %w", err)
		}
		if n, err = q.DeleteDeliveries(ctx, ids); err != nil {
			return fmt.Errorf("delete deliveries: %w", err)
		}
		return nil
	})
	return n, err
}
