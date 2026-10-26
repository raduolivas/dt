// Package postgres implements the tracking read model, the trail writer and
// the outbox on PostgreSQL, with sqlc-generated queries (package db).
//
// Tenant isolation is row-level security: every scoped method runs in a
// transaction that first sets dt.tenant_id and dt.environment, and the
// queries rely on the (forced) policy instead of filtering on tenant.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/outbox"
	"github.com/raduolivas/dt/backend/services/tracking/internal/adapters/outbound/postgres/db"
	"github.com/raduolivas/dt/backend/services/tracking/internal/app"
	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

// eventSource is the envelope source of tracking events.
const eventSource = "tracking"

// Repo is the Postgres read model.
type Repo struct {
	pool *pgxpool.Pool
}

var (
	_ app.ReadModel = (*Repo)(nil)
	_ app.Events    = (*Repo)(nil)
)

// New returns a Repo over pool.
func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// setScope makes tx see only s's tenant and environment.
func setScope(ctx context.Context, tx pgx.Tx, s app.Scope) error {
	if s.TenantID == "" {
		return errors.New("postgres: scope has no tenant")
	}
	if _, err := protoEnv(s.Env); err != nil {
		return fmt.Errorf("postgres: scope: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('dt.tenant_id', $1, true), set_config('dt.environment', $2, true)`,
		s.TenantID, s.Env); err != nil {
		return fmt.Errorf("set tenant: %w", err)
	}
	return nil
}

// inTenant runs fn in one transaction scoped to s, committed if fn returns nil.
func (r *Repo) inTenant(ctx context.Context, s app.Scope, fn func(q *db.Queries) error) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := setScope(ctx, tx, s); err != nil {
			return err
		}
		return fn(db.New(tx))
	})
}

func protoEnv(env string) (eventsv1.Environment, error) {
	switch env {
	case "test":
		return eventsv1.Environment_ENVIRONMENT_TEST, nil
	case "live":
		return eventsv1.Environment_ENVIRONMENT_LIVE, nil
	}
	return 0, fmt.Errorf("unknown environment %q", env)
}

// LoadState rebuilds the delivery's state from its record, courier, the
// tenant's default preparation time and the courier's latest stored fix.
func (r *Repo) LoadState(ctx context.Context, s app.Scope, id string) (domain.State, error) {
	var st domain.State
	err := r.inTenant(ctx, s, func(q *db.Queries) error {
		var err error
		st, err = loadState(ctx, q, id)
		return err
	})
	return st, err
}

// RecentTrail returns the delivery's fixes with from <= t <= to.
func (r *Repo) RecentTrail(ctx context.Context, s app.Scope, id string, from, to time.Time) ([]domain.Fix, error) {
	var out []domain.Fix
	err := r.inTenant(ctx, s, func(q *db.Queries) error {
		var err error
		out, err = trailBetween(ctx, q, id, from, to)
		return err
	})
	return out, err
}

// CourierActiveDeliveries returns the courier's non-final deliveries in s,
// or app.ErrCourierNotFound when the courier is not s's. It is the state
// store's source when its cached set is missing.
func (r *Repo) CourierActiveDeliveries(ctx context.Context, s app.Scope, courierID string) ([]string, error) {
	var ids []string
	err := r.inTenant(ctx, s, func(q *db.Queries) error {
		ok, err := q.CourierExists(ctx, courierID)
		if err != nil {
			return fmt.Errorf("courier exists: %w", err)
		}
		if !ok {
			return app.ErrCourierNotFound
		}
		cid := courierID
		ids, err = q.CourierActiveDeliveries(ctx, &cid)
		return err
	})
	return ids, err
}

// ProofPoints returns the delivery's proof points in time order.
func (r *Repo) ProofPoints(ctx context.Context, s app.Scope, id string) ([]app.ProofPoint, error) {
	var out []app.ProofPoint
	err := r.inTenant(ctx, s, func(q *db.Queries) error {
		rows, err := q.ListProofPoints(ctx, id)
		if err != nil {
			return fmt.Errorf("list proof points: %w", err)
		}
		for _, row := range rows {
			p := app.ProofPoint{DeliveryID: id, Status: row.Status, At: row.At, Actor: row.Actor}
			if row.Lat != nil && row.Lng != nil && row.PositionAt != nil {
				p.Fix = &domain.Fix{T: *row.PositionAt, Pos: domain.LatLng{Lat: *row.Lat, Lng: *row.Lng}}
				if row.AccuracyM != nil {
					p.Fix.AccuracyM = *row.AccuracyM
				}
			}
			out = append(out, p)
		}
		return nil
	})
	return out, err
}

