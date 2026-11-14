#!/usr/bin/env bash
# OBS smoke step. Runs against the local stack (make dev): drives fixes through the gateway with viewers connected
# (backend/tools/latency, 20 s, 2 deliveries, 1 viewer each), then checks that
#   1. metrics: dt_tracking_fix_to_viewer_duration_seconds_count{source="relay"} > 0 in Prometheus (the fix start time
#      reached Publisher.Snapshot);
#   2. rules:   at least one mc:wire_requests:rate1m series exists;
#   3. traces:  Tempo has one trace with spans from both gateway and tracking;
#   4. logs:    Loki has a line carrying that trace_id (structured metadata);
#   5. leaks:   no dt_sk_/dt_pk_ in any Loki line of the run, and neither those nor the latency tool's coordinates in
#      the trace JSON.
# Prometheus, Tempo and Loki publish no host ports: they are reached through the grafana container (busybox wget).
# Skipped (loudly) when OBS=0, or when the stack runs without OBS (OBS=0 make dev). Fails when OBS containers exist or
# the gateway still exports but grafana is not running. Usage: make smoke   (or DTCTL=backend/bin/dtctl scripts/smoke-obs.sh)
# Env: DTCTL (default backend/bin/dtctl), DT_GATEWAY_URL (default http://127.0.0.1:8080), OBS_COMPOSE_PROJECT (default dt),
# OBS_WAIT_SECONDS (default 45), and dtctl's DT_ACCOUNTS_URL / DT_INTERNAL_RPC_TOKEN.
set -euo pipefail

fail() { echo "SMOKE FAIL (obs: $1): $2" >&2; exit 1; }
ok() { echo "OK obs: $*"; }

if [[ "${OBS-}" == "0" ]]; then
  echo "!!!!!! smoke-obs: SKIPPED (OBS=0) !!!!!!"
  exit 0
fi

DTCTL="${DTCTL:-backend/bin/dtctl}"
GATEWAY="${DT_GATEWAY_URL:-http://127.0.0.1:8080}"
PROJECT="${OBS_COMPOSE_PROJECT:-dt}"
WAIT="${OBS_WAIT_SECONDS:-45}"
export DT_INTERNAL_RPC_TOKEN="${DT_INTERNAL_RPC_TOKEN:-dev-only-not-a-secret-internal-token-0000}"

if [[ -z "$(docker compose -p "$PROJECT" ps -q --status running grafana 2>/dev/null)" ]]; then
  # No grafana: only a stack started with OBS=0 may skip. Any OBS container, or a service that still exports, is a failure.
  present="$(docker compose -p "$PROJECT" ps -a --format '{{.Service}}' 2>/dev/null | grep -E '^(otel-collector|prometheus|tempo|loki|grafana)$' | tr '\n' ' ' || true)"
  endpoint="$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "${PROJECT}-gateway-1" 2>/dev/null | sed -n 's/^OTEL_EXPORTER_OTLP_ENDPOINT=//p' || true)"
  if [[ -n "$present" ]]; then
    fail setup "OBS containers exist (${present}) but grafana is not running"
  elif [[ -n "$endpoint" ]]; then
    fail setup "the gateway exports to ${endpoint} but no OBS stack runs (make dev without OBS=0?)"
  fi
  echo "!!!!!! smoke-obs: SKIPPED - the stack runs without OBS (OBS=0 make dev): no OBS container, gateway exports nothing !!!!!!"
  exit 0
fi

PROM=http://prometheus:9090
TEMPO=http://tempo:3200
LOKI=http://loki:3100

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT


