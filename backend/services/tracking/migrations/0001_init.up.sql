-- Tracking read model and trail (M3 design §4–§5). deliveries, couriers,
-- trail_fixes, simplified_routes and proof_points are tenant-owned: row-level
-- security is enabled AND forced, and every query must run in a transaction
-- that set dt.tenant_id and dt.environment with set_config(..., true).
-- Outside one, current_setting(..., true) is NULL or '' and no row matches:
-- fail closed (decision R7). tenant_settings holds no personal data and is
-- read by tenant id. The CHECKs mirror the domain enums.

CREATE TABLE tenant_settings (
  tenant_id text PRIMARY KEY CHECK (tenant_id <> ''),
  default_prep_minutes int CHECK (default_prep_minutes BETWEEN 1 AND 240),
  updated_at timestamptz NOT NULL
);

CREATE TABLE couriers (
  id text PRIMARY KEY,
  tenant_id text NOT NULL CHECK (tenant_id <> ''),
  environment text NOT NULL CHECK (environment IN ('test', 'live')),
  external_id text NOT NULL,
  display_name text NOT NULL,
  vehicle text NOT NULL CHECK (vehicle IN ('bicycle', 'e_bike', 'scooter', 'car', 'on_foot')),
  photo_url text
);

-- courier_id has no foreign key: courier.upserted may have aged out of the
-- stream before tracking first consumed it; reads LEFT JOIN couriers.
-- event_version is the last applied envelope delivery_version.
CREATE TABLE deliveries (
  id text PRIMARY KEY,
  tenant_id text NOT NULL CHECK (tenant_id <> ''),
  environment text NOT NULL CHECK (environment IN ('test', 'live')),
  model text NOT NULL CHECK (model IN ('direct', 'end_to_end')),
  status text NOT NULL CHECK (status IN ('confirmed', 'courier_assigned', 'at_pickup', 'picked_up',
                                         'arriving', 'delivered', 'cancelled', 'failed')),
  pickup geography(Point, 4326) NOT NULL,
  pickup_name text NOT NULL,
  dropoff geography(Point, 4326) NOT NULL,
  created_at timestamptz NOT NULL,
  ready_at timestamptz,
  courier_id text,
  courier_visible_since timestamptz,
  timeline jsonb NOT NULL DEFAULT '[]',
  erased_at timestamptz,
  final_at timestamptz,
  event_version bigint NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL
);
CREATE INDEX deliveries_active_courier ON deliveries (courier_id) WHERE final_at IS NULL;
CREATE INDEX deliveries_final_at ON deliveries (final_at) WHERE final_at IS NOT NULL;

-- Raw fixes, one row per (delivery, fix), in UTC day partitions created
-- ahead of time by ensure_trail_partition.
CREATE TABLE trail_fixes (
  id bigserial,
  delivery_id text NOT NULL,
  tenant_id text NOT NULL CHECK (tenant_id <> ''),
  environment text NOT NULL CHECK (environment IN ('test', 'live')),
  courier_id text NOT NULL,
  t timestamptz NOT NULL,
  lat double precision NOT NULL CHECK (lat BETWEEN -90 AND 90),
  lng double precision NOT NULL CHECK (lng BETWEEN -180 AND 180),
  accuracy_m double precision NOT NULL CHECK (accuracy_m >= 0),
  speed_mps double precision,
  heading_deg double precision,
  source text NOT NULL CHECK (source IN ('relay', 'simulator', 'sdk', 'link')),
  PRIMARY KEY (id, t)
) PARTITION BY RANGE (t);
CREATE INDEX trail_fixes_delivery_t ON trail_fixes (delivery_id, t);

CREATE TABLE simplified_routes (
  delivery_id text PRIMARY KEY,
  tenant_id text NOT NULL CHECK (tenant_id <> ''),
  environment text NOT NULL CHECK (environment IN ('test', 'live')),
  points jsonb NOT NULL,
  created_at timestamptz NOT NULL
);

