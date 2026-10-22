-- name: InsertTenant :exec
INSERT INTO tenants (id, name, live_enabled, created_at, allowed_models, default_model, pickup_confirmation, settings_updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: GetTenant :one
SELECT id, name, live_enabled, created_at, allowed_models, default_model, pickup_confirmation, settings_updated_at
FROM tenants WHERE id = $1;

-- name: SetTenantSettings :execrows
UPDATE tenants SET allowed_models = $2, default_model = $3, pickup_confirmation = $4, settings_updated_at = $5
WHERE id = $1;

-- name: SetLiveEnabled :execrows
UPDATE tenants SET live_enabled = $2 WHERE id = $1;

-- name: InsertKey :exec
INSERT INTO api_keys (id, tenant_id, environment, name, lookup, display_prefix, hash, scopes,
  allowed_ips, created_by, created_at, expires_at, revoked_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13);

-- name: GetKeyForUpdate :one
SELECT * FROM api_keys WHERE id = $1 AND tenant_id = $2 FOR UPDATE;

-- name: UpdateKey :execrows
UPDATE api_keys SET name = $3, scopes = $4, allowed_ips = $5, expires_at = $6, revoked_at = $7
WHERE id = $1 AND tenant_id = $2;

-- name: ListKeys :many
SELECT * FROM api_keys WHERE tenant_id = $1 AND environment = $2 ORDER BY created_at, id;
