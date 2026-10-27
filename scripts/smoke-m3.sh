#!/usr/bin/env bash
# M3 end-to-end smoke test. Runs against the local stack (make dev): a tenant
# with a preparation time, an end_to_end delivery with a tracking link, a
# viewer session and SSE stream, a sandbox simulation at 20x speed, then the
# trail, the proof points and a link rotation.
# Usage: make smoke   (or DTCTL=backend/bin/dtctl scripts/smoke-m3.sh)
#
# Env: DTCTL (default backend/bin/dtctl), DT_GATEWAY_URL (default
# http://127.0.0.1:8080), DT_TRACKING_URL (default http://127.0.0.1:8082, where the
# viewer streams are served), and dtctl's DT_ACCOUNTS_URL / DT_INTERNAL_RPC_TOKEN.
# Secrets stay in shell variables and are never echoed.
set -euo pipefail

DTCTL="${DTCTL:-backend/bin/dtctl}"
GATEWAY="${DT_GATEWAY_URL:-http://127.0.0.1:8080}"
TRACKING="${DT_TRACKING_URL:-http://127.0.0.1:8082}"
export DT_INTERNAL_RPC_TOKEN="${DT_INTERNAL_RPC_TOKEN:-dev-only-not-a-secret-internal-token-0000}"

body="$(mktemp)"
sse_log="$(mktemp)"
sse_pid=""
cleanup() {
  [[ -n "$sse_pid" ]] && kill "$sse_pid" 2>/dev/null || true
  rm -f "$body" "$sse_log"
}
trap cleanup EXIT

step() { echo "==> $*" >&2; }
fail() { echo "SMOKE FAIL: $*" >&2; exit 1; }

key=""
run_id="$(date -u +%Y%m%dT%H%M%S)-$$"

# call METHOD PATH [JSON]: authenticated gateway call; prints the HTTP status,
# body in $body. A fresh Idempotency-Key per call (the gateway requires it on POSTs).
call() {
  local method="$1" path="$2" data="${3-}" args=()
  [[ -n "$data" ]] && args=(-H 'Content-Type: application/json' --data "$data")
  curl -sS -o "$body" -w '%{http_code}' --max-time 10 -X "$method" \
    -H "Authorization: Bearer ${key}" -H "Idempotency-Key: smoke3-${run_id}-$(date +%s%N)" \
    "${args[@]}" "${GATEWAY}/v1${path}"
}

expect() {
  local want="$1" code
  shift
  code="$(call "$@")"
  [[ "$code" == "$want" ]] || fail "$2 $3: want ${want}, got ${code}: $(cat "$body")"
}

field() {
  local got
  got="$(jq -r "$1" <"$body")"
  [[ "$got" == "$2" ]] || fail "$1: want ${2}, got ${got}: $(cat "$body")"
}

# session TOKEN: POST /tracking/session (no API key); prints the HTTP status, body in $body.
session() {
  curl -sS -o "$body" -w '%{http_code}' --max-time 10 -X POST -H 'Content-Type: application/json' \
    --data "$(jq -nc --arg t "$1" '{token: $t}')" "${GATEWAY}/v1/tracking/session"
}

dtctl() {
  local err out
  err="$(mktemp)"
  out="$("$DTCTL" "$@" 2>"$err")" || { local msg; msg="$(cat "$err")"; rm -f "$err"; fail "dtctl $*: ${msg}"; }
  rm -f "$err"
  printf '%s' "$out"
}

step "1. create a tenant (15 minutes of preparation time) and a test key"
tenant="$(dtctl tenant create --name "Smoke M3 ${run_id}")"
[[ "$tenant" == ten_* ]] || fail "tenant id ${tenant}"
key="$(dtctl key create --tenant "$tenant" --env test --name smoke-m3)"
[[ "$key" =~ ^dt_sk_test_[0-9A-Za-z]{55}$ ]] || fail "key create did not print a test secret key"
dtctl tenant settings --tenant "$tenant" --models direct,end_to_end --default end_to_end --pickup any --prep-minutes 15 >/dev/null
echo "tenant ${tenant}" >&2

# The gateway's key cache is eventually consistent (accounts outbox -> NATS).
for _ in $(seq 1 40); do
  code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 3 -H "Authorization: Bearer ${key}" "${GATEWAY}/v1/me")"
  [[ "$code" == 200 ]] && break
  sleep 0.25
done
[[ "$code" == 200 ]] || fail "key not known to the gateway after 10 s (last status ${code})"

