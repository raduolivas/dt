-- Every query on couriers, deliveries, trail_fixes, simplified_routes and
-- proof_points relies on row-level security for the tenant and environment:
-- it runs inside a transaction that set dt.tenant_id and dt.environment.
-- They deliberately do not filter on tenant_id themselves, so a missing
-- policy fails the RLS tests.

-- name: GetTenantPrep :one
SELECT default_prep_minutes FROM tenant_settings WHERE tenant_id = $1;

-- name: SetTenantPrep :execrows
-- Settings older than the stored ones are ignored.
INSERT INTO tenant_settings (tenant_id, default_prep_minutes, updated_at)
VALUES ($1, $2, $3)
ON CONFLICT (tenant_id) DO UPDATE
SET default_prep_minutes = EXCLUDED.default_prep_minutes, updated_at = EXCLUDED.updated_at
WHERE tenant_settings.updated_at <= EXCLUDED.updated_at;

-- name: UpsertCourier :exec
INSERT INTO couriers (id, tenant_id, environment, external_id, display_name, vehicle, photo_url)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (id) DO UPDATE
SET external_id = EXCLUDED.external_id, display_name = EXCLUDED.display_name,
    vehicle = EXCLUDED.vehicle, photo_url = EXCLUDED.photo_url;

-- name: GetCourier :one
SELECT id, external_id, display_name, vehicle, photo_url FROM couriers WHERE id = $1;

-- name: CourierKnown :one
-- A courier is the scope's when it has a couriers row, or when a non-final
-- delivery of the scope is assigned to it (its courier.upserted may have aged
-- out of the stream before tracking first read it).
SELECT (EXISTS (SELECT 1 FROM couriers c WHERE c.id = sqlc.arg(courier_id)::text)
    OR EXISTS (SELECT 1 FROM deliveries d WHERE d.courier_id = sqlc.arg(courier_id)::text AND d.final_at IS NULL))::bool AS known;

-- name: GetDelivery :one
SELECT id, tenant_id, environment, model, status,
  ST_Y(pickup::geometry)::float8 AS pickup_lat, ST_X(pickup::geometry)::float8 AS pickup_lng, pickup_name,
  ST_Y(dropoff::geometry)::float8 AS dropoff_lat, ST_X(dropoff::geometry)::float8 AS dropoff_lng,
  created_at, ready_at, courier_id, courier_visible_since, timeline, erased_at, final_at, event_version, updated_at
FROM deliveries WHERE id = $1;

-- name: LockDelivery :one
SELECT id, tenant_id, environment, model, status,
  ST_Y(pickup::geometry)::float8 AS pickup_lat, ST_X(pickup::geometry)::float8 AS pickup_lng, pickup_name,
  ST_Y(dropoff::geometry)::float8 AS dropoff_lat, ST_X(dropoff::geometry)::float8 AS dropoff_lng,
  created_at, ready_at, courier_id, courier_visible_since, timeline, erased_at, final_at, event_version, updated_at
FROM deliveries WHERE id = $1 FOR UPDATE;

-- name: UpsertDelivery :exec
INSERT INTO deliveries (id, tenant_id, environment, model, status, pickup, pickup_name, dropoff,
  created_at, ready_at, courier_id, courier_visible_since, timeline, erased_at, final_at, event_version, updated_at)
VALUES (@id, @tenant_id, @environment, @model, @status,
  ST_SetSRID(ST_MakePoint(@pickup_lng::float8, @pickup_lat::float8), 4326)::geography, @pickup_name,
  ST_SetSRID(ST_MakePoint(@dropoff_lng::float8, @dropoff_lat::float8), 4326)::geography,
  @created_at, @ready_at, @courier_id, @courier_visible_since, @timeline, @erased_at, @final_at,
  @event_version, @updated_at)
ON CONFLICT (id) DO UPDATE
SET status = EXCLUDED.status, dropoff = EXCLUDED.dropoff, ready_at = EXCLUDED.ready_at,
    courier_id = EXCLUDED.courier_id, courier_visible_since = EXCLUDED.courier_visible_since,
    timeline = EXCLUDED.timeline, erased_at = EXCLUDED.erased_at, final_at = EXCLUDED.final_at,
    event_version = EXCLUDED.event_version, updated_at = EXCLUDED.updated_at;