// EnsurePartitions creates the trail's UTC day partitions from day(from)
// through daysAhead days later. It is idempotent.
func (r *Repo) EnsurePartitions(ctx context.Context, from time.Time, daysAhead int) error {
	q := db.New(r.pool)
	day := from.UTC().Truncate(24 * time.Hour)
	for i := 0; i <= daysAhead; i++ {
		if err := q.EnsureTrailPartition(ctx, pgtype.Date{Time: day.AddDate(0, 0, i), Valid: true}); err != nil {
			return fmt.Errorf("ensure trail partition %s: %w", day.AddDate(0, 0, i).Format(time.DateOnly), err)
		}
	}
	return nil
}

// Enqueue writes a tracking event to the outbox, in a transaction of its own.
func (r *Repo) Enqueue(ctx context.Context, typ, tenantID, env string, subj app.Subject, msg proto.Message) error {
	penv, err := protoEnv(env)
	if err != nil {
		return err
	}
	e, err := outbox.NewEnvelope(typ, tenantID, penv, eventSource,
		&eventsv1.Subject{DeliveryId: subj.DeliveryID, CourierId: subj.CourierID}, msg, time.Now())
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		return outbox.Enqueue(ctx, tx, outbox.SubjectFor(outbox.StreamTracking, typ), e)
	})
}

// Bind returns the projection for s inside a consumer's transaction tx.
func (r *Repo) Bind(ctx context.Context, tx pgx.Tx, s app.Scope) (app.Projection, error) {
	if err := setScope(ctx, tx, s); err != nil {
		return nil, err
	}
	return &Projection{tx: tx, q: db.New(tx), s: s}, nil
}

// Settings returns the tenant-settings projection inside tx.
func (r *Repo) Settings(tx pgx.Tx) app.SettingsProjection { return &Settings{tx: tx} }

// timelineEntry is the stored JSON shape of a timeline entry.
type timelineEntry struct {
	Status domain.Status `json:"status"`
	At     time.Time     `json:"at"`
}

func encodeTimeline(tl []domain.TimelineEntry) ([]byte, error) {
	out := make([]timelineEntry, len(tl))
	for i, e := range tl {
		out[i] = timelineEntry(e)
	}
	return json.Marshal(out)
}

func decodeTimeline(b []byte) ([]domain.TimelineEntry, error) {
	var in []timelineEntry
	if err := json.Unmarshal(b, &in); err != nil {
		return nil, fmt.Errorf("timeline: %w", err)
	}
	out := make([]domain.TimelineEntry, len(in))
	for i, e := range in {
		out[i] = domain.TimelineEntry(e)
	}
	return out, nil
}

func recordFrom(row db.GetDeliveryRow) (app.DeliveryRecord, error) {
	tl, err := decodeTimeline(row.Timeline)
	if err != nil {
		return app.DeliveryRecord{}, err
	}
	return app.DeliveryRecord{
		ID: row.ID, TenantID: row.TenantID, Env: row.Environment,
		Model: domain.Model(row.Model), Status: domain.Status(row.Status),
		Pickup: domain.LatLng{Lat: row.PickupLat, Lng: row.PickupLng}, PickupName: row.PickupName,
		Dropoff:   domain.LatLng{Lat: row.DropoffLat, Lng: row.DropoffLng},
		CreatedAt: row.CreatedAt, ReadyAt: row.ReadyAt, CourierID: row.CourierID,
		CourierVisibleSince: row.CourierVisibleSince, Timeline: tl,
		ErasedAt: row.ErasedAt, FinalAt: row.FinalAt, EventVersion: uint64(max(row.EventVersion, 0)),
		UpdatedAt: row.UpdatedAt,
	}, nil
}

func getCourier(ctx context.Context, q *db.Queries, id string) (domain.Courier, bool, error) {
	row, err := q.GetCourier(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Courier{}, false, nil
	}
	if err != nil {
		return domain.Courier{}, false, fmt.Errorf("get courier: %w", err)
	}
	return domain.Courier{ID: row.ID, ExternalID: row.ExternalID, DisplayName: row.DisplayName,
		Vehicle: row.Vehicle, PhotoURL: row.PhotoUrl}, true, nil
}

