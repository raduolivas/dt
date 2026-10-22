#!/usr/bin/env bash
# M2 end-to-end smoke test. Runs against the local stack (make dev): tenant and
# key through accounts, then couriers and deliveries through the gateway.
# Usage: make smoke   (or DTCTL=backend/bin/dtctl scripts/smoke-m2.sh)
#
# Env: DTCTL (default backend/bin/dtctl), DT_GATEWAY_URL (default
# http://127.0.0.1:8080), NATS_MONITOR_URL (default http://127.0.0.1:8222), and
# dtctl's DT_ACCOUNTS_URL / DT_INTERNAL_RPC_TOKEN. Secrets stay in shell
# variables and are never echoed.
set -euo pipefail

DTCTL="${DTCTL:-backend/bin/dtctl}"
GATEWAY="${DT_GATEWAY_URL:-http://127.0.0.1:8080}"
NATS_MONITOR="${NATS_MONITOR_URL:-http://127.0.0.1:8222}"
export DT_INTERNAL_RPC_TOKEN="${DT_INTERNAL_RPC_TOKEN:-dev-only-not-a-secret-internal-token-0000}"

body="$(mktemp)"
trap 'rm -f "$body"' EXIT

step() { echo "==> $*" >&2; }
fail() { echo "SMOKE FAIL: $*" >&2; exit 1; }

key=""
run_id="$(date -u +%Y%m%dT%H%M%S)-$$"

# call METHOD PATH [JSON]: prints the HTTP status, body in $body. Every call
# carries a fresh Idempotency-Key (it runs in a subshell, so a counter would not persist); the gateway requires it on POSTs.
call() {
  local method="$1" path="$2" data="${3-}" args=()
  [[ -n "$data" ]] && args=(-H 'Content-Type: application/json' --data "$data")
  curl -sS -o "$body" -w '%{http_code}' --max-time 5 -X "$method" \
    -H "Authorization: Bearer ${key}" -H "Idempotency-Key: smoke-${run_id}-$(date +%s%N)" \
    "${args[@]}" "${GATEWAY}/v1${path}"
}

# expect STATUS METHOD PATH [JSON]: fails unless the call answers STATUS.
expect() {
  local want="$1" code
  shift
  code="$(call "$@")"
  [[ "$code" == "$want" ]] || fail "$2 $3: want ${want}, got ${code}: $(cat "$body")"
}

# field JQ-EXPR WANT: asserts a field of the last response body.
field() {
  local got
  got="$(jq -r "$1" <"$body")"
  [[ "$got" == "$2" ]] || fail "$1: want ${2}, got ${got}: $(cat "$body")"
}

new_delivery() { # EXTERNAL_ID MODEL
  jq -nc --arg e "$1" --arg m "$2" '{
    external_id: $e, delivery_model: $m,
    pickup: {location: {lat: -23.5614, lng: -46.6559}, name: "Pizzaria Central"},
    dropoff: {location: {lat: -23.5505, lng: -46.6333}, label: "Apto 42"},
    customer: {first_name: "Ana"}, notes: "Ring twice", metadata: {table: "12"}}'
}

event() { jq -nc --arg e "$1" --arg t "$2" '{event_id: $e, type: $t}'; }

step "1. create a tenant, a test key and delivery settings"
tenant="$("$DTCTL" tenant create --name "Smoke M2 ${run_id}")"
[[ "$tenant" == ten_* ]] || fail "tenant id ${tenant}"
key="$("$DTCTL" key create --tenant "$tenant" --env test --name smoke-m2 2>/dev/null)"
[[ "$key" =~ ^dt_sk_test_[0-9A-Za-z]{55}$ ]] || fail "key create did not print a test secret key"
"$DTCTL" tenant settings --tenant "$tenant" --models direct,end_to_end --default direct --pickup any 2>/dev/null
echo "tenant ${tenant}" >&2

# The gateway's key cache is eventually consistent (accounts outbox -> NATS).
for _ in $(seq 1 40); do
  code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 3 -H "Authorization: Bearer ${key}" "${GATEWAY}/v1/me")"
  [[ "$code" == 200 ]] && break
  sleep 0.25
