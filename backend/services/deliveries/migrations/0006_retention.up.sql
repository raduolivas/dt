-- Retention (M3 design §4.2): a delivery, its history and its pickup
-- confirmations are deleted 13 months after it reached a final status.

-- retention_scopes lists every tenant and environment with deliveries, so
-- the nightly job, which reads tenant tables only through row-level
-- security, knows which scopes to visit. It holds no personal data and is
-- read across tenants (like tenant_settings). A trigger keeps it in step.
CREATE TABLE retention_scopes (
  tenant_id text NOT NULL CHECK (tenant_id <> ''),
  environment text NOT NULL CHECK (environment IN ('test', 'live')),
  PRIMARY KEY (tenant_id, environment)
);

CREATE FUNCTION note_retention_scope() RETURNS trigger
LANGUAGE plpgsql SET search_path = deliveries, extensions, pg_temp AS $$
BEGIN
  INSERT INTO retention_scopes (tenant_id, environment) VALUES (NEW.tenant_id, NEW.environment)
  ON CONFLICT DO NOTHING;
  RETURN NULL;
END $$;
CREATE TRIGGER deliveries_retention_scope AFTER INSERT ON deliveries
  FOR EACH ROW EXECUTE FUNCTION note_retention_scope();

-- Backfill. The owner is subject to the forced policy, so it is lifted for
-- this one statement; the migration runs as one transaction.
ALTER TABLE deliveries NO FORCE ROW LEVEL SECURITY;
INSERT INTO retention_scopes (tenant_id, environment) SELECT DISTINCT tenant_id, environment FROM deliveries;
ALTER TABLE deliveries FORCE ROW LEVEL SECURITY;

-- The nightly job's lease (pkg/nightly SchemaSQL, verbatim).
CREATE TABLE job_leases (name text PRIMARY KEY, holder text NOT NULL, expires_at timestamptz NOT NULL);