-- name: CourierActiveDeliveries :many
SELECT id FROM deliveries WHERE courier_id = $1 AND final_at IS NULL ORDER BY id;

-- name: ActiveDeliveries :many
SELECT id FROM deliveries WHERE final_at IS NULL ORDER BY id;

-- name: LatestFix :one
SELECT t, lat, lng, accuracy_m, speed_mps, heading_deg, source
FROM trail_fixes WHERE delivery_id = @delivery_id AND courier_id = @courier_id ORDER BY t DESC, id DESC LIMIT 1;

-- name: TrailBetween :many
SELECT t, lat, lng, accuracy_m, speed_mps, heading_deg, source
FROM trail_fixes WHERE delivery_id = @delivery_id AND courier_id = @courier_id
  AND t >= @from_t AND t <= @to_t ORDER BY t, id;

-- name: InsertProofPoint :exec
INSERT INTO proof_points (delivery_id, status, at, actor, lat, lng, position_at, accuracy_m, tenant_id, environment)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT DO NOTHING;

-- name: ListProofPoints :many
SELECT status, at, actor, lat, lng, position_at, accuracy_m
FROM proof_points WHERE delivery_id = $1 ORDER BY at, status;

-- name: DeliveryErasure :one
-- The delivery's erased_at; no row when the delivery is not the scope's.
SELECT erased_at FROM deliveries WHERE id = $1;

-- name: SimplifiedRoute :one
-- points is [[lat, lng], ...].
SELECT points FROM simplified_routes WHERE delivery_id = $1;

-- name: TrailPage :many
-- Fixes strictly after (after_t, after_id) in (t, id) order. t >= after_t is
-- redundant with the row comparison and lets Postgres prune day partitions.
SELECT id, t, lat, lng, accuracy_m, speed_mps, heading_deg, source
FROM trail_fixes
WHERE delivery_id = @delivery_id AND t >= @after_t AND (t, id) > (@after_t::timestamptz, @after_id::bigint)
ORDER BY t, id LIMIT @page_size;

-- name: EnsureTrailPartition :exec
SELECT ensure_trail_partition(@day::date);

-- name: RetentionScopes :many
-- Read across tenants: retention_scopes holds no personal data.
SELECT tenant_id, environment FROM retention_scopes ORDER BY tenant_id, environment;

-- name: UnsimplifiedTrailBefore :many
-- Deliveries with a fix before @before and no simplified route yet.
SELECT DISTINCT f.delivery_id FROM trail_fixes f
WHERE f.t < @before
  AND NOT EXISTS (SELECT 1 FROM simplified_routes s WHERE s.delivery_id = f.delivery_id)
ORDER BY f.delivery_id;

-- name: TrailPoints :many
-- Every raw position of the delivery, in trail order.
SELECT lat, lng FROM trail_fixes WHERE delivery_id = $1 ORDER BY t, id;

-- name: InsertSimplifiedRoute :exec
-- The first route stored wins.
INSERT INTO simplified_routes (delivery_id, tenant_id, environment, points, created_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (delivery_id) DO NOTHING;

-- name: DeleteDeliveryTrail :exec
DELETE FROM trail_fixes WHERE delivery_id = $1;

-- name: DropTrailPartitionsBefore :one
SELECT drop_trail_partitions_before(@day::date)::int AS dropped;

-- name: DeleteSimplifiedRoutesFinalBefore :exec
DELETE FROM simplified_routes s USING deliveries d WHERE d.id = s.delivery_id AND d.final_at < @before::timestamptz;

-- name: DeleteProofPointsFinalBefore :exec
DELETE FROM proof_points p USING deliveries d WHERE d.id = p.delivery_id AND d.final_at < @before::timestamptz;

-- name: DeleteDeliveriesFinalBefore :execrows
DELETE FROM deliveries WHERE final_at < @before::timestamptz;
