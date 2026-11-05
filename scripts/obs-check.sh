#!/usr/bin/env bash
# Checks the minimal OTel collector config (spec §4.1, §4.6, §8): the merged
# config validates, and the redact processor strips coordinates, PII and DT
# keys from a real span. Needs Docker; runs the pinned collector image.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cfg="$root/observability/collector"
image="$(sed -n 's#^[[:space:]]*image:[[:space:]]*\(otel/[^[:space:]]*\).*#\1#p' "$root/observability/compose.yaml" | head -1)"
[ -n "$image" ] || { echo "obs-check: no collector image in observability/compose.yaml" >&2; exit 1; }

work="$(mktemp -d)"
cname="dt-obs-check-$$"
cleanup() { docker rm -f "$cname" >/dev/null 2>&1 || true; rm -rf "$work"; }
trap cleanup EXIT
chmod 777 "$work" # the collector runs as an unprivileged user

mounts=(-v "$cfg/base.yaml:/etc/otelcol/base.yaml:ro"
        -v "$cfg/redact-keys.yaml:/etc/otelcol/redact-keys.yaml:ro"
        -v "$cfg/exporters.local.yaml:/etc/otelcol/exporters.local.yaml:ro"
        -v "$cfg/exporters.debug.yaml:/etc/otelcol/exporters.debug.yaml:ro")
args=(--config /etc/otelcol/base.yaml --config /etc/otelcol/exporters.local.yaml)

echo "obs-check: validating the merged config"
docker run --rm "${mounts[@]}" --entrypoint /otelcol-contrib "$image" validate "${args[@]}"
docker run --rm "${mounts[@]}" --entrypoint /otelcol-contrib "$image" validate "${args[@]}" \
  --config /etc/otelcol/exporters.debug.yaml

echo "obs-check: redaction test"
# Test-only override: write traces to a file instead of Mission Control.
cat >"$work/file.yaml" <<'YAML'
exporters:
  file/check:
    path: /out/spans.json
service:
  pipelines:
    traces:
      exporters: [file/check]
    logs:
      exporters: [file/check]
YAML
docker run -d --name "$cname" -p 127.0.0.1::4318 "${mounts[@]}" \
  -v "$work/file.yaml:/etc/otelcol/file.yaml:ro" -v "$work:/out" \
  "$image" "${args[@]}" --config /etc/otelcol/file.yaml >/dev/null
port="$(docker port "$cname" 4318/tcp | head -1 | sed 's/.*://')"

span='{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"obs-check"}}]},"scopeSpans":[{"spans":[{"traceId":"5b8efff798038103d269b633813fc60c","spanId":"eee19b7ec3c1b174","name":"obs-check-span","kind":1,"startTimeUnixNano":"1700000000000000000","endTimeUnixNano":"1700000001000000000","attributes":[{"key":"lat","value":{"stringValue":"52.1"}},{"key":"phone","value":{"stringValue":"+31612345678"}},{"key":"note","value":{"stringValue":"dt_sk_test_x"}},{"key":"dt.delivery_id","value":{"stringValue":"keep-me"}}]}]}]}]}'
for _ in $(seq 1 30); do
  if curl -fsS -o /dev/null -X POST -H 'Content-Type: application/json' -d "$span" "http://127.0.0.1:$port/v1/traces"; then sent=1; break; fi
  sleep 1
done
[ "${sent:-}" = 1 ] || { echo "obs-check: collector never accepted the span" >&2; docker logs "$cname" >&2; exit 1; }

for _ in $(seq 1 30); do # the batch processor flushes after its timeout
  [ -s "$work/spans.json" ] && break
  sleep 1
done
[ -s "$work/spans.json" ] || { echo "obs-check: no span reached the file exporter" >&2; docker logs "$cname" >&2; exit 1; }

out="$work/spans.json"
fail=0
grep -q 'obs-check-span' "$out" || { echo "FAIL: span name missing" >&2; fail=1; }
grep -q 'keep-me' "$out" || { echo "FAIL: dt.delivery_id was dropped" >&2; fail=1; }
for needle in '52.1' '+31612345678' 'dt_sk_test_x'; do
  if grep -qF -- "$needle" "$out"; then echo "FAIL: $needle survived redaction" >&2; fail=1; fi
done
for key in '"lat"' '"phone"'; do
  if grep -qF -- "$key" "$out"; then echo "FAIL: attribute $key survived redaction" >&2; fail=1; fi
done
[ "$fail" = 0 ] || exit 1
echo "obs-check: ok"