CREATE TABLE proof_points (
  delivery_id text NOT NULL,
  status text NOT NULL CHECK (status IN ('at_pickup', 'pickup_confirmed', 'arriving', 'delivered', 'failed')),
  at timestamptz NOT NULL,
  actor text NOT NULL CHECK (actor IN ('partner', 'courier', 'system')),
  lat double precision,
  lng double precision,
  position_at timestamptz,
  accuracy_m double precision,
  tenant_id text NOT NULL CHECK (tenant_id <> ''),
  environment text NOT NULL CHECK (environment IN ('test', 'live')),
  PRIMARY KEY (delivery_id, status, at)
);

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

ALTER TABLE trail_fixes ENABLE ROW LEVEL SECURITY;
ALTER TABLE trail_fixes FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON trail_fixes
  USING (tenant_id = current_setting('dt.tenant_id', true)
         AND environment = current_setting('dt.environment', true))
  WITH CHECK (tenant_id = current_setting('dt.tenant_id', true)
              AND environment = current_setting('dt.environment', true));

ALTER TABLE simplified_routes ENABLE ROW LEVEL SECURITY;
ALTER TABLE simplified_routes FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON simplified_routes
  USING (tenant_id = current_setting('dt.tenant_id', true)
         AND environment = current_setting('dt.environment', true))
  WITH CHECK (tenant_id = current_setting('dt.tenant_id', true)
              AND environment = current_setting('dt.environment', true));

ALTER TABLE proof_points ENABLE ROW LEVEL SECURITY;
ALTER TABLE proof_points FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON proof_points
  USING (tenant_id = current_setting('dt.tenant_id', true)
         AND environment = current_setting('dt.environment', true))
  WITH CHECK (tenant_id = current_setting('dt.tenant_id', true)
              AND environment = current_setting('dt.environment', true));

-- ensure_trail_partition creates the UTC day partition of trail_fixes for
-- day if it is missing. The runtime role owns no table and cannot create
-- one, so the function runs as its owner (the migration role); the new
-- partition is owned by it, gets the runtime role's DML through the
-- schema's default privileges, and carries the same forced policy, so
-- reading the partition directly is as isolated as reading trail_fixes.
CREATE FUNCTION ensure_trail_partition(day date) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path FROM CURRENT AS $$
DECLARE
  part text := 'trail_fixes_' || to_char(day, 'YYYYMMDD');
BEGIN
  -- Serialises replicas creating the same day at start-up.
  PERFORM pg_advisory_xact_lock(hashtext(current_schema() || '.trail_partitions'));
  IF to_regclass(quote_ident(part)) IS NOT NULL THEN
    RETURN;
  END IF;
  EXECUTE format('CREATE TABLE IF NOT EXISTS %I PARTITION OF trail_fixes FOR VALUES FROM (%L) TO (%L)',
                 part, day::timestamp AT TIME ZONE 'UTC', (day + 1)::timestamp AT TIME ZONE 'UTC');
  EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', part);
  EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', part);
  EXECUTE format($p$CREATE POLICY tenant_isolation ON %I
    USING (tenant_id = current_setting('dt.tenant_id', true)
           AND environment = current_setting('dt.environment', true))
    WITH CHECK (tenant_id = current_setting('dt.tenant_id', true)
                AND environment = current_setting('dt.environment', true))$p$, part);
END $$;

-- Transactional outbox and idempotent inbox (pkg/outbox SchemaSQL, verbatim).
CREATE TABLE outbox (event_id text PRIMARY KEY, subject text NOT NULL, payload bytea NOT NULL,
  headers jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now(), published_at timestamptz);
CREATE INDEX outbox_unpublished ON outbox (created_at) WHERE published_at IS NULL;
CREATE TABLE inbox (consumer text NOT NULL, event_id text NOT NULL,
  processed_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY (consumer, event_id));
