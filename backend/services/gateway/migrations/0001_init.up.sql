-- Read model of partner secret keys, built from accounts apikey.* events.
-- hash is HMAC-SHA256(pepper, full key); plaintext keys are never stored.
CREATE TABLE api_keys (
  id text PRIMARY KEY,
  tenant_id text NOT NULL,
  environment text NOT NULL CHECK (environment IN ('test', 'live')),
  lookup text NOT NULL UNIQUE,
  hash bytea NOT NULL,
  scopes text[] NOT NULL,
  allowed_ips cidr[] NOT NULL DEFAULT '{}',
  expires_at timestamptz,
  revoked_at timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- Transactional outbox and idempotent inbox (pkg/outbox SchemaSQL, verbatim).
CREATE TABLE outbox (event_id text PRIMARY KEY, subject text NOT NULL, payload bytea NOT NULL,
  headers jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now(), published_at timestamptz);
CREATE INDEX outbox_unpublished ON outbox (created_at) WHERE published_at IS NULL;
CREATE TABLE inbox (consumer text NOT NULL, event_id text NOT NULL,
  processed_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY (consumer, event_id));

-- Idempotency-Key records for partner POSTs (spec §5.1), scoped per tenant,
-- environment and key. request_hash is SHA-256(method \n path \n body).
-- An in_progress row blocks duplicates until locked_until (its owner's
-- lease); a completed row holds the response replayed until expires_at
-- (created_at + 24 h). The IdempotencySweeper deletes expired rows.
CREATE TABLE idempotency_records (
  tenant_id text,
  environment text,
  idem_key text,
  request_hash bytea NOT NULL,
  state text NOT NULL CHECK (state IN ('in_progress', 'completed')),
  locked_until timestamptz,
  response_status int,
  response_headers jsonb,
  response_body bytea,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, environment, idem_key)
);
CREATE INDEX idempotency_records_expires_at ON idempotency_records (expires_at);

-- Audit log of authenticated non-GET partner requests (spec §11.1), kept
-- 365 days (AuditRetention deletes older rows). actor_* is the credential,
-- action the route name, target the route's path value, status the one the
-- client received.
CREATE TABLE audit_log (
  id text PRIMARY KEY, -- aud_
  tenant_id text NOT NULL,
  environment text NOT NULL,
  actor_type text NOT NULL,
  actor_id text NOT NULL,
  action text NOT NULL,
  target text,
  status int NOT NULL,
  ip inet,
  user_agent text,
  request_id text NOT NULL,
  occurred_at timestamptz NOT NULL
);
CREATE INDEX audit_log_tenant_env_occurred_at ON audit_log (tenant_id, environment, occurred_at DESC);
-- Retention deletes the oldest rows across tenants.
CREATE INDEX audit_log_occurred_at ON audit_log (occurred_at);
-- Append-only for the role running the migrations, which owns the table: it
-- keeps INSERT, SELECT and DELETE (retention) but loses UPDATE and TRUNCATE.
-- An owner may revoke its own privileges, and PostgreSQL enforces that, but
-- it could also grant them back, so this guards against bugs, not a
-- compromised role. True immutability comes when migrations run as a
-- separate role (M3 staging).
REVOKE UPDATE, TRUNCATE ON audit_log FROM CURRENT_USER;
