-- Usual preparation time, used by tracking for the ETA before pickup.
-- The CHECK mirrors domain.TenantSettings.Validate; NULL = none.
ALTER TABLE tenants
  ADD COLUMN default_prep_minutes int NULL,
  ADD CONSTRAINT tenants_default_prep_minutes_range
    CHECK (default_prep_minutes BETWEEN 1 AND 240);
