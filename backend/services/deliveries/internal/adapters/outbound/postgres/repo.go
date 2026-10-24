// Package postgres implements the deliveries app.Repo on PostgreSQL with
// sqlc-generated queries (package db).
//
// Tenant isolation is row-level security: every scoped method runs in a
// transaction that first sets dt.tenant_id and dt.environment, and the
// queries rely on the (forced) policy instead of filtering on tenant. The
// customer first name, notes, dropoff label and metadata are sealed with
// sealbox. Events are written to the outbox in the same transaction as the
// state they describe.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/raduolivas/dt/backend/pkg/outbox"
	"github.com/raduolivas/dt/backend/pkg/sealbox"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/adapters/outbound/postgres/db"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/app"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/domain"
)

// externalIDKey is the unique constraint on (tenant, environment, external id).
const externalIDKey = "deliveries_external_id_key"

// Repo is the Postgres repository.
type Repo struct {
	pool *pgxpool.Pool
	box  *sealbox.Box
}

var _ app.Repo = (*Repo)(nil)

// New returns a Repo over pool that seals personal data with box.
func New(pool *pgxpool.Pool, box *sealbox.Box) *Repo {
	return &Repo{pool: pool, box: box}
}

// inTenant runs fn in one transaction scoped to s's tenant and environment
// by row-level security, committed if fn returns nil.
func (r *Repo) inTenant(ctx context.Context, s app.Scope, fn func(tx pgx.Tx, q *db.Queries) error) error {
	if s.TenantID == "" {
		return errors.New("postgres: scope has no tenant")
	}
	if _, err := protoEnv(s.Env); err != nil {
		return fmt.Errorf("postgres: scope: %w", err)
	}
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('dt.tenant_id', $1, true), set_config('dt.environment', $2, true)`,
			s.TenantID, s.Env); err != nil {
			return fmt.Errorf("set tenant: %w", err)
		}
		return fn(tx, db.New(tx))
	})
}

// Settings returns the tenant's delivery settings, or the defaults when
// none were received yet.
func (r *Repo) Settings(ctx context.Context, tenantID string) (domain.TenantSettings, error) {
	row, err := db.New(r.pool).GetSettings(ctx, tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DefaultTenantSettings(), nil
	}
	if err != nil {
		return domain.TenantSettings{}, fmt.Errorf("get settings: %w", err)
	}
	models := make([]domain.Model, len(row.AllowedModels))
	for i, m := range row.AllowedModels {
		models[i] = domain.Model(m)
	}
	return domain.TenantSettings{
		AllowedModels: models, DefaultModel: domain.Model(row.DefaultModel),
		PickupPolicy: domain.PickupPolicy(row.PickupConfirmation),
	}, nil
}

// ApplySettings stores the tenant's settings as of updatedAt in tx, unless
// the stored ones are newer.
func (r *Repo) ApplySettings(ctx context.Context, tx pgx.Tx, tenantID string, s domain.TenantSettings, updatedAt time.Time) error {
	models := make([]string, len(s.AllowedModels))
	for i, m := range s.AllowedModels {
		models[i] = string(m)
	}
	err := db.New(tx).ApplySettings(ctx, db.ApplySettingsParams{
		TenantID: tenantID, AllowedModels: models, DefaultModel: string(s.DefaultModel),
		PickupConfirmation: string(s.PickupPolicy), UpdatedAt: updatedAt,
	})
	if err != nil {
		return fmt.Errorf("apply settings: %w", err)
	}
	return nil
}

// Create stores a new delivery, its history and evts.
func (r *Repo) Create(ctx context.Context, s app.Scope, d *domain.Delivery, evts []domain.Event) error {
	if d.TenantID != s.TenantID || d.Env != s.Env {
		return fmt.Errorf("create delivery %s: delivery scope %s/%s differs from %s/%s", d.ID, d.TenantID, d.Env, s.TenantID, s.Env)
	}
	return r.inTenant(ctx, s, func(tx pgx.Tx, q *db.Queries) error {
		sealed, err := r.seal(d)
		if err != nil {
			return err
		}
		err = q.InsertDelivery(ctx, db.InsertDeliveryParams{
			ID: d.ID, TenantID: s.TenantID, Environment: s.Env, ExternalID: d.ExternalID,
			Model: string(d.Model), Status: string(d.Status), PickupPolicy: string(d.Policy),
			PickupLat: d.Pickup.Lat, PickupLng: d.Pickup.Lng, PickupName: d.PickupName,
			DropoffLat: d.Dropoff.Lat, DropoffLng: d.Dropoff.Lng,
			DropoffLabelSealed: sealed.dropoffLabel, CustomerFirstNameSealed: sealed.customerFirstName,
			NotesSealed: sealed.notes, MetadataSealed: sealed.metadata,
			ItemsSummary: d.ItemsSummary, ItemCount: itemCount(d.ItemCount), CourierID: courierID(d),
			ErasedAt: d.ErasedAt, DroppedReports: droppedReports(d), CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
		})
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == externalIDKey {
			return app.ErrExternalIDTaken
		}
		if err != nil {
			return fmt.Errorf("insert delivery: %w", err)
		}
		if err := insertHistory(ctx, q, s, d.ID, 0, d.NewHistory()); err != nil {
			return err
		}
		return enqueue(ctx, tx, s, evts, d.UpdatedAt)
	})
}

// Get returns the delivery with its full history.
func (r *Repo) Get(ctx context.Context, s app.Scope, id string) (*domain.Delivery, error) {
	var d *domain.Delivery
	err := r.inTenant(ctx, s, func(_ pgx.Tx, q *db.Queries) error {
		var err error
		d, err = r.load(ctx, q, id)
		return err
	})
	return d, err
}

// List returns a page of deliveries, newest first by id, each with its full
// history, and whether more follow.
func (r *Repo) List(ctx context.Context, s app.Scope, f app.ListFilter) ([]*domain.Delivery, bool, error) {
	if f.Limit <= 0 {
		return nil, false, errors.New("list deliveries: limit must be positive")
	}
	var (
		out  []*domain.Delivery
		more bool
	)
	err := r.inTenant(ctx, s, func(_ pgx.Tx, q *db.Queries) error {
		p := db.ListDeliveriesParams{
			ExternalID: f.ExternalID, CreatedGte: f.CreatedGTE, CreatedLt: f.CreatedLT,
			RowLimit: int32(min(f.Limit, 1000)) + 1, // #nosec G115 -- bounded above
		}
		if f.Status != nil {
			p.Status = new(string(*f.Status))
		}
		if f.Model != nil {
			p.Model = new(string(*f.Model))
		}
		if f.StartingAfter != "" {
			p.StartingAfter = &f.StartingAfter
		}
		rows, err := q.ListDeliveries(ctx, p)
		if err != nil {
			return fmt.Errorf("list deliveries: %w", err)
		}
		if len(rows) > f.Limit {
			rows, more = rows[:f.Limit], true
		}
		ids := make([]string, len(rows))
		for i, row := range rows {
			ids[i] = row.ID
		}
		hist, err := q.ListHistory(ctx, ids)
		if err != nil {
			return fmt.Errorf("list history: %w", err)
		}
		byDelivery := map[string][]db.ListHistoryRow{}
		for _, h := range hist {
			byDelivery[h.DeliveryID] = append(byDelivery[h.DeliveryID], h)
		}
		out = make([]*domain.Delivery, 0, len(rows))
		for _, row := range rows {
			d, err := r.toDelivery(db.GetDeliveryRow(row), byDelivery[row.ID])
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return out, more, nil
}

// Mutate locks the delivery, runs fn on it and stores the result, its new
// history entries and fn's events, keeping the pickup watch in step.
func (r *Repo) Mutate(ctx context.Context, s app.Scope, id string, fn func(*domain.Delivery) ([]domain.Event, error)) (*domain.Delivery, error) {
	var d *domain.Delivery
	err := r.inTenant(ctx, s, func(tx pgx.Tx, q *db.Queries) error {
		if _, err := q.LockDelivery(ctx, id); err != nil {
			return notFound("lock delivery", err)
		}
		var err error
		if d, err = r.load(ctx, q, id); err != nil {
			return err
		}
		loaded := len(d.History)
		evts, err := fn(d)
		if err != nil {
			return err
		}
		sealed, err := r.seal(d)
		if err != nil {
			return err
		}
		n, err := q.UpdateDelivery(ctx, db.UpdateDeliveryParams{
			ID: d.ID, Status: string(d.Status), DropoffLat: d.Dropoff.Lat, DropoffLng: d.Dropoff.Lng,
			DropoffLabelSealed: sealed.dropoffLabel, CustomerFirstNameSealed: sealed.customerFirstName,
			NotesSealed: sealed.notes, MetadataSealed: sealed.metadata,
			CourierID: courierID(d), ErasedAt: d.ErasedAt, DroppedReports: droppedReports(d), UpdatedAt: d.UpdatedAt,
		})
		if err != nil {
			return fmt.Errorf("update delivery: %w", err)
		}
		if n == 0 {
			return app.ErrNotFound
		}
		if err := insertHistory(ctx, q, s, d.ID, loaded, d.NewHistory()); err != nil {
			return err
		}
		if err := syncPickupWatch(ctx, q, s, d); err != nil {
			return err
		}
		return enqueue(ctx, tx, s, evts, d.UpdatedAt)
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}

// Courier returns the courier, or app.ErrNotFound.
func (r *Repo) Courier(ctx context.Context, s app.Scope, id string) (domain.Courier, error) {
	var c domain.Courier
	err := r.inTenant(ctx, s, func(_ pgx.Tx, q *db.Queries) error {
		row, err := q.GetCourier(ctx, id)
		if err != nil {
			return notFound("get courier", err)
		}
		c = toCourier(row)
		return nil
	})
	return c, err
}

// UpsertCourier creates c or updates the courier with its external id,
// keeping that courier's id and created_at. courier.upserted events in evts
// are rewritten to name the stored courier.
func (r *Repo) UpsertCourier(ctx context.Context, s app.Scope, c domain.Courier, evts []domain.Event) (domain.Courier, bool, error) {
	var (
		stored  domain.Courier
		created bool
	)
	err := r.inTenant(ctx, s, func(tx pgx.Tx, q *db.Queries) error {
		row, err := q.UpsertCourier(ctx, db.UpsertCourierParams{
			ID: c.ID, TenantID: s.TenantID, Environment: s.Env, ExternalID: c.ExternalID,
			DisplayName: c.DisplayName, Vehicle: string(c.Vehicle), PhotoUrl: c.PhotoURL,
			CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
		})
		if err != nil {
			return fmt.Errorf("upsert courier: %w", err)
		}
		stored, created = toCourier(row), row.ID == c.ID
		out := make([]domain.Event, len(evts))
		for i, e := range evts {
			if p, ok := e.Payload.(domain.CourierUpserted); ok {
				p.CourierID = stored.ID
				e.Payload = p
			}
			out[i] = e
		}
		return enqueue(ctx, tx, s, out, stored.UpdatedAt)
	})
	if err != nil {
		return domain.Courier{}, false, err
	}
	return stored, created, nil
}

// DuePickupWatches returns up to limit unwarned watches due at now, across
// tenants, oldest due first.
func (r *Repo) DuePickupWatches(ctx context.Context, now time.Time, limit int) ([]app.PickupWatch, error) {
	rows, err := db.New(r.pool).DuePickupWatches(ctx, db.DuePickupWatchesParams{
		Now: now, RowLimit: int32(min(max(limit, 0), 10000)), // #nosec G115 -- bounded
	})
	if err != nil {
		return nil, fmt.Errorf("due pickup watches: %w", err)
	}
	out := make([]app.PickupWatch, len(rows))
	for i, row := range rows {
		out[i] = app.PickupWatch{DeliveryID: row.DeliveryID, TenantID: row.TenantID, Env: row.Environment, Since: row.Since.UTC()}
	}
	return out, nil
}

// WarnPickupPending re-checks w's delivery in its tenant. If the pickup is
// still waiting for the other side, it marks the watch warned and enqueues
// delivery.pickup_confirmation_pending, at most once; otherwise it drops
// the watch.
func (r *Repo) WarnPickupPending(ctx context.Context, w app.PickupWatch, now time.Time) error {
	s := app.Scope{TenantID: w.TenantID, Env: w.Env}
	return r.inTenant(ctx, s, func(tx pgx.Tx, q *db.Queries) error {
		// Lock the delivery before the watch, in the same order as Mutate.
		if _, err := q.LockDelivery(ctx, w.DeliveryID); errors.Is(err, pgx.ErrNoRows) {
			return deleteWatch(ctx, q, w.DeliveryID)
		} else if err != nil {
			return fmt.Errorf("lock delivery: %w", err)
		}
		watch, err := q.LockPickupWatch(ctx, w.DeliveryID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock pickup watch: %w", err)
		}
		if watch.WarnedAt != nil {
			return nil
		}
		d, err := r.load(ctx, q, w.DeliveryID)
		if err != nil {
			return err
		}
		if _, waiting := pickupWaitingSince(d); !waiting {
			return deleteWatch(ctx, q, w.DeliveryID)
		}
		if err := q.MarkPickupWatchWarned(ctx, db.MarkPickupWatchWarnedParams{DeliveryID: w.DeliveryID, WarnedAt: &now}); err != nil {
			return fmt.Errorf("mark pickup watch warned: %w", err)
		}
		return enqueue(ctx, tx, s, []domain.Event{{
			Type: domain.EventPickupConfirmationPending,
			Payload: domain.PickupConfirmationPending{
				DeliveryID: d.ID, ConfirmedBy: confirmedBy(d), Since: watch.Since.UTC(),
			},
		}}, now)
	})
}

// load reads a delivery and its full history; it must run in inTenant.
func (r *Repo) load(ctx context.Context, q *db.Queries, id string) (*domain.Delivery, error) {
	row, err := q.GetDelivery(ctx, id)
	if err != nil {
		return nil, notFound("get delivery", err)
	}
	hist, err := q.ListHistory(ctx, []string{id})
	if err != nil {
		return nil, fmt.Errorf("list history: %w", err)
	}
	return r.toDelivery(row, hist)
}

func (r *Repo) toDelivery(row db.GetDeliveryRow, hist []db.ListHistoryRow) (*domain.Delivery, error) {
	sl := sealer{box: r.box, deliveryID: row.ID}
	d := &domain.Delivery{
		ID: row.ID, TenantID: row.TenantID, Env: row.Environment, ExternalID: row.ExternalID,
		Model: domain.Model(row.Model), Status: domain.Status(row.Status), Policy: domain.PickupPolicy(row.PickupPolicy),
		Pickup: domain.Location{Lat: row.PickupLat, Lng: row.PickupLng}, PickupName: row.PickupName,
		Dropoff:      domain.Location{Lat: row.DropoffLat, Lng: row.DropoffLng},
		ItemsSummary: row.ItemsSummary, ErasedAt: utc(row.ErasedAt), DroppedReports: int(row.DroppedReports),
		CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
	}
	var err error
	if d.DropoffLabel, err = sl.openString(fieldDropoffLabel, row.DropoffLabelSealed); err != nil {
		return nil, err
	}
	if d.CustomerFirstName, err = sl.openString(fieldCustomerFirstName, row.CustomerFirstNameSealed); err != nil {
		return nil, err
	}
	if d.Notes, err = sl.openString(fieldNotes, row.NotesSealed); err != nil {
		return nil, err
	}
	if d.Metadata, err = sl.openMap(fieldMetadata, row.MetadataSealed); err != nil {
		return nil, err
	}
	if row.ItemCount != nil {
		d.ItemCount = new(int(*row.ItemCount))
	}
	if row.CourierID != nil {
		d.Courier = &domain.CourierRef{
			ID: *row.CourierID, ExternalID: deref(row.CourierExternalID),
			DisplayName: deref(row.CourierDisplayName), Vehicle: domain.Vehicle(deref(row.CourierVehicle)),
		}
	}
	d.History = make([]domain.HistoryEntry, len(hist))
	for i, h := range hist {
		d.History[i] = domain.HistoryEntry{
			EventID: deref(h.EventID), Type: h.Type, Actor: domain.ActorKind(h.Actor),
			By: domain.ConfirmedBy(deref(h.By)), From: domain.Status(deref(h.FromStatus)),
			To: domain.Status(h.ToStatus), Outcome: domain.Outcome(h.Outcome),
			OccurredAt: h.OccurredAt.UTC(), ReceivedAt: h.ReceivedAt.UTC(), Reason: deref(h.Reason),
		}
	}
	return d, nil
}

func (r *Repo) seal(d *domain.Delivery) (sealedFields, error) {
	sl := sealer{box: r.box, deliveryID: d.ID}
	var (
		f   sealedFields
		err error
	)
	if f.dropoffLabel, err = sl.sealString(fieldDropoffLabel, d.DropoffLabel); err != nil {
		return f, err
	}
	if f.customerFirstName, err = sl.sealString(fieldCustomerFirstName, d.CustomerFirstName); err != nil {
		return f, err
	}
	if f.notes, err = sl.sealString(fieldNotes, d.Notes); err != nil {
		return f, err
	}
	f.metadata, err = sl.sealMap(fieldMetadata, d.Metadata)
	return f, err
}

// insertHistory appends entries after the first `after` ones (seq is
// 1-based and gap-free; the delivery row lock serialises writers).
func insertHistory(ctx context.Context, q *db.Queries, s app.Scope, deliveryID string, after int, entries []domain.HistoryEntry) error {
	for i, h := range entries {
		err := q.InsertHistory(ctx, db.InsertHistoryParams{
			DeliveryID: deliveryID, Seq: int32(after + i + 1), // #nosec G115 -- history length
			EventID: nilIfEmpty(h.EventID), Type: h.Type, Actor: string(h.Actor), By: nilIfEmpty(string(h.By)),
			FromStatus: nilIfEmpty(string(h.From)), ToStatus: string(h.To), Outcome: string(h.Outcome),
			OccurredAt: h.OccurredAt, ReceivedAt: h.ReceivedAt, Reason: nilIfEmpty(h.Reason),
			TenantID: s.TenantID, Environment: s.Env,
		})
		if err != nil {
			return fmt.Errorf("insert history: %w", err)
		}
	}
	return nil
}

// syncPickupWatch starts the watch when a courier_and_store pickup has one
// side's confirmation and ends it otherwise (picked up, final, or no
// confirmation).
func syncPickupWatch(ctx context.Context, q *db.Queries, s app.Scope, d *domain.Delivery) error {
	since, waiting := pickupWaitingSince(d)
	if !waiting {
		return deleteWatch(ctx, q, d.ID)
	}
	err := q.InsertPickupWatch(ctx, db.InsertPickupWatchParams{
		DeliveryID: d.ID, TenantID: s.TenantID, Environment: s.Env,
		Since: since, DueAt: since.Add(domain.PickupPendingAfter),
	})
	if err != nil {
		return fmt.Errorf("insert pickup watch: %w", err)
	}
	return nil
}

func deleteWatch(ctx context.Context, q *db.Queries, deliveryID string) error {
	if err := q.DeletePickupWatch(ctx, deliveryID); err != nil {
		return fmt.Errorf("delete pickup watch: %w", err)
	}
	return nil
}

// pickupWaitingSince reports whether d is a courier_and_store delivery not
// yet picked up with exactly one of the two sides' confirmations, and when
// that side first confirmed.
func pickupWaitingSince(d *domain.Delivery) (time.Time, bool) {
	if d.Policy != domain.PolicyCourierAndStore {
		return time.Time{}, false
	}
	switch d.Status {
	case domain.StatusConfirmed, domain.StatusCourierAssigned, domain.StatusAtPickup:
	default:
		return time.Time{}, false
	}
	var (
		since          time.Time
		courier, store bool
	)
	for _, h := range d.History {
		if h.Type != string(domain.ReportPickupConfirmed) || (h.By != domain.ByCourier && h.By != domain.ByStore) {
			continue
		}
		if since.IsZero() {
			since = h.OccurredAt
		}
		courier = courier || h.By == domain.ByCourier
		store = store || h.By == domain.ByStore
	}
	return since, courier != store
}

// confirmedBy returns who confirmed d's pickup so far, each once, in order.
func confirmedBy(d *domain.Delivery) []domain.ConfirmedBy {
	var out []domain.ConfirmedBy
	seen := map[domain.ConfirmedBy]bool{}
	for _, h := range d.History {
		if h.Type == string(domain.ReportPickupConfirmed) && !seen[h.By] {
			seen[h.By] = true
			out = append(out, h.By)
		}
	}
	return out
}

// enqueue writes evts, in order, to the outbox on deliveries.<type>.
func enqueue(ctx context.Context, tx pgx.Tx, s app.Scope, evts []domain.Event, fallback time.Time) error {
	for _, e := range evts {
		env, err := envelope(s.TenantID, s.Env, e, fallback)
		if err != nil {
			return err
		}
		if err := outbox.Enqueue(ctx, tx, outbox.SubjectFor(outbox.StreamDeliveries, e.Type), env); err != nil {
			return err
		}
	}
	return nil
}

func toCourier(row db.Courier) domain.Courier {
	return domain.Courier{
		ID: row.ID, ExternalID: row.ExternalID, DisplayName: row.DisplayName, PhotoURL: row.PhotoUrl,
		Vehicle: domain.Vehicle(row.Vehicle), CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
	}
}

func notFound(op string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrNotFound
	}
	return fmt.Errorf("%s: %w", op, err)
}

func courierID(d *domain.Delivery) *string {
	if d.Courier == nil {
		return nil
	}
	return &d.Courier.ID
}

func itemCount(n *int) *int32 {
	if n == nil {
		return nil
	}
	return new(int32(*n)) // #nosec G115 -- the domain bounds item_count to 0-999
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	return new(t.UTC())
}

func droppedReports(d *domain.Delivery) int32 {
	return int32(d.DroppedReports) // #nosec G115 -- a delivery receives far fewer than 2^31 reports
}
