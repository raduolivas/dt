#!/usr/bin/env bash
# Waits until every local service answers /readyz with 200.
# Usage: scripts/wait-ready.sh [timeout_seconds=60]
set -euo pipefail

timeout="${1:-60}"
deadline=$(( $(date +%s) + timeout ))
services=(gateway:8080 deliveries:8081 tracking:8082 webhooks:8083 accounts:8084)
# Services that serve internal RPC answer over mTLS only.
mtls=" deliveries accounts "
certs="$(cd "$(dirname "$0")/.." && pwd)/infra/compose/certs"

for entry in "${services[@]}"; do
  name="${entry%%:*}"
  port="${entry##*:}"
  scheme=http
  curl_tls=()
  if [[ "$mtls" == *" ${name} "* ]]; then
    scheme=https
    curl_tls=(--cacert "$certs/ca.pem" --cert "$certs/dtctl.pem" --key "$certs/dtctl.key")
  fi
  until curl -fsS -o /dev/null --max-time 3 "${curl_tls[@]}" "${scheme}://127.0.0.1:${port}/readyz"; do
    if (( $(date +%s) >= deadline )); then
      echo "${name} not ready after ${timeout}s" >&2
      exit 1
    fi
    sleep 1
  done
  echo "${name} ready"
done
