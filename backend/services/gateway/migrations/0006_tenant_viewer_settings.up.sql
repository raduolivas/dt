-- What the hosted tracking page needs of a tenant (M4 design), built from the
-- accounts tenant.settings_set events. A tenant without a row has no embed
-- origins and the DT default branding. updated_at is the event's own time:
-- an event not newer than it is ignored.
CREATE TABLE tenant_viewer_settings (
  tenant_id text PRIMARY KEY,
  embed_origins text[] NOT NULL DEFAULT '{}',
  display_name text,
  primary_color text,
  theme text,
  logo bytea,
  logo_content_type text,
  logo_sha256 text,
  updated_at timestamptz NOT NULL
);