# get URL: GET inside the compose network through the grafana container.
get() { docker compose -p "$PROJECT" exec -T grafana wget -q -O - "$1"; }
# q BASE PATH key=value...: GET with URL-encoded query parameters.
q() {
  local base="$1" path="$2" url sep='?' kv
  shift 2
  url="${base}${path}"
  for kv in "$@"; do
    url+="${sep}${kv%%=*}=$(jq -rn --arg v "${kv#*=}" '$v|@uri')"
    sep='&'
  done
  get "$url"
}
# poll NAME SECONDS CMD...: run CMD every 2 s until it succeeds or SECONDS pass.
poll() {
  local name="$1" secs="$2" end
  shift 2
  end=$((SECONDS + secs))
  while ((SECONDS < end)); do
    if "$@" 2>/dev/null; then return 0; fi
    sleep 2
  done
  fail "$name" "no result within ${secs}s"
}

# ---- load: the latency tool through the gateway ----------------------------------------------------------------
[[ -x "$DTCTL" ]] || fail setup "dtctl not found at ${DTCTL} (make dtctl)"
tenant="$("$DTCTL" tenant create --name "Smoke OBS")"
"$DTCTL" tenant settings --tenant "$tenant" --models direct,end_to_end --default end_to_end --pickup any >/dev/null
key="$("$DTCTL" key create --tenant "$tenant" --env test --name "smoke obs")"
ready=""
for _ in $(seq 1 40); do
  [[ "$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer ${key}" "${GATEWAY}/v1/me")" == 200 ]] && ready=1 && break
  sleep 0.25
done
[[ -n "$ready" ]] || fail setup "the gateway did not accept the new key within 10 s"

# Coordinates the latency tool sends (backend/tools/latency/main.go): pickup 52.3731,4.8922; dropoff 52.3600,4.8852;
# fixes 52.3676 +- 0.0x and lng 4.9000 + idx*0.0015. Any of these in a trace is a leak.
coord_re='52\.3[0-9]|4\.8922|4\.8852|4\.90[0-9]{2}'
# Personal-data strings the tool sends: pickup name, dropoff label, courier display names ("Latency 0", "Latency 1").
pii_re='Latency (pickup|dropoff|[0-9])'

# Baseline right before the load (M1-M5 ran before this step and also made fixes): check 1 needs a real increase.
scalar() { jq -r '.data.result[0].value[1] // "0"'; }
RELAY_Q='sum(dt_tracking_fix_to_viewer_duration_seconds_count{source="relay"}) or vector(0)'
relay_before="$(q "$PROM" /api/v1/query "query=${RELAY_Q}" | scalar)"
start_ns="$(date +%s)000000000"
echo "==> obs: driving fixes for 20 s (2 deliveries, 1 viewer each)" >&2
# The tool opens a viewer right after assigning the courier; tracking learns of the delivery over NATS, so under load
# that first session can answer 503 tracking_unavailable. A failed start is retried (twice); the run window starts anew.
attempt=0
until (cd backend/tools/latency && DT_API_KEY="$key" go run . -api "$GATEWAY" -deliveries 2 -viewers 1 -duration 20s) >"$work/latency.out" 2>&1; do
  attempt=$((attempt + 1))
  if ((attempt >= 3)); then cat "$work/latency.out" >&2; fail load "the latency tool failed ${attempt} times"; fi
  echo "==> obs: latency tool failed (attempt ${attempt}: $(grep -m1 '^latency:' "$work/latency.out" | cut -c1-120)); retrying" >&2
  sleep 3
  start_ns="$(date +%s)000000000"
done
end_ns="$(( $(date +%s) + 1 ))000000000"

# ---- 1. metrics ------------------------------------------------------------------------------------------------
# The one watched delivery sees about 20 relayed fixes (measured: 19); require at least 10 more than the baseline, which also
# rules out the tail of M3's export (the metric export interval is 15 s).
relay_count() {
  local n
  n="$(q "$PROM" /api/v1/query "query=${RELAY_Q}" | scalar)"
  awk -v n="$n" -v b="$relay_before" 'BEGIN{exit !(n - b >= 10)}'
}
poll fix-to-viewer "$WAIT" relay_count
ok "fix-to-viewer relay count rose from ${relay_before} by at least 10 during the run"

# ---- 2. recording rules ----------------------------------------------------------------------------------------
# The courier wire is the one the tool drives with fixes: a positive rate within the run window (not just a series).
wire_positive() {
  local w=$(($(date +%s) - start_ns / 1000000000 + 1))
  [[ "$(q "$PROM" /api/v1/query "query=count(max_over_time(mc:wire_requests:rate1m{wire=\"courier>gateway.courier_api\"}[${w}s]) > 0)" | scalar)" -ge 1 ]]
}
poll wire-requests "$WAIT" wire_positive
ok "mc:wire_requests:rate1m{courier>gateway.courier_api} > 0 within the run window"

# ---- 3. traces -------------------------------------------------------------------------------------------------
# Candidates: traces with spans from both services (Tempo's search drops leading zeros of the id: pad to 32 digits).
candidates="$work/candidates"
find_traces() {
  q "$TEMPO" /api/search 'q={ resource.service.name = "gateway" } && { resource.service.name = "tracking" }' \
    "start=$((start_ns / 1000000000 - 5))" "end=$((end_ns / 1000000000 + 60))" limit=200 \
    | jq -r '.traces[]?.traceID | ("00000000000000000000000000000000" + .)[-32:]' >"$candidates" && [[ -s "$candidates" ]]
}
poll trace "$WAIT" find_traces
ok "$(wc -l <"$candidates") trace(s) span gateway and tracking"

# ---- 4. logs ---------------------------------------------------------------------------------------------------
# Only some requests log a business event, so the trace with a log line is picked among the candidates: trace_ids of
# Loki lines of the run (structured metadata) intersected with the candidates.
trace_id=""
find_log() {
  q "$LOKI" /loki/api/v1/query_range 'query={service_name=~".+"} | trace_id=~".+"' \
    "start=$((start_ns - 60000000000))" "end=$((end_ns + 60000000000))" limit=1000 \
    | jq -r '.data.result[].stream.trace_id' | sort -u >"$work/log-traces" || return 1
  trace_id="$(sort -u "$candidates" | comm -12 - "$work/log-traces" | head -n 1)"
  [[ -n "$trace_id" ]]
}
poll log "$WAIT" find_log
get "${TEMPO}/api/traces/${trace_id}" >"$work/trace.json" || fail trace "could not fetch trace ${trace_id}"
for svc in gateway tracking; do
  jq -e --arg s "$svc" '[.. | objects | select(.key? == "service.name") | .value.stringValue] | index($s) != null' "$work/trace.json" >/dev/null \
    || fail trace "trace ${trace_id} has no span from service.name=${svc}"
done
ok "trace ${trace_id} spans gateway and tracking"
ok "Loki has a log line with trace_id ${trace_id}"

# ---- 5. leak check ---------------------------------------------------------------------------------------------
loki_leaks="$(q "$LOKI" /loki/api/v1/query_range 'query={service_name=~".+"} |~ "dt_sk_|dt_pk_|Latency (pickup|dropoff|[0-9])|52[.]3731|4[.]8922|4[.]8852"' \
  "start=$((start_ns - 60000000000))" "end=$((end_ns + 60000000000))" limit=5 \
  | jq -r '[.data.result[].values[]] | length')" || fail leak "the Loki leak query failed"
[[ "$loki_leaks" == 0 ]] || fail leak "${loki_leaks} Loki line(s) in the run window contain a key prefix, a latency-tool name or a coordinate"
# Tempo's JSON for the trace found above and up to 10 other candidates (fix traces, where coordinates would show).
cp "$work/trace.json" "$work/t-0.json"
i=0
for t in $(grep -vx "$trace_id" "$candidates" | head -n 10); do
  i=$((i + 1))
  get "${TEMPO}/api/traces/${t}" >"$work/t-${i}.json" || fail leak "could not fetch trace ${t}"
done
if grep -Elq 'dt_sk_|dt_pk_' "$work"/t-*.json; then fail leak "a trace contains an API key prefix"; fi
if grep -Eq "$pii_re" "$work"/t-*.json; then fail leak "a trace contains a latency-tool name ($(grep -Eho "$pii_re" "$work"/t-*.json | sort -u | head -3 | tr '\n' ' '))"; fi
if grep -Elq "$coord_re" "$work"/t-*.json; then
  fail leak "a trace contains a coordinate string ($(grep -Eho "$coord_re" "$work"/t-*.json | sort -u | head -3 | tr '\n' ' '))"
fi
ok "no keys in logs, no keys or coordinates in $((i + 1)) trace(s)"
echo "OK obs: all checks passed"
