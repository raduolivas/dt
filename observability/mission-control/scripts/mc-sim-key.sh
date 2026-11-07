#!/usr/bin/env bash
# Makes sure observability/mission-control/.env holds a working sandbox key (MC_SANDBOX_KEY) for simulation mode.
# Reuses a key already in .env when the gateway still accepts it; otherwise creates a tenant (direct deliveries,
# pickup policy "any", as scripts/courier-link.sh does) and a test key with dtctl. The key is only ever written to
# .env (git-ignored, mode 600) and never printed.
# Env: DTCTL (default backend/bin/dtctl), DT_GATEWAY_URL (default http://127.0.0.1:8080), and dtctl's DT_ACCOUNTS_URL,
# DT_INTERNAL_RPC_TOKEN and mTLS variables (the root Makefile exports them).
set -euo pipefail

DTCTL="${DTCTL:-backend/bin/dtctl}"
GATEWAY="${DT_GATEWAY_URL:-http://127.0.0.1:8080}"
export DT_INTERNAL_RPC_TOKEN="${DT_INTERNAL_RPC_TOKEN:-dev-only-not-a-secret-internal-token-0000}"
ENVFILE="$(dirname "$0")/../.env"
fail() { echo "mc-sim: $*" >&2; exit 1; }

accepted() { # key -> 0 when the gateway knows it
  [[ "$(curl -sS -o /dev/null -w '%{http_code}' --max-time 3 -H "Authorization: Bearer $1" "${GATEWAY}/v1/me" || true)" == 200 ]]
}

key=""
if [[ -f "$ENVFILE" ]]; then
  key="$(sed -n 's/^MC_SANDBOX_KEY=//p' "$ENVFILE" | tail -n 1)"
fi
if [[ "$key" == dt_sk_test_* ]] && accepted "$key"; then
  echo "==> sandbox key in .env still works" >&2
  exit 0
fi

echo "==> creating a sandbox tenant and a test key" >&2
run_id="$(date -u +%Y%m%dT%H%M%S)-$$"
err="$(mktemp)"
trap 'rm -f "$err"' EXIT
tenant="$("$DTCTL" tenant create --name "Mission Control Sim ${run_id}" 2>"$err")" || fail "dtctl tenant create: $(cat "$err")"
key="$("$DTCTL" key create --tenant "$tenant" --env test --name mission-control-sim 2>"$err")" || fail "dtctl key create: $(cat "$err")"
"$DTCTL" tenant settings --tenant "$tenant" --models direct,end_to_end --default direct --pickup any >/dev/null 2>"$err" ||
  fail "dtctl tenant settings: $(cat "$err")"
[[ "$key" == dt_sk_test_* ]] || fail "dtctl did not print a test secret key"

# the gateway's key cache is eventually consistent (accounts outbox -> NATS)
for _ in $(seq 1 40); do
  accepted "$key" && break
  sleep 0.25
done
accepted "$key" || fail "the gateway did not accept the new key within 10 s"

touch "$ENVFILE"
chmod 600 "$ENVFILE"
tmp="$(mktemp)"
grep -v '^MC_SANDBOX_KEY=' "$ENVFILE" >"$tmp" || true
printf 'MC_SANDBOX_KEY=%s\n' "$key" >>"$tmp"
cat "$tmp" >"$ENVFILE"
rm -f "$tmp"
echo "==> wrote MC_SANDBOX_KEY to ${ENVFILE}" >&2
