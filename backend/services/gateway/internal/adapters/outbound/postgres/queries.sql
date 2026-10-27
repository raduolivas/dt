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
INSERT INTO idempotency_records (tenant_id, environment, idem_key, request_hash, state, locked_until, resource_id, created_at, expires_at)
VALUES (@tenant_id, @environment, @idem_key, @request_hash, 'in_progress', @locked_until, sqlc.narg(resource_id), @created_at, @expires_at)
ON CONFLICT (tenant_id, environment, idem_key) DO NOTHING
RETURNING idem_key;

-- name: LockIdempotencyRecord :one
SELECT request_hash, state, locked_until, response_status, response_headers, response_body, resource_id, expires_at
FROM idempotency_records
WHERE tenant_id = @tenant_id AND environment = @environment AND idem_key = @idem_key
FOR UPDATE;

-- name: ReclaimIdempotencyRecord :exec
-- Restarts an expired record or a stale in-progress one as a new claim.
UPDATE idempotency_records
SET request_hash = @request_hash, state = 'in_progress', locked_until = @locked_until,
    response_status = NULL, response_headers = NULL, response_body = NULL, resource_id = sqlc.narg(resource_id),
    created_at = @created_at, expires_at = @expires_at
WHERE tenant_id = @tenant_id AND environment = @environment AND idem_key = @idem_key;

-- name: CompleteIdempotencyRecord :execrows
UPDATE idempotency_records
SET state = 'completed', locked_until = NULL,
    response_status = @response_status, response_headers = @response_headers, response_body = @response_body,
    resource_id = COALESCE(sqlc.narg(resource_id), resource_id)
WHERE tenant_id = @tenant_id AND environment = @environment AND idem_key = @idem_key
  AND state = 'in_progress' AND locked_until = @lease;

-- name: AbandonIdempotencyRecord :execrows
DELETE FROM idempotency_records
WHERE tenant_id = @tenant_id AND environment = @environment AND idem_key = @idem_key
  AND state = 'in_progress' AND locked_until = @lease;

-- name: DeleteIdempotencyRecordsForResource :execrows
-- Erasure: drops the stored responses of one resource (they may carry its
-- personal data).
DELETE FROM idempotency_records
WHERE tenant_id = @tenant_id AND environment = @environment AND resource_id = @resource_id;

-- name: DeleteExpiredIdempotencyRecords :execrows
-- One sweeper batch, oldest first; key-based so concurrent updates are safe.
DELETE FROM idempotency_records
WHERE (tenant_id, environment, idem_key) IN (
  SELECT x.tenant_id, x.environment, x.idem_key FROM idempotency_records x
  WHERE x.expires_at < @before
  ORDER BY x.expires_at
  LIMIT @batch
);

-- name: InsertAuditEntry :exec
INSERT INTO audit_log (id, tenant_id, environment, actor_type, actor_id, action, target, status, ip, user_agent, request_id, occurred_at)
VALUES (@id, @tenant_id, @environment, @actor_type, @actor_id, @action, sqlc.narg(target), @status, sqlc.narg(ip), sqlc.narg(user_agent), @request_id, @occurred_at);

-- name: DeleteAuditEntriesBefore :execrows
-- One retention batch, oldest first.
DELETE FROM audit_log
WHERE id IN (
  SELECT x.id FROM audit_log x
  WHERE x.occurred_at < @before
  ORDER BY x.occurred_at
  LIMIT @batch
);

-- name: LockDeliveryLinks :exec
-- Serializes Replace and Expire of one delivery's links for the transaction.
SELECT pg_advisory_xact_lock(hashtextextended('tracking_links:' || @delivery_id::text, 0));

-- name: GetLinkExpiry :one
SELECT expires_at FROM tracking_link_expiry WHERE delivery_id = $1;

-- name: InsertLinkExpiry :exec
-- The first final time wins.
INSERT INTO tracking_link_expiry (delivery_id, expires_at) VALUES ($1, $2)
ON CONFLICT (delivery_id) DO NOTHING;

-- name: RevokeUnrevokedLinks :exec
UPDATE tracking_links SET revoked_at = @revoked_at
WHERE delivery_id = @delivery_id AND revoked_at IS NULL;

-- name: InsertTrackingLink :exec
INSERT INTO tracking_links (id, tenant_id, environment, delivery_id, token_hash, created_at, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: GetTrackingLinkByHash :one
SELECT id, tenant_id, environment, delivery_id, created_at, revoked_at, expires_at
FROM tracking_links WHERE token_hash = $1;

-- name: GetTrackingLink :one
SELECT id, tenant_id, environment, delivery_id, created_at, revoked_at, expires_at
FROM tracking_links WHERE id = $1;

-- name: ExpireUnrevokedLinks :many
UPDATE tracking_links SET expires_at = @expires_at
WHERE delivery_id = @delivery_id AND revoked_at IS NULL AND expires_at IS NULL
RETURNING id;

-- name: LinksRevokedSince :many
SELECT id FROM tracking_links
WHERE delivery_id = @delivery_id AND revoked_at >= @since
ORDER BY revoked_at;
