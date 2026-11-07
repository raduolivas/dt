#!/usr/bin/env bash
# Break (or heal) one Mission Control simulation scenario against the local DT stack.
#   1 phone stops, 2 invalid courier token, 3 rate limit, 4 delivery reassigned  -> the sim API of mc-backend
#   5 Redis paused, 6 OSRM stopped                                                -> Docker, from this script only
# mc-backend never touches Docker (no socket, no shell); scenarios 5 and 6 are run here and nowhere else.
#
# Usage: mc-break.sh <1..6> [heal]      (via the root Makefile: make mc-break SCENARIO=5 [HEAL=1])
# Env: MC_URL (default http://127.0.0.1:8095), DT_COMPOSE_PROJECT (default dt), DT_COMPOSE_FILE (default
# infra/compose/compose.yaml), MC_BREAK_RUN (a simulated delivery id; default: the newest live run).
#
# Scenario 4: DT refuses a reassignment from picked_up on, and a normal run confirms pickup at once, so this script
# makes a new run that holds its pickup (POST /api/sim/runs?pickup=hold) and reassigns that one. Heal reassigns it
# back and releases the pickup.
#
# Scenario 6: OSRM is untraced and DT calls it only when a leg is built, so stopping it is not enough. After the
# stop this script creates a fresh simulated delivery, which makes tracking build a route; tracking then logs
# route_fallback, which is what the dependency_failing detector reads (ruling R28). Heal restarts both OSRM
# containers and waits for them to be healthy.
set -euo pipefail
trap 'command rm -f /tmp/mc-break.$$' EXIT

MC="${MC_URL:-http://127.0.0.1:8095}"
PROJECT="${DT_COMPOSE_PROJECT:-dt}"
FILE="${DT_COMPOSE_FILE:-infra/compose/compose.yaml}"
dc() { docker compose -p "$PROJECT" -f "$FILE" "$@"; }
fail() { echo "mc-break: $*" >&2; exit 1; }

scenario="${1:-}"
mode="${2:-break}"
[[ "$scenario" =~ ^[1-6]$ ]] || fail "usage: mc-break.sh <1..6> [heal]"
[[ "$mode" == break || "$mode" == heal ]] || fail "second argument must be 'heal' or empty"

# run_id prints the delivery id of the run to act on (newest live run unless MC_BREAK_RUN is set).
run_id() {
  local id="${MC_BREAK_RUN:-}"
  if [[ -z "$id" ]]; then
    id="$(curl -fsS --max-time 5 "${MC}/api/sim/status" | jq -r '.runs | last | .delivery_id // empty')" ||
      fail "no simulation API at ${MC} (run make mc-sim first)"
  fi
  [[ -n "$id" ]] || fail "no simulated run is live (run make mc-sim first)"
  printf '%s' "$id"
}
sim_post() { # path [json]
  local code
  code="$(curl -sS -o /tmp/mc-break.$$ -w '%{http_code}' --max-time 15 -X POST -H 'Content-Type: application/json' \
    ${2:+--data "$2"} "${MC}/api/sim$1")"
  [[ "$code" == 204 || "$code" == 201 ]] || fail "POST /api/sim$1: status ${code}: $(jq -r .error 2>/dev/null </tmp/mc-break.$$ || true)"
}

case "${scenario}:${mode}" in
  4:break)
    # DT reassigns only before pickup, and the normal run confirms pickup at once: make a run that holds it.
    sim_post "/runs?pickup=hold"
    id="$(jq -r .delivery_id </tmp/mc-break.$$)"
    echo "new held run ${id}; letting its courier start"
    sleep 6
    sim_post "/runs/${id}/break" "{\"scenario\":4}"
    echo "scenario 4 on ${id} (heal reassigns back and releases its pickup)"
    ;;
  [1-4]:break) id="$(run_id)"; sim_post "/runs/${id}/break" "{\"scenario\":${scenario}}"; echo "scenario ${scenario} on ${id}" ;;
  [1-4]:heal) id="$(run_id)"; sim_post "/runs/${id}/heal"; echo "healed ${id}" ;;
  5:break) dc pause redis; echo "redis paused" ;;
  5:heal) dc unpause redis; echo "redis unpaused" ;;
  6:break)
    dc stop osrm-car osrm-bike
    echo "osrm stopped; forcing a routing call with a fresh simulated delivery"
    sim_post "/runs"
    ;;
  6:heal)
    dc start osrm-car osrm-bike
    for _ in $(seq 1 60); do
      st="$(dc ps --format '{{.Service}}={{.Health}}' osrm-car osrm-bike | tr '\n' ' ')"
      [[ "$st" == *osrm-car=healthy* && "$st" == *osrm-bike=healthy* ]] && { echo "osrm healthy"; exit 0; }
      sleep 1
    done
    fail "osrm did not become healthy within 60 s: ${st}"
    ;;
esac
