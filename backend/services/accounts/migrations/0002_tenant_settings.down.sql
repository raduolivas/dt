ALTER TABLE tenants
  DROP CONSTRAINT tenants_pickup_confirmation_known,
  DROP CONSTRAINT tenants_default_model_allowed,
  DROP CONSTRAINT tenants_allowed_models_known,
  DROP COLUMN settings_updated_at,
  DROP COLUMN pickup_confirmation,
  DROP COLUMN default_model,
  DROP COLUMN allowed_models;
