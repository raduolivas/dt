ALTER TABLE tenants
  DROP CONSTRAINT tenants_logo_content_type_known,
  DROP CONSTRAINT tenants_logo_max,
  DROP CONSTRAINT tenants_embed_origins_max,
  DROP COLUMN logo_content_type,
  DROP COLUMN logo,
  DROP COLUMN branding,
  DROP COLUMN embed_origins;