func loadState(ctx context.Context, q *db.Queries, id string) (domain.State, error) {
	row, err := q.GetDelivery(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.State{}, app.ErrNotFound
	}
	if err != nil {
		return domain.State{}, fmt.Errorf("get delivery: %w", err)
	}
	rec, err := recordFrom(row)
	if err != nil {
		return domain.State{}, err
	}
	var st domain.State
	var courier *domain.Courier
	if rec.CourierID != nil {
		c, ok, err := getCourier(ctx, q, *rec.CourierID)
		if err != nil {
			return domain.State{}, err
		}
		if !ok {
			c = domain.Courier{ID: *rec.CourierID}
		}
		courier = &c
		f, err := q.LatestFix(ctx, db.LatestFixParams{DeliveryID: id, CourierID: *rec.CourierID})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return domain.State{}, fmt.Errorf("latest fix: %w", err)
		default:
			st.LastFix = &domain.Fix{T: f.T, Pos: domain.LatLng{Lat: f.Lat, Lng: f.Lng}, AccuracyM: f.AccuracyM,
				SpeedMps: f.SpeedMps, HeadingDeg: f.HeadingDeg, Source: f.Source}
		}
	}
	app.ApplyRecord(&st, rec, courier)
	prep, err := q.GetTenantPrep(ctx, rec.TenantID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return domain.State{}, fmt.Errorf("tenant prep: %w", err)
	}
	if prep != nil {
		p := int(*prep)
		st.DefaultPrepMinutes = &p
	}
	return st, nil
}

func trailBetween(ctx context.Context, q *db.Queries, id string, from, to time.Time) ([]domain.Fix, error) {
	rows, err := q.TrailBetween(ctx, db.TrailBetweenParams{DeliveryID: id, FromT: from, ToT: to})
	if err != nil {
		return nil, fmt.Errorf("trail: %w", err)
	}
	out := make([]domain.Fix, len(rows))
	for i, f := range rows {
		out[i] = domain.Fix{T: f.T, Pos: domain.LatLng{Lat: f.Lat, Lng: f.Lng}, AccuracyM: f.AccuracyM,
			SpeedMps: f.SpeedMps, HeadingDeg: f.HeadingDeg, Source: f.Source}
	}
	return out, nil
}

// Projection is the read model inside one consumer transaction, for one
// scope; its reads see the transaction's writes.
type Projection struct {
	tx pgx.Tx
	q  *db.Queries
	s  app.Scope
}

var _ app.Projection = (*Projection)(nil)

// LoadState implements app.ReadModel within the transaction.
func (p *Projection) LoadState(ctx context.Context, s app.Scope, id string) (domain.State, error) {
	if s != p.s {
		return domain.State{}, fmt.Errorf("projection bound to %v, asked for %v", p.s, s)
	}
	return loadState(ctx, p.q, id)
}

// RecentTrail implements app.ReadModel within the transaction.
func (p *Projection) RecentTrail(ctx context.Context, _ app.Scope, id string, from, to time.Time) ([]domain.Fix, error) {
	return trailBetween(ctx, p.q, id, from, to)
}

// Delivery returns the delivery's record, locked FOR UPDATE.
func (p *Projection) Delivery(ctx context.Context, id string) (app.DeliveryRecord, bool, error) {
	row, err := p.q.LockDelivery(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.DeliveryRecord{}, false, nil
	}
	if err != nil {
		return app.DeliveryRecord{}, false, fmt.Errorf("lock delivery: %w", err)
	}
	rec, err := recordFrom(db.GetDeliveryRow(row))
	return rec, err == nil, err
}

