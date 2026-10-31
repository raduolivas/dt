#!/usr/bin/env bash
# Prints a courier link (for the phone) and a tracking link (for the laptop) for a fresh test delivery: a tenant with
# direct deliveries and pickup policy "any", a test courier (dev-courier), a direct delivery, the assignment and a
# link-mode courier session. The courier link works once and for 15 minutes; run this again for a new one.
# Each run creates a new tenant (and key) unless DT_API_KEY is set.
# Usage: make courier-link   (after make dev; add make lan-https first to use the link on a phone)
#
# Env: DTCTL (default backend/bin/dtctl), DT_GATEWAY_URL (default http://127.0.0.1:8080), DT_API_KEY (an existing test
# secret key; skips the tenant setup), DT_PICKUP (lat,lng of the pickup, default 52.3731,4.8922 in Amsterdam, where the
# map fixture is; the dropoff is about 2 km south-west of it). The links go to stdout, everything else to stderr.
set -euo pipefail

DTCTL="${DTCTL:-backend/bin/dtctl}"
GATEWAY="${DT_GATEWAY_URL:-http://127.0.0.1:8080}"
export DT_INTERNAL_RPC_TOKEN="${DT_INTERNAL_RPC_TOKEN:-dev-only-not-a-secret-internal-token-0000}"
pickup="${DT_PICKUP:-52.3731,4.8922}"
[[ "$pickup" =~ ^-?[0-9]+(\.[0-9]+)?,-?[0-9]+(\.[0-9]+)?$ ]] || { echo "DT_PICKUP must be lat,lng" >&2; exit 1; }
dropoff="$(LC_ALL=C awk -F, '{printf "%.5f,%.5f", $1 - 0.0131, $2 - 0.0070}' <<<"$pickup")"

body="$(mktemp)"
trap 'rm -f "$body"' EXIT
fail() { echo "courier-link: $*" >&2; exit 1; }
dtctl() {
  local err out
  err="$(mktemp)"
  out="$("$DTCTL" "$@" 2>"$err")" || { local msg; msg="$(cat "$err")"; rm -f "$err"; fail "dtctl $*: ${msg}"; }
  rm -f "$err"
  printf '%s' "$out"
}
run_id="$(date -u +%Y%m%dT%H%M%S)-$$"
key="${DT_API_KEY:-}"

# call METHOD PATH [JSON]: prints the HTTP status, body in $body.
call() {
  local method="$1" path="$2" data="${3-}" args=()
  [[ -n "$data" ]] && args=(-H 'Content-Type: application/json' --data "$data")
  curl -sS -o "$body" -w '%{http_code}' --max-time 10 -X "$method" \
    -H "Authorization: Bearer ${key}" -H "Idempotency-Key: cl-${run_id}-$(date +%s%N)" \
    "${args[@]}" "${GATEWAY}/v1${path}"
}
expect() {
  local want="$1" code
  shift
  code="$(call "$@")"
  [[ "$code" == "$want" ]] || fail "$1 $2: want ${want}, got ${code}: $(cat "$body")"
}

if [[ -z "$key" ]]; then
  echo "==> creating a tenant and a test key" >&2
  tenant="$(dtctl tenant create --name "Courier Dev ${run_id}")"
  key="$(dtctl key create --tenant "$tenant" --env test --name courier-link)"
  dtctl tenant settings --tenant "$tenant" --models direct,end_to_end --default direct --pickup any >/dev/null
fi
[[ "$key" == dt_sk_test_* ]] || fail "the key is not a test secret key"

# The gateway's key cache is eventually consistent (accounts outbox -> NATS).
code=""
for _ in $(seq 1 40); do
  code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 3 -H "Authorization: Bearer ${key}" "${GATEWAY}/v1/me")" || true
  [[ "$code" == 200 ]] && break
  sleep 0.25
done
[[ "$code" == 200 ]] || fail "the gateway did not accept the key within 10 s (last status ${code})"

echo "==> courier, delivery, assignment, link session" >&2
code="$(call PUT /couriers/dev-courier '{"display_name":"Dev Courier","vehicle":"e_bike"}')"
[[ "$code" == 200 || "$code" == 201 ]] || fail "PUT /couriers/dev-courier: ${code}: $(cat "$body")"
courier="$(jq -r .id <"$body")"
[[ "$courier" == cou_* ]] || fail "courier id ${courier}"

# a delivery created right after the tenant's settings can race them to the deliveries service; retry briefly
for _ in $(seq 1 20); do
  code="$(call POST /deliveries "$(jq -nc --arg e "courier-link-${run_id}" --arg p "$pickup" --arg d "$dropoff" '{
    external_id: $e, delivery_model: "direct",
    pickup: {location: {lat: ($p | split(",")[0] | tonumber), lng: ($p | split(",")[1] | tonumber)}, name: "Dev Kitchen"},
    dropoff: {location: {lat: ($d | split(",")[0] | tonumber), lng: ($d | split(",")[1] | tonumber)}, label: "Dev Door"}, notes: "Ring twice",
    customer: {first_name: "Ana"}}')")"
  [[ "$code" == 201 ]] && break
  sleep 0.5
done
[[ "$code" == 201 ]] || fail "POST /deliveries: ${code}: $(cat "$body")"
delivery="$(jq -r .id <"$body")"
tracking="$(jq -r '.tracking.url // empty' <"$body")"
[[ -n "$tracking" ]] || fail "the delivery has no tracking link: $(cat "$body")"

expect 200 POST "/deliveries/${delivery}/assignment" "$(jq -nc --arg c "$courier" '{courier_id: $c}')"
expect 201 POST /courier-sessions "$(jq -nc --arg c "$courier" '{courier_id: $c, mode: "link"}')"
link="$(jq -r .link_url <"$body")"
[[ "$link" == */s/* ]] || fail "unexpected link_url ${link}"

{
  echo
  echo "delivery ${delivery}, courier ${courier}"
  echo "The courier link works once, for 15 minutes. The tracking link opens on the machine running the stack."
  echo
} >&2
echo "courier:  ${link}"
echo "tracking: ${tracking}"
