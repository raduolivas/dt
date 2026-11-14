#!/usr/bin/env bash
# Checks the OBS configs (spec §4, §6, §7): the merged collector config
# validates, the redact processors strip coordinates, PII and DT keys from a
# real span, log record and metric datapoint, and the Prometheus, Tempo and
# Loki configs validate. Needs Docker; runs the pinned images.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cfg="$root/observability/collector"
image="$(sed -n 's#^[[:space:]]*image:[[:space:]]*\(otel/[^[:space:]]*\).*#\1#p' "$root/observability/compose.yaml" | head -1)"
img() { sed -n "s#^[[:space:]]*image:[[:space:]]*\\($1[^[:space:]]*\\).*#\\1#p" "$root/observability/compose.yaml" | head -1; }
prom_image="$(img prom/)"; tempo_image="$(img grafana/tempo)"; loki_image="$(img grafana/loki)"
[ -n "$image" ] || { echo "obs-check: no collector image in observability/compose.yaml" >&2; exit 1; }

for v in prom_image tempo_image loki_image; do
  [ -n "${!v}" ] || { echo "obs-check: no $v in observability/compose.yaml" >&2; exit 1; }
done

work="$(mktemp -d)"
cname="dt-obs-check-$$"
cleanup() { docker rm -f "$cname" >/dev/null 2>&1 || true; rm -rf "$work"; }
trap cleanup EXIT
chmod 777 "$work" # the collector runs as an unprivileged user
# The httpcheck receiver loads the dev mTLS client certificate at start, so the
# collector needs one: write a throwaway set (dev-certs.sh world-readable keys).
"$root/scripts/dev-certs.sh" "$work/certs"

mounts=(-v "$cfg/base.yaml:/etc/otelcol/base.yaml:ro"
        -v "$cfg/redact-keys.yaml:/etc/otelcol/redact-keys.yaml:ro"
        -v "$cfg/exporters.local.yaml:/etc/otelcol/exporters.local.yaml:ro"
        -v "$cfg/exporters.debug.yaml:/etc/otelcol/exporters.debug.yaml:ro"
        -v "$work/certs:/certs:ro")
args=(--config /etc/otelcol/base.yaml --config /etc/otelcol/exporters.local.yaml)

echo "obs-check: validating the merged config"
docker run --rm "${mounts[@]}" --entrypoint /otelcol-contrib "$image" validate "${args[@]}"
docker run --rm "${mounts[@]}" --entrypoint /otelcol-contrib "$image" validate "${args[@]}" \
  --config /etc/otelcol/exporters.debug.yaml

echo "obs-check: validating the Prometheus, Tempo and Loki configs, and the Prometheus rules"
obs="$root/observability"
docker run --rm --entrypoint promtool \
  -v "$obs/prometheus/prometheus.yaml:/etc/prometheus/prometheus.yaml:ro" \
  -v "$obs/prometheus/rules:/etc/prometheus/rules:ro" \
  "$prom_image" check config /etc/prometheus/prometheus.yaml
# The rule and test files are separate mounts; `rule_files` names only the rule
# files, so the *_test.yaml fixtures never reach Prometheus.
docker run --rm --entrypoint promtool \
  -v "$obs/prometheus/rules:/etc/prometheus/rules:ro" \
  "$prom_image" check rules /etc/prometheus/rules/slo.yaml /etc/prometheus/rules/mc-wires.yaml
docker run --rm --entrypoint promtool \
  -v "$obs/prometheus/rules:/etc/prometheus/rules:ro" \
  "$prom_image" test rules /etc/prometheus/rules/slo_test.yaml /etc/prometheus/rules/mc-wires_test.yaml
docker run --rm --entrypoint /tempo -v "$obs/tempo/tempo.yaml:/etc/tempo/tempo.yaml:ro" \
  "$tempo_image" -config.file=/etc/tempo/tempo.yaml -config.verify=true
docker run --rm --entrypoint /usr/bin/loki -v "$obs/loki/loki.yaml:/etc/loki/loki.yaml:ro" \
  "$loki_image" -config.file=/etc/loki/loki.yaml -verify-config

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
    metrics:
      exporters: [file/check]
