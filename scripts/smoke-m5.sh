#!/usr/bin/env bash
# M5 end-to-end smoke test. Runs against the local stack (make dev): a courier link session, the code exchange, three
# fixes, a pickup confirmation from the courier (policy "any"), delivered twice with one event id (duplicate), then
# revocation and the used link code. The courier page is served at the courier service (:8092) with its security
# headers.
# Usage: make smoke   (or DTCTL=backend/bin/dtctl scripts/smoke-m5.sh)
#
# Env: DTCTL (default backend/bin/dtctl), DT_GATEWAY_URL (default http://127.0.0.1:8080), DT_COURIER_PAGE_URL (default
# http://127.0.0.1:8092; empty skips the page check), and dtctl's DT_ACCOUNTS_URL / DT_INTERNAL_RPC_TOKEN.
# DT_SMOKE_KEY: an existing test secret key whose tenant allows direct deliveries with pickup policy "any"; step 1 is
# then skipped and dtctl is never called. Secrets stay in shell variables and are never echoed.
set -euo pipefail

DTCTL="${DTCTL:-backend/bin/dtctl}"
GATEWAY="${DT_GATEWAY_URL:-http://127.0.0.1:8080}"
PAGE="${DT_COURIER_PAGE_URL-http://127.0.0.1:8092}"
export DT_INTERNAL_RPC_TOKEN="${DT_INTERNAL_RPC_TOKEN:-dev-only-not-a-secret-internal-token-0000}"

body="$(mktemp)"
headers="$(mktemp)"
trap 'rm -f "$body" "$headers"' EXIT

step() { echo "==> $*" >&2; }
fail() { echo "SMOKE FAIL: $*" >&2; exit 1; }

key=""
run_id="$(date -u +%Y%m%dT%H%M%S)-$$"

# call METHOD PATH [JSON]: partner call with the secret key; prints the HTTP status, body in $body.
call() {
  local method="$1" path="$2" data="${3-}" args=()
  [[ -n "$data" ]] && args=(-H 'Content-Type: application/json' --data "$data")
  curl -sS -o "$body" -w '%{http_code}' --max-time 10 -X "$method" \
    -H "Authorization: Bearer ${key}" -H "Idempotency-Key: smoke5-${run_id}-$(date +%s%N)" \
    "${args[@]}" "${GATEWAY}/v1${path}"
}
expect() {
  local want="$1" code
  shift
  code="$(call "$@")"
  [[ "$code" == "$want" ]] || fail "$1 $2: want ${want}, got ${code}: $(cat "$body")"
}

# ccall METHOD PATH [JSON]: courier call with $access (empty: no token); prints the HTTP status, body in $body.
access=""
ccall() {
  local method="$1" path="$2" data="${3-}" args=()
  [[ -n "$data" ]] && args=(-H 'Content-Type: application/json' --data "$data")
  [[ -n "$access" ]] && args+=(-H "Authorization: Bearer ${access}")
  curl -sS -o "$body" -w '%{http_code}' --max-time 10 -X "$method" "${args[@]}" "${GATEWAY}/v1/courier${path}"
}
cexpect() {
  local want="$1" code
  shift
  code="$(ccall "$@")"
  [[ "$code" == "$want" ]] || fail "courier $1 $2: want ${want}, got ${code}: $(cat "$body")"
}

dtctl() {
  local err out
  err="$(mktemp)"
  out="$("$DTCTL" "$@" 2>"$err")" || { local msg; msg="$(cat "$err")"; rm -f "$err"; fail "dtctl $*: ${msg}"; }
  rm -f "$err"
  printf '%s' "$out"
}

field() {
  local got
  got="$(jq -r "$1" <"$body")"
  [[ "$got" == "$2" ]] || fail "$1: want ${2}, got ${got}: $(cat "$body")"
}

if [[ -n "${DT_SMOKE_KEY:-}" ]]; then
  step "1. use the test key from DT_SMOKE_KEY (its tenant is set up already)"
  key="$DT_SMOKE_KEY"
  [[ "$key" == dt_sk_test_* ]] || fail "DT_SMOKE_KEY is not a test secret key"
else
  step "1. create a tenant (direct deliveries, pickup policy any) and a test key"
  tenant="$(dtctl tenant create --name "Smoke M5 ${run_id}")"
  [[ "$tenant" == ten_* ]] || fail "tenant id ${tenant}"
  key="$(dtctl key create --tenant "$tenant" --env test --name smoke-m5)"
  [[ "$key" =~ ^dt_sk_test_[0-9A-Za-z]{55}$ ]] || fail "key create did not print a test secret key"
  dtctl tenant settings --tenant "$tenant" --models direct,end_to_end --default direct --pickup any >/dev/null
  echo "tenant ${tenant}" >&2
fi

# The gateway's key cache is eventually consistent (accounts outbox -> NATS).
code=""
for _ in $(seq 1 40); do
  code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 3 -H "Authorization: Bearer ${key}" "${GATEWAY}/v1/me")" || true
  [[ "$code" == 200 ]] && break
  sleep 0.25
done
[[ "$code" == 200 ]] || fail "key not known to the gateway after 10 s (last status ${code})"

