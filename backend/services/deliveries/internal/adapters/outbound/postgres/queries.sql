-- Every query on couriers, deliveries and delivery_history relies on row-level
-- security for the tenant and environment: it runs inside a transaction that
-- set dt.tenant_id and dt.environment. They deliberately do not filter on
-- tenant_id themselves, so a missing policy fails the RLS tests.

-- name: GetSettings :one
SELECT allowed_models, default_model, pickup_confirmation FROM tenant_settings WHERE tenant_id = $1;

-- name: ApplySettings :exec
-- Settings older than the stored ones are ignored.
INSERT INTO tenant_settings (tenant_id, allowed_models, default_model, pickup_confirmation, updated_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (tenant_id) DO UPDATE
SET allowed_models = EXCLUDED.allowed_models, default_model = EXCLUDED.default_model,
    pickup_confirmation = EXCLUDED.pickup_confirmation, updated_at = EXCLUDED.updated_at
WHERE tenant_settings.updated_at <= EXCLUDED.updated_at;

-- name: InsertDelivery :exec
INSERT INTO deliveries (id, tenant_id, environment, external_id, model, status, pickup_policy,
  pickup, pickup_name, dropoff, dropoff_label_sealed, customer_first_name_sealed, notes_sealed,
  metadata_sealed, items_summary, item_count, ready_at, courier_id, erased_at, dropped_reports, version, created_at, updated_at)
VALUES (@id, @tenant_id, @environment, @external_id, @model, @status, @pickup_policy,
  ST_SetSRID(ST_MakePoint(@pickup_lng::float8, @pickup_lat::float8), 4326)::geography, @pickup_name,
  ST_SetSRID(ST_MakePoint(@dropoff_lng::float8, @dropoff_lat::float8), 4326)::geography,
  @dropoff_label_sealed, @customer_first_name_sealed, @notes_sealed, @metadata_sealed,
  @items_summary, @item_count, @ready_at, @courier_id, @erased_at, @dropped_reports, @version, @created_at, @updated_at);

-- name: LockDelivery :one
SELECT id FROM deliveries WHERE id = $1 FOR UPDATE;

-- name: GetDelivery :one
SELECT d.id, d.tenant_id, d.environment, d.external_id, d.model, d.status, d.pickup_policy,
  ST_Y(d.pickup::geometry)::float8 AS pickup_lat, ST_X(d.pickup::geometry)::float8 AS pickup_lng, d.pickup_name,
  ST_Y(d.dropoff::geometry)::float8 AS dropoff_lat, ST_X(d.dropoff::geometry)::float8 AS dropoff_lng,
  d.dropoff_label_sealed, d.customer_first_name_sealed, d.notes_sealed, d.metadata_sealed,
  d.items_summary, d.item_count, d.ready_at, d.courier_id, c.external_id AS courier_external_id,
  c.display_name AS courier_display_name, c.vehicle AS courier_vehicle,
  d.erased_at, d.dropped_reports, d.version, d.created_at, d.updated_at
FROM deliveries d LEFT JOIN couriers c ON c.id = d.courier_id
WHERE d.id = $1;

-- name: ListDeliveries :many
-- Newest first by id; the caller asks for one row more than the page to
-- learn whether more follow.
SELECT d.id, d.tenant_id, d.environment, d.external_id, d.model, d.status, d.pickup_policy,
  ST_Y(d.pickup::geometry)::float8 AS pickup_lat, ST_X(d.pickup::geometry)::float8 AS pickup_lng, d.pickup_name,
  ST_Y(d.dropoff::geometry)::float8 AS dropoff_lat, ST_X(d.dropoff::geometry)::float8 AS dropoff_lng,
  d.dropoff_label_sealed, d.customer_first_name_sealed, d.notes_sealed, d.metadata_sealed,
  d.items_summary, d.item_count, d.ready_at, d.courier_id, c.external_id AS courier_external_id,
  c.display_name AS courier_display_name, c.vehicle AS courier_vehicle,
  d.erased_at, d.dropped_reports, d.version, d.created_at, d.updated_at
FROM deliveries d LEFT JOIN couriers c ON c.id = d.courier_id
WHERE (sqlc.narg(status)::text IS NULL OR d.status = sqlc.narg(status)::text)
  AND (sqlc.narg(model)::text IS NULL OR d.model = sqlc.narg(model)::text)
  AND (sqlc.narg(created_gte)::timestamptz IS NULL OR d.created_at >= sqlc.narg(created_gte)::timestamptz)
  AND (sqlc.narg(created_lt)::timestamptz IS NULL OR d.created_at < sqlc.narg(created_lt)::timestamptz)
  AND (sqlc.narg(external_id)::text IS NULL OR d.external_id = sqlc.narg(external_id)::text)
  AND (sqlc.narg(starting_after)::text IS NULL OR d.id < sqlc.narg(starting_after)::text)
ORDER BY d.id DESC
LIMIT sqlc.arg(row_limit);

-- name: UpdateDelivery :execrows
UPDATE deliveries
SET status = @status,
    dropoff = ST_SetSRID(ST_MakePoint(@dropoff_lng::float8, @dropoff_lat::float8), 4326)::geography,
    dropoff_label_sealed = @dropoff_label_sealed, customer_first_name_sealed = @customer_first_name_sealed,
    notes_sealed = @notes_sealed, metadata_sealed = @metadata_sealed,
    ready_at = @ready_at, courier_id = @courier_id, erased_at = @erased_at, dropped_reports = @dropped_reports,
    version = @version, updated_at = @updated_at
WHERE id = @id;

-- name: BumpDeliveryVersion :one
-- For changes outside UpdateDelivery; the caller holds the row lock.
UPDATE deliveries SET version = version + 1 WHERE id = $1 RETURNING version;

-- name: ListHistory :many
SELECT delivery_id, seq, event_id, type, actor, "by", from_status, to_status, outcome,
  occurred_at, received_at, reason_sealed
FROM delivery_history WHERE delivery_id = ANY(@delivery_ids::text[])
ORDER BY delivery_id, seq;

-- name: InsertHistory :exec
INSERT INTO delivery_history (delivery_id, seq, event_id, type, actor, "by", from_status, to_status,
  outcome, occurred_at, received_at, reason_sealed, tenant_id, environment)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14);

