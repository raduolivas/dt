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

-- name: InsertIdempotencyRecord :one
-- Claims a new key; no row comes back when the key already has a record.
INSERT INTO idempotency_records (tenant_id, environment, idem_key, request_hash, state, locked_until, created_at, expires_at)
VALUES (@tenant_id, @environment, @idem_key, @request_hash, 'in_progress', @locked_until, @created_at, @expires_at)
ON CONFLICT (tenant_id, environment, idem_key) DO NOTHING
RETURNING idem_key;

-- name: LockIdempotencyRecord :one
SELECT request_hash, state, locked_until, response_status, response_headers, response_body, expires_at
FROM idempotency_records
WHERE tenant_id = @tenant_id AND environment = @environment AND idem_key = @idem_key
FOR UPDATE;

-- name: ReclaimIdempotencyRecord :exec
-- Restarts an expired record or a stale in-progress one as a new claim.
UPDATE idempotency_records
SET request_hash = @request_hash, state = 'in_progress', locked_until = @locked_until,
    response_status = NULL, response_headers = NULL, response_body = NULL,
    created_at = @created_at, expires_at = @expires_at
WHERE tenant_id = @tenant_id AND environment = @environment AND idem_key = @idem_key;

-- name: CompleteIdempotencyRecord :execrows
UPDATE idempotency_records
SET state = 'completed', locked_until = NULL,
    response_status = @response_status, response_headers = @response_headers, response_body = @response_body
WHERE tenant_id = @tenant_id AND environment = @environment AND idem_key = @idem_key
  AND state = 'in_progress' AND locked_until = @lease;

-- name: AbandonIdempotencyRecord :execrows
DELETE FROM idempotency_records
WHERE tenant_id = @tenant_id AND environment = @environment AND idem_key = @idem_key
  AND state = 'in_progress' AND locked_until = @lease;

-- name: DeleteExpiredIdempotencyRecords :execrows
-- One sweeper batch, oldest first; key-based so concurrent updates are safe.
DELETE FROM idempotency_records
WHERE (tenant_id, environment, idem_key) IN (
  SELECT x.tenant_id, x.environment, x.idem_key FROM idempotency_records x
  WHERE x.expires_at < @before
  ORDER BY x.expires_at
  LIMIT @batch
);
