-- 0001 revoked UPDATE and TRUNCATE on audit_log from CURRENT_USER, the role
-- running the migrations. Now that a separate role owns the schema and runs
-- them (mig_gateway locally), the runtime role gets UPDATE through default
-- privileges instead. Revoke both from every role holding them, whatever its
-- name (roles are provisioned by infrastructure), so audit_log stays
-- append-only for the service: INSERT, SELECT and DELETE (retention) remain.
DO $$
DECLARE r record;
BEGIN
  FOR r IN
    SELECT DISTINCT a.grantee
    FROM pg_class c, aclexplode(c.relacl) a
    WHERE c.oid = 'audit_log'::regclass AND a.privilege_type IN ('UPDATE', 'TRUNCATE')
  LOOP
    IF r.grantee = 0 THEN
      REVOKE UPDATE, TRUNCATE ON audit_log FROM PUBLIC;
    ELSE
      EXECUTE format('REVOKE UPDATE, TRUNCATE ON audit_log FROM %s', r.grantee::regrole);
    END IF;
  END LOOP;
END $$;