YAML
docker run -d --name "$cname" -p 127.0.0.1::4318 "${mounts[@]}" \
  -v "$work/file.yaml:/etc/otelcol/file.yaml:ro" -v "$work:/out" \
  "$image" "${args[@]}" --config /etc/otelcol/file.yaml >/dev/null
port="$(docker port "$cname" 4318/tcp | head -1 | sed 's/.*://')"

span='{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"obs-check"}}]},"scopeSpans":[{"spans":[{"traceId":"5b8efff798038103d269b633813fc60c","spanId":"eee19b7ec3c1b174","name":"obs-check-span","kind":1,"startTimeUnixNano":"1700000000000000000","endTimeUnixNano":"1700000001000000000","attributes":[{"key":"lat","value":{"stringValue":"52.1"}},{"key":"phone","value":{"stringValue":"+31612345678"}},{"key":"note","value":{"stringValue":"dt_sk_test_x"}},{"key":"dropoff_lat","value":{"stringValue":"seg-lat-77"}},{"key":"x-api-key","value":{"stringValue":"seg-apikey-88"}},{"key":"dt.delivery_id","value":{"stringValue":"keep-me"}},{"key":"messaging.destination.name","value":{"stringValue":"keep-dest"}},{"key":"server.address","value":{"stringValue":"keep-server"}},{"key":"http.request.header.authorization","value":{"stringValue":"f1-auth-55"}},{"key":"url.full","value":{"stringValue":"f1-urlfull-66"}},{"key":"url.query","value":{"stringValue":"f1-urlquery-77"}},{"key":"http.request.header.cookie","value":{"stringValue":"f1-cookie-88"}},{"key":"dt.tok","value":{"stringValue":"dt_lnk_span-secret-91"}},{"key":"dt.jwt1","value":{"stringValue":"eyJhbGciOiJIUzI1NiJ9.span-secret-92.y"}}],"events":[{"name":"obs-check-event","timeUnixNano":"1700000000500000000","attributes":[{"key":"customer_phone","value":{"stringValue":"event-phone-99"}},{"key":"dt.event_id","value":{"stringValue":"keep-event"}}]}]}]}]}]}'
for _ in $(seq 1 30); do
  if curl -fsS -o /dev/null -X POST -H 'Content-Type: application/json' -d "$span" "http://127.0.0.1:$port/v1/traces"; then sent=1; break; fi
  sleep 1
done
[ "${sent:-}" = 1 ] || { echo "obs-check: collector never accepted the span" >&2; docker logs "$cname" >&2; exit 1; }

# Same checks for a log record: one exact key, one segment-matched key.
logrec='{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"obs-check"}}]},"scopeLogs":[{"logRecords":[{"timeUnixNano":"1700000000000000000","severityText":"INFO","body":{"stringValue":"obs-check-log"},"attributes":[{"key":"email","value":{"stringValue":"log-email-11"}},{"key":"dropoff_lng","value":{"stringValue":"log-lng-22"}},{"key":"dt.request_id","value":{"stringValue":"keep-log"}}]}]}]}]}'
curl -fsS -o /dev/null -X POST -H 'Content-Type: application/json' -d "$logrec" "http://127.0.0.1:$port/v1/logs" \
  || { echo "obs-check: collector rejected the log record" >&2; exit 1; }

