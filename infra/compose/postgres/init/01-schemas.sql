-- Dev/test only. Production roles and passwords are provisioned by infrastructure, never by this file.
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
REVOKE ALL ON DATABASE dt FROM PUBLIC;

CREATE SCHEMA extensions;
CREATE EXTENSION postgis SCHEMA extensions;

DO $$
DECLARE svc text;
BEGIN
  FOREACH svc IN ARRAY ARRAY['gateway','deliveries','tracking','webhooks','accounts'] LOOP
    EXECUTE format('CREATE ROLE %I LOGIN PASSWORD %L', 'svc_' || svc, 'svc_' || svc);
    EXECUTE format('GRANT CONNECT ON DATABASE dt TO %I', 'svc_' || svc);
    EXECUTE format('CREATE SCHEMA %I AUTHORIZATION %I', svc, 'svc_' || svc);
    EXECUTE format('GRANT USAGE ON SCHEMA extensions TO %I', 'svc_' || svc);
    EXECUTE format('ALTER ROLE %I SET search_path = %I, extensions', 'svc_' || svc, svc);
  END LOOP;
END $$;
