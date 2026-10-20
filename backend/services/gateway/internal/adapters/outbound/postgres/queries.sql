-- name: InsertKey :exec
-- A redelivered or replayed apikey.created is a no-op.
INSERT INTO api_keys (id, tenant_id, environment, lookup, hash, scopes, allowed_ips, expires_at, revoked_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (id) DO NOTHING;

-- name: SetKeyExpiry :execrows
UPDATE api_keys SET expires_at = $2, updated_at = now() WHERE id = $1;

-- name: RevokeKey :execrows
-- The first revocation time wins.
UPDATE api_keys SET revoked_at = COALESCE(revoked_at, $2), updated_at = now() WHERE id = $1;

-- name: GetKeyByLookup :one
SELECT id, tenant_id, environment, lookup, hash, scopes, allowed_ips, expires_at, revoked_at
FROM api_keys WHERE lookup = $1;
