// Package otelxtest holds test helpers for metric tests: a label allow-list
// that makes a new or unexpected metric label fail CI (spec §5.2).
package otelxtest

import (
	"slices"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// httpServerKeys are the otelhttp-standard attributes plus the spec's three.
// server.address must be the configured host, never the client's.
var httpServerKeys = []string{
	"http.route", "http.request.method", "http.response.status_code",
	"network.protocol.name", "network.protocol.version",
	"url.scheme", "server.address", "server.port",
}

var rpcKeys = []string{"rpc.system", "rpc.service", "rpc.method", "rpc.connect_rpc.error_code"}

// Allowed maps instrument name to the attribute keys it may carry. It is the
// spec §5.2 table using the Prometheus-independent OTel names.
var Allowed = map[string][]string{
	"http.server.request.duration": httpServerKeys,
	"rpc.server.duration":          rpcKeys,
	"rpc.client.duration":          rpcKeys,

	"dt_consumer_pending_messages":       {"stream", "consumer"},
	"dt.consumer.process.duration":       {"stream", "consumer", "outcome"},
	"dt.consumer.messages":               {"stream", "consumer", "outcome"},
	"dt.outbox.pending":                  {},
	"dt.outbox.publish.duration":         {},
	"dt.tracking.fix_to_viewer.duration": {"source"},
	"dt.tracking.fixes":                  {"source", "outcome"},
	"dt.tracking.viewer.connections":     {"transport"},
	"dt.routing.duration":                {"operation", "outcome"},
	"dt.routing.errors":                  {"operation", "outcome"},
	"dt.webhook.deliveries":              {"tenant_id", "outcome"},
	"dt.webhook.first_attempt.delay":     {"tenant_id", "outcome"},
	"dt.courier.sessions.started":        {"mode"},
	"dt.courier.refresh":                 {"outcome"},
	"dt.ratelimit.rejections":            {"surface"},
}

var checkedPrefixes = []string{"dt.", "dt_", "http.", "rpc."}

// AssertAllowed fails t for any instrument under the dt./dt_/http./rpc.
// prefixes that is not in Allowed, or any datapoint attribute key outside the
// instrument's list. Other instruments (SDK and runtime metrics) are ignored.
func AssertAllowed(t testing.TB, rm metricdata.ResourceMetrics) {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if !hasCheckedPrefix(m.Name) {
				continue
			}
			keys, ok := Allowed[m.Name]
			if !ok {
				t.Errorf("metric %q is not in the allow-list (spec §5.2)", m.Name)
				continue
			}
			for _, set := range attributeSets(m.Data) {
				for _, kv := range set.ToSlice() {
					if !slices.Contains(keys, string(kv.Key)) {
						t.Errorf("metric %q has disallowed label %q (allowed: %v)", m.Name, kv.Key, keys)
					}
				}
			}
		}
	}
}

func hasCheckedPrefix(name string) bool {
	for _, p := range checkedPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func attributeSets(data metricdata.Aggregation) []attribute.Set {
	var out []attribute.Set
	switch d := data.(type) {
	case metricdata.Sum[int64]:
		for _, p := range d.DataPoints {
			out = append(out, p.Attributes)
		}
	case metricdata.Sum[float64]:
		for _, p := range d.DataPoints {
			out = append(out, p.Attributes)
		}
	case metricdata.Gauge[int64]:
		for _, p := range d.DataPoints {
			out = append(out, p.Attributes)
		}
	case metricdata.Gauge[float64]:
		for _, p := range d.DataPoints {
			out = append(out, p.Attributes)
		}
	case metricdata.Histogram[int64]:
		for _, p := range d.DataPoints {
			out = append(out, p.Attributes)
		}
	case metricdata.Histogram[float64]:
		for _, p := range d.DataPoints {
			out = append(out, p.Attributes)
		}
	}
	return out
}
