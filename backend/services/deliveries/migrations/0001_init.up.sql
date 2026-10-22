-- Deliveries schema (decision C6). couriers, deliveries and delivery_history
-- are tenant-owned: row-level security is enabled AND forced (the service
-- role owns the tables), and every query must run in a transaction that set
-- dt.tenant_id and dt.environment with set_config(..., true). Outside one,
-- current_setting(..., true) is NULL or '' and no row matches: fail closed.
-- tenant_settings and pickup_watch hold no personal data and are read
-- across tenants (settings consumer, pickup sweeper).
-- The CHECKs mirror the domain enums.

CREATE TABLE tenant_settings (
  tenant_id text PRIMARY KEY CHECK (tenant_id <> ''),
  allowed_models text[] NOT NULL
    CHECK (cardinality(allowed_models) BETWEEN 1 AND 2
           AND allowed_models <@ ARRAY['direct', 'end_to_end']
           AND array_position(allowed_models, NULL) IS NULL),
  default_model text NOT NULL CHECK (allowed_models @> ARRAY[default_model]),
  pickup_confirmation text NOT NULL
    CHECK (pickup_confirmation IN ('any', 'courier', 'store', 'courier_and_store')),
  updated_at timestamptz NOT NULL
);

CREATE TABLE couriers (
  id text PRIMARY KEY,
  tenant_id text NOT NULL CHECK (tenant_id <> ''),
  environment text NOT NULL CHECK (environment IN ('test', 'live')),
  external_id text NOT NULL,
  display_name text NOT NULL,
  vehicle text NOT NULL CHECK (vehicle IN ('bicycle', 'e_bike', 'scooter', 'car', 'on_foot')),
  photo_url text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  UNIQUE (tenant_id, environment, external_id),
  UNIQUE (id, tenant_id, environment)
);

-- *_sealed columns are sealbox AES-256-GCM blobs (AAD "<id>|<field>").
CREATE TABLE deliveries (
  id text PRIMARY KEY,
  tenant_id text NOT NULL CHECK (tenant_id <> ''),
  environment text NOT NULL CHECK (environment IN ('test', 'live')),
  external_id text NOT NULL,
  model text NOT NULL CHECK (model IN ('direct', 'end_to_end')),
  status text NOT NULL CHECK (status IN ('confirmed', 'courier_assigned', 'at_pickup', 'picked_up',
                                         'arriving', 'delivered', 'cancelled', 'failed')),
  pickup_policy text NOT NULL CHECK (pickup_policy IN ('any', 'courier', 'store', 'courier_and_store')),
  pickup geography(Point, 4326) NOT NULL,
  pickup_name text NOT NULL,
  dropoff geography(Point, 4326) NOT NULL,
  dropoff_label_sealed bytea,
  customer_first_name_sealed bytea,
  notes_sealed bytea,
  metadata_sealed bytea,
  items_summary text,
  item_count int CHECK (item_count BETWEEN 0 AND 999),
  courier_id text,
  erased_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  CONSTRAINT deliveries_external_id_key UNIQUE (tenant_id, environment, external_id),
  UNIQUE (id, tenant_id, environment),
  -- A delivery's courier belongs to the same tenant and environment.
  FOREIGN KEY (courier_id, tenant_id, environment) REFERENCES couriers (id, tenant_id, environment)
);
CREATE INDEX deliveries_tenant_environment_id ON deliveries (tenant_id, environment, id DESC);

CREATE TABLE delivery_history (
  delivery_id text NOT NULL,
  seq int NOT NULL CHECK (seq > 0),
  event_id text,
  type text NOT NULL CHECK (type IN ('created', 'assigned', 'cancelled', 'at_pickup', 'pickup_confirmed',
                                     'arriving', 'delivered', 'failed')),
  actor text NOT NULL CHECK (actor IN ('partner', 'courier', 'system')),
  "by" text CHECK ("by" IN ('courier', 'store', 'partner')),
  from_status text CHECK (from_status IN ('confirmed', 'courier_assigned', 'at_pickup', 'picked_up',
                                          'arriving', 'delivered', 'cancelled', 'failed')),
  to_status text NOT NULL CHECK (to_status IN ('confirmed', 'courier_assigned', 'at_pickup', 'picked_up',
                                               'arriving', 'delivered', 'cancelled', 'failed')),
  outcome text NOT NULL CHECK (outcome IN ('applied', 'duplicate', 'stale', 'conflict', 'recorded')),
  occurred_at timestamptz NOT NULL,
  received_at timestamptz NOT NULL,
  reason text,
  tenant_id text NOT NULL CHECK (tenant_id <> ''),
  environment text NOT NULL CHECK (environment IN ('test', 'live')),
  PRIMARY KEY (delivery_id, seq),
  FOREIGN KEY (delivery_id, tenant_id, environment) REFERENCES deliveries (id, tenant_id, environment)
);
CREATE UNIQUE INDEX delivery_history_event_id ON delivery_history (delivery_id, event_id)
  WHERE event_id IS NOT NULL;

CREATE TABLE pickup_watch (
  delivery_id text PRIMARY KEY,
  tenant_id text NOT NULL,
  environment text NOT NULL CHECK (environment IN ('test', 'live')),
  since timestamptz NOT NULL,
  due_at timestamptz NOT NULL,
  warned_at timestamptz,
  FOREIGN KEY (delivery_id, tenant_id, environment) REFERENCES deliveries (id, tenant_id, environment)
);
CREATE INDEX pickup_watch_due ON pickup_watch (due_at) WHERE warned_at IS NULL;

ALTER TABLE couriers ENABLE ROW LEVEL SECURITY;
ALTER TABLE couriers FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON couriers
  USING (tenant_id = current_setting('dt.tenant_id', true)
         AND environment = current_setting('dt.environment', true))
  WITH CHECK (tenant_id = current_setting('dt.tenant_id', true)
              AND environment = current_setting('dt.environment', true));

ALTER TABLE deliveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE deliveries FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON deliveries
  USING (tenant_id = current_setting('dt.tenant_id', true)
         AND environment = current_setting('dt.environment', true))
  WITH CHECK (tenant_id = current_setting('dt.tenant_id', true)
              AND environment = current_setting('dt.environment', true));

ALTER TABLE delivery_history ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_history FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON delivery_history
  USING (tenant_id = current_setting('dt.tenant_id', true)
         AND environment = current_setting('dt.environment', true))
  WITH CHECK (tenant_id = current_setting('dt.tenant_id', true)
              AND environment = current_setting('dt.environment', true));

-- Transactional outbox and idempotent inbox (pkg/outbox SchemaSQL, verbatim).
CREATE TABLE outbox (event_id text PRIMARY KEY, subject text NOT NULL, payload bytea NOT NULL,
  headers jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now(), published_at timestamptz);
CREATE INDEX outbox_unpublished ON outbox (created_at) WHERE published_at IS NULL;
CREATE TABLE inbox (consumer text NOT NULL, event_id text NOT NULL,
  processed_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY (consumer, event_id));