done
[[ "$code" == 200 ]] || fail "key not known to the gateway after 10 s (last status ${code})"

step "2. PUT /couriers/c-1 -> 201 with a cou_ id"
expect 201 PUT /couriers/c-1 '{"display_name":"Carlos","vehicle":"e_bike"}'
courier_id="$(jq -r .id <"$body")"
[[ "$courier_id" == cou_* ]] || fail "courier id ${courier_id}"
field .external_id c-1

step "3. create a direct delivery -> 201 confirmed"
expect 201 POST /deliveries "$(new_delivery order-direct-${run_id} direct)"
d1="$(jq -r .id <"$body")"
[[ "$d1" == del_* ]] || fail "delivery id ${d1}"
field .status confirmed
field .delivery_model direct
field .customer.first_name Ana

step "4. pickup_confirmed by store -> picked_up, then arriving, then delivered"
expect 200 POST "/deliveries/${d1}/events" '{"event_id":"e1","type":"pickup_confirmed","by":"store"}'
field .outcome applied
field .delivery.status picked_up
expect 200 POST "/deliveries/${d1}/events" "$(event e2 arriving)"
field .delivery.status arriving
expect 200 POST "/deliveries/${d1}/events" "$(event e3 delivered)"
field .outcome applied
field .delivery.status delivered

step "5. repeating the last report with the same event_id -> duplicate"
expect 200 POST "/deliveries/${d1}/events" "$(event e3 delivered)"
field .outcome duplicate
field .delivery.status delivered

step "6. end_to_end delivery: at_pickup before assignment -> 409 invalid_transition"
expect 201 POST /deliveries "$(new_delivery order-e2e-${run_id} end_to_end)"
d2="$(jq -r .id <"$body")"
expect 409 POST "/deliveries/${d2}/events" "$(event e4 at_pickup)"
field .code invalid_transition

step "7. assign the courier -> courier_assigned; cancel -> cancelled"
expect 200 POST "/deliveries/${d2}/assignment" "$(jq -nc --arg c "$courier_id" '{courier_id: $c}')"
field .status courier_assigned
field .courier.id "$courier_id"
expect 200 POST "/deliveries/${d2}/cancel" '{"reason":"smoke"}'
field .status cancelled

step "8. erase personal data on the delivered delivery"
expect 200 DELETE "/deliveries/${d1}/personal-data"
field .customer.first_name null
field .personal_data_erased true
expect 200 GET "/deliveries/${d1}"
field .customer.first_name null

step "9. the list shows both deliveries, newest first"
expect 200 GET /deliveries
field '.data | length' 2
field '.data[0].id' "$d2"
field .has_more false

step "10. deliveries.delivery.* events reached the DELIVERIES stream"
# The outbox relay publishes asynchronously; poll the NATS monitoring endpoint.
msgs=0
for _ in $(seq 1 40); do
  msgs="$(curl -fsS --max-time 3 "${NATS_MONITOR}/jsz?streams=true" \
    | jq '[.account_details[]?.stream_detail[]? | select(.name == "DELIVERIES") | .state.messages] | add // 0')"
  # 2 created + status changes (>= 4 on d1, 1 cancel on d2) is well above 6.
  (( msgs >= 6 )) && break
  sleep 0.25
done
(( msgs >= 6 )) || fail "DELIVERIES stream holds ${msgs} messages, want at least 6"
# The monitoring endpoint reports the number of distinct subjects, not their names:
# created, status_changed, pickup_confirmed, personal_data_erased and the two
# courier subjects make six; require at least the five delivery-side ones.
nsubj="$(curl -fsS --max-time 3 "${NATS_MONITOR}/jsz?streams=true" \
  | jq '[.account_details[]?.stream_detail[]? | select(.name == "DELIVERIES") | .state.num_subjects] | add // 0')"
(( nsubj >= 5 )) || fail "DELIVERIES stream holds ${nsubj} distinct subjects, want at least 5"
echo "   ${msgs} messages on ${nsubj} subjects in DELIVERIES" >&2

echo "SMOKE M2 OK"
echo OK
