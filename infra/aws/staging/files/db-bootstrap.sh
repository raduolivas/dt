# Runs in the dt-staging-db-bootstrap task (postgres:18-alpine, sh). The
# deploy runs it before the migrations, every time: it is idempotent.
#  1. On a fresh database, runs the existing role and schema SQL
#     (infra/compose/postgres/init/01-schemas.sql, passed as BOOTSTRAP_SQL).
#  2. Sets the svc_* and mig_* passwords from Secrets Manager (the SQL file
#     sets dev passwords equal to the role names).
# Both happen in ONE transaction (every statement of the SQL file is
# transactional: CREATE SCHEMA/EXTENSION/ROLE, GRANT, ALTER DEFAULT
# PRIVILEGES, ALTER ROLE, DO). A failure part-way rolls everything back, so a
# retry starts from a clean database, and a role never exists with its dev
# password.
# The RDS master user is not a superuser: on PostgreSQL 16+ it must be
# granted SET and INHERIT on the roles it creates to run CREATE SCHEMA ...
# AUTHORIZATION and ALTER DEFAULT PRIVILEGES FOR ROLE, hence
# createrole_self_grant. Passwords go to psql on stdin (a psql variable),
# never on a command line or in the log.
set -eu

psql_q() { psql -X -v ON_ERROR_STOP=1 -q "$@"; }

# A separate assignment so that a failed connection stops the script (set -e).
exists="$(psql_q -tAc "SELECT count(*) FROM pg_roles WHERE rolname = 'mig_gateway'")"

roles="svc_gateway mig_gateway svc_deliveries mig_deliveries svc_tracking mig_tracking
  svc_webhooks mig_webhooks svc_accounts mig_accounts"

# password ROLE: the role's password from <ROLE>_PASSWORD.
password() {
  eval "printf '%s' \"\${$(echo "$1_PASSWORD" | tr '[:lower:]' '[:upper:]'):-}\""
}

# Checked before anything is sent, so the transaction below never stops
# half-written (a pipeline's status is psql's).
for role in $roles; do
  case "$(password "$role")" in
    *[!0-9a-f]* | "") echo "db-bootstrap: the password of $role is not hex" >&2; exit 1 ;;
  esac
done

{
  echo "BEGIN;"
  if [ "$exists" = 0 ]; then
    echo "db-bootstrap: creating schemas and roles" >&2
    echo "SET LOCAL createrole_self_grant = 'set, inherit';"
    printf '%s\n' "$BOOTSTRAP_SQL"
  else
    echo "db-bootstrap: roles exist" >&2
  fi
  for role in $roles; do
    printf '\\set pw %s\nALTER ROLE %s PASSWORD :'"'"'pw'"'"';\n' "$(password "$role")" "$role"
  done
  echo "COMMIT;"
} | psql_q
echo "db-bootstrap: done (passwords set)"
