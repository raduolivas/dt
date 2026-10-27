# Runs in the dt-staging-db-bootstrap task (postgres:18-alpine, sh). The
# deploy runs it before the migrations, every time: it is idempotent.
#  1. On a fresh database, runs the existing role and schema SQL
#     (infra/compose/postgres/init/01-schemas.sql, passed as BOOTSTRAP_SQL).
#  2. Always sets the svc_* and mig_* passwords from Secrets Manager (the
#     SQL file sets dev passwords equal to the role names).
# The RDS master user is not a superuser: on PostgreSQL 16+ it must be
# granted SET and INHERIT on the roles it creates to run CREATE SCHEMA ...
# AUTHORIZATION and ALTER DEFAULT PRIVILEGES FOR ROLE, hence
# createrole_self_grant. Passwords go to psql on stdin (a psql variable), never on a
# command line or in the log.
set -eu

psql_q() { psql -X -v ON_ERROR_STOP=1 -q "$@"; }

# A separate assignment so that a failed connection stops the script (set -e).
exists="$(psql_q -tAc "SELECT count(*) FROM pg_roles WHERE rolname = 'mig_gateway'")"
if [ "$exists" = 0 ]; then
  echo "db-bootstrap: creating schemas and roles"
  { echo "SET createrole_self_grant = 'set, inherit';"; printf '%s\n' "$BOOTSTRAP_SQL"; } | psql_q
else
  echo "db-bootstrap: roles exist"
fi

for svc in gateway deliveries tracking webhooks accounts; do
  for kind in svc mig; do
    var="$(echo "${kind}_${svc}_PASSWORD" | tr '[:lower:]' '[:upper:]')"
    eval "pw=\${$var}"
    # shellcheck disable=SC2154 # pw is set by the eval above
    case "$pw" in
      *[!0-9a-f]* | "") echo "db-bootstrap: $var is not a hex password" >&2; exit 1 ;;
    esac
    printf '\\set pw %s\nALTER ROLE %s PASSWORD :'"'"'pw'"'"';\n' "$pw" "${kind}_${svc}" | psql_q
  done
done
echo "db-bootstrap: passwords set"