# Same checks for a metric datapoint: coordinates deleted, DT key masked, F1 keys deleted.
metric='{"resourceMetrics":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"obs-check"}}]},"scopeMetrics":[{"metrics":[{"name":"obs_check_metric","gauge":{"dataPoints":[{"timeUnixNano":"1700000000000000000","asDouble":1,"attributes":[{"key":"lat","value":{"stringValue":"metric-lat-33"}},{"key":"metric_note","value":{"stringValue":"dt_sk_test_x"}},{"key":"url.query","value":{"stringValue":"metric-urlquery-44"}},{"key":"db.query.parameters","value":{"stringValue":"metric-dbparams-45"}},{"key":"dt.tok","value":{"stringValue":"dt_lnk_metric-secret-93"}},{"key":"dt.jwt1","value":{"stringValue":"eyJhbGciOiJIUzI1NiJ9.metric-secret-94.y"}},{"key":"dt.keep_metric","value":{"stringValue":"keep-metric"}},{"key":"messaging.destination.name","value":{"stringValue":"keep-metric-dest"}}]}]}}]}]}]}'
curl -fsS -o /dev/null -X POST -H 'Content-Type: application/json' -d "$metric" "http://127.0.0.1:$port/v1/metrics" \
  || { echo "obs-check: collector rejected the metric" >&2; exit 1; }

for _ in $(seq 1 30); do # the batch processor flushes after its timeout
  grep -q 'obs-check-span' "$work/spans.json" 2>/dev/null && grep -q 'obs-check-log' "$work/spans.json" 2>/dev/null && grep -q 'obs_check_metric' "$work/spans.json" 2>/dev/null && break
  sleep 1
done
[ -s "$work/spans.json" ] || { echo "obs-check: no span reached the file exporter" >&2; docker logs "$cname" >&2; exit 1; }

out="$work/spans.json"
fail=0
grep -q 'obs-check-span' "$out" || { echo "FAIL: span name missing" >&2; fail=1; }
grep -q 'obs-check-event' "$out" || { echo "FAIL: span event missing" >&2; fail=1; }
grep -q 'obs-check-log' "$out" || { echo "FAIL: log record missing" >&2; fail=1; }
grep -q 'keep-event' "$out" || { echo "FAIL: dt.event_id on the span event was dropped" >&2; fail=1; }
grep -q 'obs_check_metric' "$out" || { echo "FAIL: metric missing" >&2; fail=1; }
for keep in keep-metric keep-metric-dest; do
  grep -qF -- "$keep" "$out" || { echo "FAIL: metric attribute value $keep was dropped" >&2; fail=1; }
done
# Masks must replace the value (keep the key), not delete the attribute.
for key in '"dt.tok"' '"dt.jwt1"'; do
  grep -qF -- "$key" "$out" || { echo "FAIL: masked attribute $key was deleted, not masked" >&2; fail=1; }
done
[ "$(grep -o '\[REDACTED\]' "$out" | wc -l)" -ge 6 ] || { echo "FAIL: expected [REDACTED] for dt_sk_, dt_lnk_ and eyJ values on span and metric" >&2; fail=1; }
grep -q 'keep-log' "$out" || { echo "FAIL: dt.request_id on the log was dropped" >&2; fail=1; }
grep -q 'keep-me' "$out" || { echo "FAIL: dt.delivery_id was dropped" >&2; fail=1; }
# Standard OTel namespaces must survive the segment rule (name/address are sensitive segments).
for keep in messaging.destination.name keep-dest server.address keep-server; do
  grep -qF -- "$keep" "$out" || { echo "FAIL: standard attribute $keep was stripped" >&2; fail=1; }
done
for needle in '52.1' '+31612345678' 'dt_sk_test_x' seg-lat-77 seg-apikey-88 event-phone-99 log-email-11 log-lng-22 metric-lat-33 metric-urlquery-44 metric-dbparams-45 f1-auth-55 f1-urlfull-66 f1-urlquery-77 f1-cookie-88 span-secret-91 span-secret-92 metric-secret-93 metric-secret-94; do
  if grep -qF -- "$needle" "$out"; then echo "FAIL: $needle survived redaction" >&2; fail=1; fi
done
for key in '"lat"' '"phone"' '"dropoff_lat"' '"x-api-key"' '"customer_phone"' '"email"' '"dropoff_lng"' '"http.request.header.authorization"' '"url.full"' '"url.query"' '"db.query.parameters"' '"http.request.header.cookie"'; do
  if grep -qF -- "$key" "$out"; then echo "FAIL: attribute $key survived redaction" >&2; fail=1; fi
done
[ "$fail" = 0 ] || exit 1
echo "obs-check: ok"
