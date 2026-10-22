-- Tenant delivery settings. The CHECKs mirror domain.TenantSettings.Validate;
-- existing tenants take the defaults.
ALTER TABLE tenants
  ADD COLUMN allowed_models text[] NOT NULL DEFAULT '{direct,end_to_end}',
  ADD COLUMN default_model text NOT NULL DEFAULT 'direct',
  ADD COLUMN pickup_confirmation text NOT NULL DEFAULT 'any',
  ADD COLUMN settings_updated_at timestamptz NOT NULL DEFAULT now(),
  ADD CONSTRAINT tenants_allowed_models_known
    CHECK (cardinality(allowed_models) BETWEEN 1 AND 2
           AND allowed_models <@ ARRAY['direct', 'end_to_end']
           AND array_position(allowed_models, NULL) IS NULL
           AND (cardinality(allowed_models) = 1 OR allowed_models[1] <> allowed_models[2])),
  ADD CONSTRAINT tenants_default_model_allowed CHECK (allowed_models @> ARRAY[default_model]),
  ADD CONSTRAINT tenants_pickup_confirmation_known
    CHECK (pickup_confirmation IN ('any', 'courier', 'store', 'courier_and_store'));
