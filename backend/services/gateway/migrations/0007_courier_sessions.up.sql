-- Courier sessions (M5 spec §6.1). link_code_hash and refresh_hash are
-- HMAC-SHA256(gateway pepper, "courier-link:" + code / "courier-refresh:" +
-- token); the secrets themselves are never stored. The link code hash is
-- kept after the exchange, so a second exchange is told the code was used.
-- Expiry is lazy: a session past expires_at stays 'active' until something
-- ends it, and reads as ended (expired).
CREATE TABLE courier_sessions (
  id text PRIMARY KEY, -- cs_
  tenant_id text NOT NULL,
  environment text NOT NULL CHECK (environment IN ('test', 'live')),
  courier_id text NOT NULL, -- cou_
  mode text NOT NULL CHECK (mode IN ('sdk', 'link')),
  status text NOT NULL CHECK (status IN ('pending_link', 'active', 'ended')),
  created_at timestamptz NOT NULL,
  started_at timestamptz,
  expires_at timestamptz,
  ended_at timestamptz,
  end_reason text CHECK (end_reason IN ('courier', 'partner', 'expired', 'replaced', 'refresh_reuse')),
  expires_in_seconds int NOT NULL CHECK (expires_in_seconds BETWEEN 900 AND 43200),
  link_code_hash bytea UNIQUE CHECK (octet_length(link_code_hash) = 32),
  link_expires_at timestamptz,
  refresh_hash bytea UNIQUE CHECK (octet_length(refresh_hash) = 32),
  refresh_generation int NOT NULL DEFAULT 0,
  last_seen_at timestamptz,
  CHECK ((status = 'ended') = (ended_at IS NOT NULL AND end_reason IS NOT NULL)),
  CHECK (status <> 'active' OR (started_at IS NOT NULL AND expires_at IS NOT NULL AND refresh_hash IS NOT NULL)),
  CHECK (mode <> 'link' OR (link_code_hash IS NOT NULL AND link_expires_at IS NOT NULL))
);
-- One usable session and one pending link per courier.
CREATE UNIQUE INDEX courier_sessions_one_active ON courier_sessions (tenant_id, environment, courier_id)
  WHERE status = 'active';
CREATE UNIQUE INDEX courier_sessions_one_pending_link ON courier_sessions (tenant_id, environment, courier_id)
  WHERE status = 'pending_link';

-- Every superseded refresh token hash of a session, so presenting any
-- earlier token is detected as reuse (and ends the session).
CREATE TABLE courier_refresh_used (
  hash bytea PRIMARY KEY CHECK (octet_length(hash) = 32),
  session_id text NOT NULL REFERENCES courier_sessions (id) ON DELETE CASCADE
);
CREATE INDEX courier_refresh_used_session ON courier_refresh_used (session_id);