if [[ -n "$PAGE" ]]; then
  step "2. the courier page: /s/<code> and / are the shell, with the security headers"
  for path in /s/abc /; do
    code="$(curl -sS -D "$headers" -o "$body" -w '%{http_code}' --max-time 5 "${PAGE}${path}")" || fail "GET ${PAGE}${path} (is the courier service up? run make dev)"
    [[ "$code" == 200 ]] || fail "GET ${PAGE}${path}: want 200, got ${code} (run make web-assets)"
    grep -q '<div id="root">' "$body" || fail "GET ${PAGE}${path} is not the page shell"
    grep -qi "^content-security-policy:.*default-src 'self'.*frame-ancestors 'none'" "$headers" || fail "${path}: no CSP header with frame-ancestors"
    grep -qi '^referrer-policy: no-referrer' "$headers" || fail "${path}: no Referrer-Policy header"
    grep -qi '^x-robots-tag: noindex' "$headers" || fail "${path}: no X-Robots-Tag header"
  done
fi

step "3. a courier, a direct delivery assigned to it, and a link session"
expect 201 PUT /couriers/smoke-courier '{"display_name":"Smoke Courier","vehicle":"e_bike"}'
courier="$(jq -r .id <"$body")"
[[ "$courier" == cou_* ]] || fail "courier id ${courier}"
# a delivery created right after the tenant's settings can race them to the deliveries service; retry briefly
for _ in $(seq 1 20); do
  code="$(call POST /deliveries "$(jq -nc --arg e "order-m5-${run_id}" '{
    external_id: $e, delivery_model: "direct",
    pickup: {location: {lat: 52.3731, lng: 4.8922}, name: "Smoke Kitchen"},
    dropoff: {location: {lat: 52.3600, lng: 4.8852}, label: "Smoke Door"},
    customer: {first_name: "Ana"}}')")"
  [[ "$code" == 201 ]] && break
  sleep 0.5
done
[[ "$code" == 201 ]] || fail "POST /deliveries: ${code}: $(cat "$body")"
delivery="$(jq -r .id <"$body")"
expect 200 POST "/deliveries/${delivery}/assignment" "$(jq -nc --arg c "$courier" '{courier_id: $c}')"
field .courier.id "$courier"
expect 201 POST /courier-sessions "$(jq -nc --arg c "$courier" '{courier_id: $c, mode: "link"}')"
session="$(jq -r .session.id <"$body")"
link="$(jq -r .link_url <"$body")"
code_value="${link##*/s/}"
[[ "$session" == cs_* && -n "$code_value" && "$code_value" != "$link" ]] || fail "session ${session}, link_url ${link}"
field .session.status pending_link

step "4. exchange the link code; the session is active"
exchange="$(jq -nc --arg c "$code_value" '{grant_type: "link_code", code: $c}')"
cexpect 200 POST /token "$exchange"
access="$(jq -r .access_token <"$body")"
[[ -n "$access" && "$access" != null && "$(jq -r ".refresh_token | length > 20" <"$body")" == true ]] || fail "the token exchange answered no token pair"
expect 200 GET "/courier-sessions/${session}"
field .status active
cexpect 200 GET /deliveries
field ".deliveries | map(select(.id == \"${delivery}\")) | length" 1

step "5. three fixes are accepted"
# The assignment reaches the tracking service through NATS a moment after it is made; until then the fixes are
# dropped (no assigned delivery yet), so post again with fresh timestamps for up to ~22 s.
for _ in $(seq 1 20); do
  now="$(date +%s)"
  fixes="$(jq -nc --argjson n "$now" '{fixes: [range(0; 3) | {
    t: (($n - 3 + .) | todate), lat: (52.3731 + . * 0.0001), lng: 4.8922, accuracy_m: 6.5, speed_mps: 4.8, heading_deg: 84}]}')"
  cexpect 200 POST /locations "$fixes"
  [[ "$(jq -r .accepted <"$body")" == 3 ]] && break
  sleep 1.1 # the location limit is 1 batch/s
done
field .accepted 3
field .dropped 0

step "6. pickup_confirmed from the courier: picked_up under policy any"
cexpect 200 POST "/deliveries/${delivery}/events" "$(jq -nc --arg e "smoke5-${run_id}-pickup" --arg t "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '{event_id: $e, type: "pickup_confirmed", occurred_at: $t}')"
field .outcome applied
field .delivery.status picked_up
expect 200 GET "/deliveries/${delivery}"
field .status picked_up

step "6b. delivered, then the same event again: duplicate (the delivery is final and off the courier's list)"
delivered="$(jq -nc --arg e "smoke5-${run_id}-delivered" --arg t "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '{event_id: $e, type: "delivered", occurred_at: $t}')"
cexpect 200 POST "/deliveries/${delivery}/events" "$delivered"
field .outcome applied
field .delivery.status delivered
cexpect 200 POST "/deliveries/${delivery}/events" "$delivered"
field .outcome duplicate
field .delivery.status delivered

step "7. revoke the session: the next location post is 401"
expect 200 DELETE "/courier-sessions/${session}"
sleep 1.1 # past the 1 batch/s location limit, so the answer is the revocation and not a 429
cexpect 401 POST /locations "$fixes"

step "8. exchanging the same code again: 401 link_code_used"
cexpect 401 POST /token "$exchange"
grep -q 'link_code_used' "$body" || fail "want link_code_used: $(cat "$body")"

echo "SMOKE M5 OK"
echo OK
