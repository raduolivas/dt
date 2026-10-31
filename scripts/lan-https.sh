#!/usr/bin/env bash
# Serves the courier page and the courier API over HTTPS on the LAN, so a phone can use geolocation (iOS needs a
# secure context) and the https page can call an https API (no mixed content).
#   https://<LAN-IP>:8443  the courier page      https://<LAN-IP>:8444  the courier API (only /v1/courier/)
#   http://<LAN-IP>:8445/dt-local-ca.crt          the local CA to install on the phone
# It makes a local CA once (build/lan/ca, reused so the phone's trust survives re-runs) and a fresh server
# certificate for the LAN address, builds the courier page for the https API, points the gateway's COURIER_URL_BASE
# at the page, and starts the two nginx services (compose profile "lan"). Needs the stack (make dev).
# Usage: make lan-https   (or DT_LAN_IP=192.168.1.50 scripts/lan-https.sh)
# Env: DT_LAN_IP (default: the address of the default route), COMPOSE_OVERRIDE (as in the Makefile).
# See docs/runbooks/courier-phone-test.md for the phone steps.
set -euo pipefail

cd "$(dirname "$0")/.."
lan="build/lan"
ca_dir="${lan}/ca"
env_file="${lan}/lan.env"

fail() { echo "lan-https: $*" >&2; exit 1; }

ip="${DT_LAN_IP:-}"
if [[ -z "$ip" ]]; then
  ip="$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for (i = 1; i < NF; i++) if ($i == "src") {print $(i + 1); exit}}')" || true
fi
if [[ -z "$ip" ]]; then
  ip="$(hostname -I 2>/dev/null | awk '{print $1}')" || true
fi
[[ "$ip" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "could not find the LAN address; set DT_LAN_IP=<address>"

mkdir -p "$ca_dir"

# The CA lives in build/lan/ca as ca.pem + ca.key, created once. The server certificate is remade on every run
# (cheap, and it follows the LAN address when DHCP hands out a new one).
if command -v mkcert >/dev/null 2>&1; then
  echo "==> certificates with mkcert (CA in ${ca_dir}; the system trust store is not touched)" >&2
  CAROOT="$(pwd)/${ca_dir}" mkcert -cert-file "${lan}/server.pem" -key-file "${lan}/server.key" "$ip" localhost 127.0.0.1 >/dev/null
  cp "${ca_dir}/rootCA.pem" "${ca_dir}/ca.pem"
else
  echo "==> certificates with openssl (CA in ${ca_dir})" >&2
  if [[ ! -f "${ca_dir}/ca.pem" || ! -f "${ca_dir}/ca.key" ]]; then
    openssl req -x509 -newkey rsa:2048 -nodes -days 3650 -keyout "${ca_dir}/ca.key" -out "${ca_dir}/ca.pem" \
      -subj "/CN=DT local development CA" \
      -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign" 2>/dev/null
  fi
  openssl req -newkey rsa:2048 -nodes -keyout "${lan}/server.key" -out "${lan}/server.csr" \
    -subj "/CN=${ip}" 2>/dev/null
  ext="$(mktemp)"
  cat >"$ext" <<EXT
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=IP:${ip},IP:127.0.0.1,DNS:localhost
EXT
  openssl x509 -req -in "${lan}/server.csr" -CA "${ca_dir}/ca.pem" -CAkey "${ca_dir}/ca.key" -CAcreateserial \
    -out "${lan}/server.pem" -days 365 -extfile "$ext" 2>/dev/null
  rm -f "$ext" "${lan}/server.csr" "${ca_dir}/ca.srl"
fi
# The nginx image runs as an unprivileged user; this is a throwaway dev key for a LAN-only listener.
chmod 644 "${lan}/server.key"
chmod 600 "${ca_dir}"/*.key "${ca_dir}"/rootCA-key.pem 2>/dev/null || true

api="https://${ip}:8444"
sed "s|__API_ORIGIN__|${api}|g" infra/compose/courier/nginx-lan.conf.tmpl >"${lan}/courier.conf"

echo "==> building the courier page for ${api}/v1" >&2
VITE_DT_API="${api}/v1" pnpm -C web --filter @dt/courier-page build >/dev/null
rm -rf build/courier-lan
cp -R web/apps/courier-page/dist build/courier-lan

cat >"$env_file" <<ENV
# Written by scripts/lan-https.sh; the Makefile passes it to Compose. Delete build/lan to go back to localhost only.
DT_LAN_IP=${ip}
COURIER_URL_BASE=https://${ip}:8443
COMPOSE_PROFILES=lan
ENV

echo "==> starting the HTTPS services and pointing the gateway at https://${ip}:8443" >&2
compose=(docker compose --env-file "$(pwd)/${env_file}" -f infra/compose/compose.yaml)
[[ -n "${COMPOSE_OVERRIDE:-}" ]] && compose+=(-f "$COMPOSE_OVERRIDE")
"${compose[@]}" up -d --force-recreate courier-lan api-lan
"${compose[@]}" up -d --no-deps gateway
scripts/wait-ready.sh >&2

# the phone gets the page and the API through these addresses; check both answer
curl -fsS --max-time 5 --cacert "${ca_dir}/ca.pem" -o /dev/null "https://${ip}:8443/" || fail "https://${ip}:8443/ does not answer"
code="$(curl -sS --max-time 5 --cacert "${ca_dir}/ca.pem" -o /dev/null -w '%{http_code}' -X POST \
  -H 'Content-Type: application/json' --data '{"grant_type":"link_code","code":"x"}' "${api}/v1/courier/token")"
[[ "$code" == 401 ]] || fail "${api}/v1/courier/token: want 401 for a bad code, got ${code}"

cat >&2 <<MSG

HTTPS is up on ${ip}.
  1. On the phone (same Wi-Fi), open http://${ip}:8445/dt-local-ca.crt and install the profile, then trust it:
     Settings > General > About > Certificate Trust Settings > enable "DT local development CA".
  2. Run: make courier-link   and open the courier link on the phone.
Details: docs/runbooks/courier-phone-test.md
MSG