// PutDelivery inserts or updates the record.
func (p *Projection) PutDelivery(ctx context.Context, rec app.DeliveryRecord) error {
	tl, err := encodeTimeline(rec.Timeline)
	if err != nil {
		return err
	}
	err = p.q.UpsertDelivery(ctx, db.UpsertDeliveryParams{
		ID: rec.ID, TenantID: p.s.TenantID, Environment: p.s.Env, Model: string(rec.Model), Status: string(rec.Status),
		PickupLat: rec.Pickup.Lat, PickupLng: rec.Pickup.Lng, PickupName: rec.PickupName,
		DropoffLat: rec.Dropoff.Lat, DropoffLng: rec.Dropoff.Lng,
		CreatedAt: rec.CreatedAt, ReadyAt: rec.ReadyAt, CourierID: rec.CourierID,
		CourierVisibleSince: rec.CourierVisibleSince, Timeline: tl, ErasedAt: rec.ErasedAt, FinalAt: rec.FinalAt,
		EventVersion: int64(min(rec.EventVersion, 1<<62)), UpdatedAt: rec.UpdatedAt,
	})
	if err != nil {
		return fmt.Errorf("upsert delivery: %w", err)
	}
	return nil
}

// Courier returns the courier, if known in the scope.
func (p *Projection) Courier(ctx context.Context, id string) (domain.Courier, bool, error) {
	return getCourier(ctx, p.q, id)
}

// PutCourier inserts or updates the courier.
func (p *Projection) PutCourier(ctx context.Context, c domain.Courier) error {
	err := p.q.UpsertCourier(ctx, db.UpsertCourierParams{
		ID: c.ID, TenantID: p.s.TenantID, Environment: p.s.Env, ExternalID: c.ExternalID,
		DisplayName: c.DisplayName, Vehicle: c.Vehicle, PhotoUrl: c.PhotoURL,
	})
	if err != nil {
		return fmt.Errorf("upsert courier: %w", err)
	}
	return nil
}

// CourierDeliveries returns the courier's non-final deliveries.
func (p *Projection) CourierDeliveries(ctx context.Context, courierID string) ([]string, error) {
	cid := courierID
	ids, err := p.q.CourierActiveDeliveries(ctx, &cid)
	if err != nil {
		return nil, fmt.Errorf("courier deliveries: %w", err)
	}
	return ids, nil
}

// PutProofPoint stores the proof point; a repeat is ignored.
func (p *Projection) PutProofPoint(ctx context.Context, pp app.ProofPoint) error {
	arg := db.InsertProofPointParams{
		DeliveryID: pp.DeliveryID, Status: pp.Status, At: pp.At, Actor: pp.Actor,
		TenantID: p.s.TenantID, Environment: p.s.Env,
	}
	if f := pp.Fix; f != nil {
		lat, lng, acc, at := f.Pos.Lat, f.Pos.Lng, f.AccuracyM, f.T
		arg.Lat, arg.Lng, arg.AccuracyM, arg.PositionAt = &lat, &lng, &acc, &at
	}
	if err := p.q.InsertProofPoint(ctx, arg); err != nil {
		return fmt.Errorf("insert proof point: %w", err)
	}
	return nil
}

// Settings stores tenant settings inside a consumer transaction.
type Settings struct {
	tx pgx.Tx
}

var _ app.SettingsProjection = (*Settings)(nil)

// SetTenantPrep stores the default preparation time unless newer is stored.
func (s *Settings) SetTenantPrep(ctx context.Context, tenantID string, prep *int, at time.Time) (bool, error) {
	var p *int32
	if prep != nil {
		v := int32(min(max(*prep, 0), 1<<20))
		p = &v
	}
	n, err := db.New(s.tx).SetTenantPrep(ctx, db.SetTenantPrepParams{TenantID: tenantID, DefaultPrepMinutes: p, UpdatedAt: at})
	if err != nil {
		return false, fmt.Errorf("set tenant prep: %w", err)
	}
	return n == 1, nil
}

// Bind scopes the transaction to sc and returns its projection.
func (s *Settings) Bind(ctx context.Context, sc app.Scope) (app.Projection, error) {
	if err := setScope(ctx, s.tx, sc); err != nil {
		return nil, err
	}
	return &Projection{tx: s.tx, q: db.New(s.tx), s: sc}, nil
}

// ActiveDeliveries returns sc's non-final deliveries; it scopes the
// transaction to sc.
func (s *Settings) ActiveDeliveries(ctx context.Context, sc app.Scope) ([]string, error) {
	if err := setScope(ctx, s.tx, sc); err != nil {
		return nil, err
	}
	ids, err := db.New(s.tx).ActiveDeliveries(ctx)
	if err != nil {
		return nil, fmt.Errorf("active deliveries: %w", err)
	}
	return ids, nil
}
