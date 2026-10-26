ALTER TABLE tenants
  DROP CONSTRAINT tenants_default_prep_minutes_range,
  DROP COLUMN default_prep_minutes;
