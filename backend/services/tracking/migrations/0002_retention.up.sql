-- Retention (M3 design §4.2): raw fixes 30 days, then one simplified route
-- per delivery; simplified routes, proof points and delivery records 13
-- months after the delivery is final.

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
LANGUAGE plpgsql SET search_path = tracking, extensions, pg_temp AS $$
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

-- drop_trail_partitions_before drops the trail's day partitions for days
-- before cutoff and returns how many it dropped. Like
-- ensure_trail_partition it runs as the owner of the partitions; the caller
-- has simplified every trail they hold. It crosses tenants, so it refuses a
-- cutoff later than the 30-day raw retention allows (the UTC day of now -
-- 30 days, the job's own cutoff): it can never drop a fix younger than that.
CREATE FUNCTION drop_trail_partitions_before(cutoff date) RETURNS int
LANGUAGE plpgsql SECURITY DEFINER SET search_path = tracking, extensions, pg_temp AS $$
DECLARE
  part text;
  n int := 0;
BEGIN
  IF cutoff IS NULL OR cutoff > ((now() AT TIME ZONE 'UTC') - interval '30 days')::date THEN
    RAISE EXCEPTION 'drop_trail_partitions_before: cutoff % is within the 30-day raw retention', cutoff;
  END IF;
  PERFORM pg_advisory_xact_lock(hashtext(current_schema() || '.trail_partitions'));
  FOR part IN
    SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
    WHERE i.inhparent = 'trail_fixes'::regclass AND c.relname ~ '^trail_fixes_[0-9]{8}$'
      AND to_date(substr(c.relname, 13), 'YYYYMMDD') < cutoff
    ORDER BY c.relname
  LOOP
    EXECUTE format('DROP TABLE %I', part);
    n := n + 1;
  END LOOP;
  RETURN n;
END $$;
REVOKE EXECUTE ON FUNCTION drop_trail_partitions_before(date) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION drop_trail_partitions_before(date) TO svc_tracking;
