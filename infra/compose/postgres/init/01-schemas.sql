-- Dev/test only. Production roles and passwords are provisioned by infrastructure, never by this file.
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
REVOKE ALL ON DATABASE dt FROM PUBLIC;

CREATE SCHEMA extensions;
CREATE EXTENSION postgis SCHEMA extensions;

-- Each service has two roles: mig_<svc> owns the schema and runs the
-- migrations, svc_<svc> is the runtime role. svc_<svc> gets DML on every
-- table mig_<svc> creates but owns none, so it cannot ALTER them (for
-- example to turn row-level security off) or create new ones.
DO $$
DECLARE svc text; mig text; rt text;
BEGIN
  FOREACH svc IN ARRAY ARRAY['gateway','deliveries','tracking','webhooks','accounts'] LOOP
    mig := 'mig_' || svc;
    rt := 'svc_' || svc;
    EXECUTE format('CREATE ROLE %I LOGIN PASSWORD %L', mig, mig);
    EXECUTE format('CREATE ROLE %I LOGIN PASSWORD %L', rt, rt);
    EXECUTE format('GRANT CONNECT ON DATABASE dt TO %I, %I', mig, rt);
    EXECUTE format('CREATE SCHEMA %I AUTHORIZATION %I', svc, mig);
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO %I', svc, rt);
    EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA %I GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %I', mig, svc, rt);
    EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA %I GRANT USAGE, SELECT ON SEQUENCES TO %I', mig, svc, rt);
    EXECUTE format('GRANT USAGE ON SCHEMA extensions TO %I, %I', mig, rt);
    EXECUTE format('ALTER ROLE %I SET search_path = %I, extensions', mig, svc);
    EXECUTE format('ALTER ROLE %I SET search_path = %I, extensions', rt, svc);
  END LOOP;
END $$;