step "2. create an end_to_end delivery in Amsterdam; it carries a tracking link"
expect 201 POST /deliveries "$(jq -nc --arg e "order-m3-${run_id}" '{
  external_id: $e, delivery_model: "end_to_end",
  pickup: {location: {lat: 52.3731, lng: 4.8922}, name: "Smoke Kitchen"},
  dropoff: {location: {lat: 52.3600, lng: 4.8852}, label: "Smoke Door"},
  customer: {first_name: "Ana"}}')"
delivery="$(jq -r .id <"$body")"
[[ "$delivery" == del_* ]] || fail "delivery id ${delivery}"
field .status confirmed
token="$(jq -r .tracking.token <"$body")"
url="$(jq -r .tracking.url <"$body")"
[[ "$token" =~ ^[A-Za-z0-9_-]{22}$ ]] || fail "tracking.token is not a 22-character token"
[[ "$url" == */t/"$token" ]] || fail "tracking.url ${url} does not end in /t/<token>"
field .tracking.expires_at null

step "3. start a viewer session: no courier yet, ETA estimated from the preparation time"
# The tracking service learns of a new delivery through NATS, so the very first
# session can answer 503 tracking_unavailable for a moment.
for _ in $(seq 1 40); do
  code="$(session "$token")"
  [[ "$code" == 503 ]] || break
  sleep 0.25
done
[[ "$code" == 200 ]] || fail "POST /tracking/session: ${code}: $(cat "$body")"
viewer="$(jq -r .viewer_token <"$body")"
[[ -n "$viewer" && "$viewer" != null ]] || fail "no viewer_token"
field .snapshot.courier null
field .snapshot.eta_state estimated
field .snapshot.delivery_id "$delivery"

step "4. open the SSE stream in the background"
curl -sS -N --max-time 240 -H "Authorization: Bearer ${viewer}" -H 'Accept: text/event-stream' \
  "${TRACKING}/v1/tracking/stream/sse" >"$sse_log" 2>/dev/null &
sse_pid=$!
for _ in $(seq 1 40); do
  grep -q '^event: snapshot' "$sse_log" && break
  sleep 0.25
done
grep -q '^event: snapshot' "$sse_log" || fail "the SSE stream sent no first snapshot"

step "5. simulate at speed_factor 20 and wait for delivered"
expect 202 POST "/test/deliveries/${delivery}/simulate" '{"speed_factor":20}'
status=""
for _ in $(seq 1 240); do
  expect 200 GET "/deliveries/${delivery}"
  status="$(jq -r .status <"$body")"
  [[ "$status" == delivered ]] && break
  sleep 1
done
[[ "$status" == delivered ]] || fail "the simulated delivery is ${status} after 240 s"
sleep 2 # the final snapshot reaches the stream

step "6. the SSE log saw signal values and ended on a snapshot without a courier"
kill "$sse_pid" 2>/dev/null || true
wait "$sse_pid" 2>/dev/null || true
sse_pid=""
signals="$(grep -o '"signal":"[a-z]*"' "$sse_log" | sort -u | tr '\n' ' ')"
[[ -n "$signals" ]] || fail "no courier.signal value in the SSE log"
echo "   signals seen: ${signals}" >&2
last="$(awk '/^event: /{ev=$2} /^data: /{ if (ev == "snapshot") last=substr($0, 7) } END{print last}' "$sse_log")"
[[ -n "$last" ]] || fail "no snapshot in the SSE log"
[[ "$(jq -r '.data.status' <<<"$last")" == delivered ]] || fail "last snapshot status is not delivered: ${last}"
[[ "$(jq -r '.data.courier' <<<"$last")" == null ]] || fail "last snapshot still shows the courier: ${last}"

step "7. the trail and the proof points are not empty"
expect 200 GET "/deliveries/${delivery}/trail"
trail="$(jq '.data | length' <"$body")"
(( trail > 0 )) || fail "the trail is empty: $(cat "$body")"
field .raw_available true
expect 200 GET "/deliveries/${delivery}/proof-points"
points="$(jq '.data | length' <"$body")"
(( points > 0 )) || fail "no proof points: $(cat "$body")"
echo "   ${trail} trail fixes, ${points} proof points" >&2

step "8. rotate the link: a session with the old token is refused (401)"
expect 200 POST "/deliveries/${delivery}/tracking-link/rotate"
new_token="$(jq -r .token <"$body")"
[[ "$new_token" =~ ^[A-Za-z0-9_-]{22}$ && "$new_token" != "$token" ]] || fail "rotation did not return a new token"
code="$(session "$token")"
[[ "$code" == 401 ]] || fail "old token session: want 401, got ${code}: $(cat "$body")"
code="$(session "$new_token")"
[[ "$code" == 200 ]] || fail "new token session: want 200, got ${code}: $(cat "$body")"

echo "SMOKE M3 OK"
echo OK