-- name: ClearHistoryReasons :exec
UPDATE delivery_history SET reason_sealed = NULL WHERE delivery_id = $1;

-- name: GetCourier :one
SELECT * FROM couriers WHERE id = $1;

-- name: UpsertCourier :one
-- Keeps the existing id and created_at of a courier with the same external id.
INSERT INTO couriers (id, tenant_id, environment, external_id, display_name, vehicle, photo_url,
  created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (tenant_id, environment, external_id) DO UPDATE
SET display_name = EXCLUDED.display_name, vehicle = EXCLUDED.vehicle, photo_url = EXCLUDED.photo_url,
    updated_at = EXCLUDED.updated_at
RETURNING *;

-- name: InsertPickupWatch :exec
-- The first confirmation starts the watch; later ones leave it unchanged.
INSERT INTO pickup_watch (delivery_id, tenant_id, environment, since, due_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (delivery_id) DO NOTHING;

-- name: DeletePickupWatch :exec
DELETE FROM pickup_watch WHERE delivery_id = $1;

-- name: DuePickupWatches :many
SELECT delivery_id, tenant_id, environment, since FROM pickup_watch
WHERE due_at <= @now AND warned_at IS NULL
ORDER BY due_at, delivery_id
LIMIT @row_limit;

-- name: LockPickupWatch :one
SELECT * FROM pickup_watch WHERE delivery_id = $1 FOR UPDATE;

-- name: MarkPickupWatchWarned :exec
UPDATE pickup_watch SET warned_at = $2 WHERE delivery_id = $1;

-- name: RetentionScopes :many
-- Read across tenants: retention_scopes holds no personal data.
SELECT tenant_id, environment FROM retention_scopes ORDER BY tenant_id, environment;

-- name: FinalDeliveriesBefore :many
-- Deliveries whose transition to their final status occurred before @before.
SELECT d.id FROM deliveries d
WHERE d.status IN ('delivered', 'cancelled', 'failed')
  AND EXISTS (SELECT 1 FROM delivery_history h
              WHERE h.delivery_id = d.id AND h.outcome = 'applied' AND h.to_status = d.status
                AND h.occurred_at < @before)
ORDER BY d.id
LIMIT @row_limit;

-- name: DeletePickupWatches :exec
DELETE FROM pickup_watch WHERE delivery_id = ANY(@delivery_ids::text[]);

-- name: DeleteHistories :exec
DELETE FROM delivery_history WHERE delivery_id = ANY(@delivery_ids::text[]);

-- name: DeleteDeliveries :execrows
DELETE FROM deliveries WHERE id = ANY(@delivery_ids::text[]);
