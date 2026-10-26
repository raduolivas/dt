#!/usr/bin/env bash
# Writes a throwaway dev CA and one certificate per service (plus dtctl) to
# infra/compose/certs/ for the internal mTLS of the local stack. Existing
# files are kept; delete the directory to start over. Dev only: the keys are
# world-readable so the containers' non-root users can read them.
# Usage: scripts/dev-certs.sh [dir]
set -euo pipefail

dir="${1:-$(cd "$(dirname "$0")/.." && pwd)/infra/compose/certs}"
names=(accounts deliveries tracking gateway webhooks dtctl)

if [[ -f "$dir/ca.pem" ]]; then
  missing=0
  for n in "${names[@]}"; do
    [[ -f "$dir/$n.pem" && -f "$dir/$n.key" ]] || missing=1
  done
  (( missing == 0 )) && exit 0
  rm -rf "$dir"
fi

mkdir -p "$dir"
umask 077
openssl ecparam -name prime256v1 -genkey -noout -out "$dir/ca.key"
openssl req -x509 -new -key "$dir/ca.key" -sha256 -days 3650 \
  -subj "/CN=DT dev CA" -out "$dir/ca.pem" \
  -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign"

for n in "${names[@]}"; do
  openssl ecparam -name prime256v1 -genkey -noout -out "$dir/$n.key"
  openssl req -new -key "$dir/$n.key" -subj "/CN=$n" -out "$dir/$n.csr"
  cat > "$dir/$n.ext" <<EXT
basicConstraints=CA:FALSE
keyUsage=digitalSignature
extendedKeyUsage=serverAuth,clientAuth
subjectAltName=DNS:$n,DNS:localhost,IP:127.0.0.1
EXT
  openssl x509 -req -in "$dir/$n.csr" -CA "$dir/ca.pem" -CAkey "$dir/ca.key" -CAcreateserial \
    -sha256 -days 825 -extfile "$dir/$n.ext" -out "$dir/$n.pem" 2>/dev/null
  rm -f "$dir/$n.csr" "$dir/$n.ext"
done
rm -f "$dir/ca.srl"
# The CA key stays private; everything else must be readable by container users.
chmod 644 "$dir"/*.pem "$dir"/*.key
chmod 600 "$dir/ca.key"
chmod 755 "$dir"
echo "dev certs written to $dir" >&2
