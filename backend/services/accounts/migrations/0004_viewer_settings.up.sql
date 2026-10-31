-- Hosted tracking page settings: the origins allowed to frame it and the
-- tenant's branding. The CHECKs mirror domain.TenantSettings.Validate's size
-- limits; branding holds the text fields, the logo has its own columns.
ALTER TABLE tenants
  ADD COLUMN embed_origins text[] NOT NULL DEFAULT '{}',
  ADD COLUMN branding jsonb NULL,
  ADD COLUMN logo bytea NULL,
  ADD COLUMN logo_content_type text NULL,
  ADD CONSTRAINT tenants_embed_origins_max CHECK (cardinality(embed_origins) <= 20),
  ADD CONSTRAINT tenants_logo_max CHECK (logo IS NULL OR octet_length(logo) <= 102400),
  ADD CONSTRAINT tenants_logo_content_type_known
    CHECK ((logo IS NULL) = (logo_content_type IS NULL)
           AND (logo_content_type IS NULL OR logo_content_type IN ('image/png', 'image/webp')));
