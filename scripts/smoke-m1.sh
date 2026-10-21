#!/usr/bin/env bash
# M1 end-to-end smoke test. Runs against the local stack (make dev):
# tenant + key through accounts, then authentication at the gateway edge.
# Usage: make smoke   (or DTCTL=backend/bin/dtctl scripts/smoke-m1.sh)
#
# Env: DTCTL (default backend/bin/dtctl), DT_GATEWAY_URL (default
# http://127.0.0.1:8080), and dtctl's DT_ACCOUNTS_URL / DT_INTERNAL_RPC_TOKEN.
# Secrets stay in shell variables and are never echoed.
set -euo pipefail

DTCTL="${DTCTL:-backend/bin/dtctl}"
GATEWAY="${DT_GATEWAY_URL:-http://127.0.0.1:8080}"
export DT_INTERNAL_RPC_TOKEN="${DT_INTERNAL_RPC_TOKEN:-dev-only-not-a-secret-internal-token-0000}"

# The gateway's read model is eventually consistent (accounts outbox -> NATS
# -> gateway). Spec §11.1: a revoked key is rejected within 5 s.
POLL_TIMEOUT_MS=5000
# 250 ms keeps a full 5 s poll of 401s within the 20/min failed-auth limit.
POLL_INTERVAL=0.25

body="$(mktemp)"
trap 'rm -f "$body"' EXIT

step() { echo "==> $*" >&2; }
fail() { echo "SMOKE FAIL: $*" >&2; exit 1; }
now_ms() { echo $(( $(date +%s%N) / 1000000 )); }

# status KEY [curl args...]: prints the HTTP status of GET /v1/me; body in $body.
status() {
  local key="$1"; shift
  curl -sS -o "$body" -w '%{http_code}' --max-time 3 \
    -H "Authorization: Bearer ${key}" "$@" "${GATEWAY}/v1/me"
}

# wait_for WANT KEY: polls GET /v1/me until it answers WANT; any status other
# than WANT or the expected transitional one fails. Prints the elapsed ms.
wait_for() {
  local want="$1" key="$2" start code elapsed
  start="$(now_ms)"
  while :; do
    code="$(status "$key")"
    elapsed=$(( $(now_ms) - start ))
    if [[ "$code" == "$want" ]]; then
      echo "$elapsed"
      return 0
    fi
    case "$want:$code" in
      200:401 | 401:200) ;; # not consistent yet
      *) fail "GET /v1/me: want ${want}, got ${code}: $(cat "$body")" ;;
    esac
    (( elapsed > POLL_TIMEOUT_MS )) && fail "GET /v1/me still ${code} after ${elapsed} ms (want ${want} within ${POLL_TIMEOUT_MS} ms)"
    sleep "$POLL_INTERVAL"
  done
}

me() { curl -fsS --max-time 3 -H "Authorization: Bearer $1" "${GATEWAY}/v1/me"; }

step "1. create a tenant and a test key"
tenant="$("$DTCTL" tenant create --name "Smoke $(date -u +%Y%m%dT%H%M%SZ)")"
[[ "$tenant" == ten_* ]] || fail "tenant id ${tenant}"
key1="$("$DTCTL" key create --tenant "$tenant" --env test --name smoke-1 2>/dev/null)"
[[ "$key1" =~ ^dt_sk_test_[0-9A-Za-z]{55}$ ]] || fail "key create did not print a test secret key"
echo "tenant ${tenant}" >&2

step "2. GET /v1/me answers 200 once the read model has the key"
ms="$(wait_for 200 "$key1")"
echo "   consistent after ${ms} ms" >&2

step "3. environment is test"
me1="$(me "$key1")"
[[ "$(jq -r .environment <<<"$me1")" == test ]] || fail "environment: ${me1}"
[[ "$(jq -r .tenant_id <<<"$me1")" == "$tenant" ]] || fail "tenant_id: ${me1}"
[[ "$(jq -r .credential.type <<<"$me1")" == secret_key ]] || fail "credential.type: ${me1}"
key1_id="$(jq -r .credential.id <<<"$me1")"
[[ "$key1_id" == ak_* ]] || fail "credential.id: ${me1}"

step "4. a secret key sent with an Origin header is 403 secret_key_in_browser"
code="$(status "$key1" -H 'Origin: https://example.com')"
[[ "$code" == 403 ]] || fail "Origin: want 403, got ${code}"
[[ "$(jq -r .code <"$body")" == secret_key_in_browser ]] || fail "Origin: $(cat "$body")"

step "5. revoke; the gateway answers 401 within 5 s"
"$DTCTL" key revoke --tenant "$tenant" --key "$key1_id" 2>/dev/null
revoke_ms="$(wait_for 401 "$key1")"
[[ "$(jq -r .code <"$body")" == unauthenticated ]] || fail "revoked: $(cat "$body")"
echo "   revoked key rejected after ${revoke_ms} ms" >&2

step "6. roll a second key; the old and the new key both answer 200"
key2="$("$DTCTL" key create --tenant "$tenant" --env test --name smoke-2 2>/dev/null)"
wait_for 200 "$key2" >/dev/null
key2_id="$(me "$key2" | jq -r .credential.id)"
key3="$("$DTCTL" key roll --tenant "$tenant" --key "$key2_id" 2>/dev/null)"
[[ "$key3" =~ ^dt_sk_test_[0-9A-Za-z]{55}$ && "$key3" != "$key2" ]] || fail "roll did not print a new secret key"
wait_for 200 "$key3" >/dev/null
[[ "$(me "$key2" | jq -r .credential.id)" == "$key2_id" ]] || fail "old key after roll"
key3_id="$(me "$key3" | jq -r .credential.id)"
[[ "$key3_id" == ak_* && "$key3_id" != "$key2_id" ]] || fail "new key id ${key3_id}"

echo "revoke latency: ${revoke_ms} ms" >&2
echo "SMOKE OK"
