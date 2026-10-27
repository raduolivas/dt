-- Customer tracking links (M3 design §3.2). token_hash is SHA-256 of the
-- 128-bit link token; the token itself is never stored. A delivery has at
-- most one unrevoked link; rotation revokes it and inserts the next.
CREATE TABLE tracking_links (
  id text PRIMARY KEY, -- tl_
  tenant_id text NOT NULL,
  environment text NOT NULL CHECK (environment IN ('test', 'live')),
  delivery_id text NOT NULL,
  token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
  created_at timestamptz NOT NULL,
  revoked_at timestamptz,
  expires_at timestamptz
);
CREATE UNIQUE INDEX tracking_links_one_unrevoked ON tracking_links (delivery_id) WHERE revoked_at IS NULL;
CREATE INDEX tracking_links_delivery ON tracking_links (delivery_id, revoked_at);

-- The link expiry of each final delivery (final time + 30 min), recorded
-- from delivery.status_changed; the first one wins. A link issued or rotated
-- after the delivery is final takes it.
CREATE TABLE tracking_link_expiry (
  delivery_id text PRIMARY KEY,
  expires_at timestamptz NOT NULL
);
