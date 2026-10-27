package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/raduolivas/dt/backend/services/tracking/internal/adapters/outbound/postgres/db"
	"github.com/raduolivas/dt/backend/services/tracking/internal/app"
	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
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

// DeleteErasedTrail implements app.RetentionStore.
func (r *Repo) DeleteErasedTrail(ctx context.Context, s app.Scope, since time.Time) (int64, error) {
	var n int64
	err := r.inTenant(ctx, s, func(q *db.Queries) error {
		var err error
		n, err = q.DeleteErasedTrail(ctx, since)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("delete erased trail: %w", err)
	}
	return n, nil
}

// UnsimplifiedBefore implements app.RetentionStore.
func (r *Repo) UnsimplifiedBefore(ctx context.Context, s app.Scope, before time.Time) ([]string, error) {
	var ids []string
	err := r.inTenant(ctx, s, func(q *db.Queries) error {
		var err error
		ids, err = q.UnsimplifiedTrailBefore(ctx, before)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("unsimplified trails: %w", err)
	}
	return ids, nil
}

// TrailPoints implements app.RetentionStore.
func (r *Repo) TrailPoints(ctx context.Context, s app.Scope, id string) ([]domain.LatLng, error) {
	var out []domain.LatLng
	err := r.inTenant(ctx, s, func(q *db.Queries) error {
		var err error
		out, err = trailPoints(ctx, q, id)
		return err
	})
	return out, err
}

// PutSimplifiedRoute implements app.RetentionStore.
func (r *Repo) PutSimplifiedRoute(ctx context.Context, s app.Scope, id string, route []domain.LatLng, at time.Time) error {
	return r.inTenant(ctx, s, func(q *db.Queries) error {
		return putRoute(ctx, q, s, id, route, at)
	})
}

// DropTrailPartitionsBefore implements app.RetentionStore.
func (r *Repo) DropTrailPartitionsBefore(ctx context.Context, day time.Time) (int, error) {
	n, err := db.New(r.pool).DropTrailPartitionsBefore(ctx, pgtype.Date{Time: day.UTC().Truncate(24 * time.Hour), Valid: true})
	if err != nil {
		return 0, fmt.Errorf("drop trail partitions: %w", err)
	}
	return int(n), nil
}

// DeleteFinalBefore implements app.RetentionStore, in one transaction.
func (r *Repo) DeleteFinalBefore(ctx context.Context, s app.Scope, before time.Time) (int64, error) {
	var n int64
	err := r.inTenant(ctx, s, func(q *db.Queries) error {
		if err := q.DeleteSimplifiedRoutesFinalBefore(ctx, before); err != nil {
			return fmt.Errorf("delete simplified routes: %w", err)
		}
		if err := q.DeleteProofPointsFinalBefore(ctx, before); err != nil {
			return fmt.Errorf("delete proof points: %w", err)
		}
		var err error
		if n, err = q.DeleteDeliveriesFinalBefore(ctx, before); err != nil {
			return fmt.Errorf("delete deliveries: %w", err)
		}
		return nil
	})
	return n, err
}

// TrailPoints implements app.Projection.
func (p *Projection) TrailPoints(ctx context.Context, id string) ([]domain.LatLng, error) {
	return trailPoints(ctx, p.q, id)
}

// ReplaceTrail implements app.Projection.
func (p *Projection) ReplaceTrail(ctx context.Context, id string, route []domain.LatLng, at time.Time) error {
	if err := putRoute(ctx, p.q, p.s, id, route, at); err != nil {
		return err
	}
	if err := p.q.DeleteDeliveryTrail(ctx, id); err != nil {
		return fmt.Errorf("delete trail: %w", err)
	}
	return nil
}

func trailPoints(ctx context.Context, q *db.Queries, id string) ([]domain.LatLng, error) {
	rows, err := q.TrailPoints(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("trail points: %w", err)
	}
	out := make([]domain.LatLng, len(rows))
	for i, row := range rows {
		out[i] = domain.LatLng{Lat: row.Lat, Lng: row.Lng}
	}
	return out, nil
}

// putRoute stores route as simplified_routes.points, [[lat, lng], ...],
// unless the delivery has a route.
func putRoute(ctx context.Context, q *db.Queries, s app.Scope, id string, route []domain.LatLng, at time.Time) error {
	pairs := make([][2]float64, len(route))
	for i, p := range route {
		pairs[i] = [2]float64{p.Lat, p.Lng}
	}
	points, err := json.Marshal(pairs)
	if err != nil {
		return fmt.Errorf("encode route: %w", err)
	}
	if err := q.InsertSimplifiedRoute(ctx, db.InsertSimplifiedRouteParams{DeliveryID: id, TenantID: s.TenantID,
		Environment: s.Env, Points: points, CreatedAt: at}); err != nil {
		return fmt.Errorf("insert simplified route: %w", err)
	}
	return nil
}
