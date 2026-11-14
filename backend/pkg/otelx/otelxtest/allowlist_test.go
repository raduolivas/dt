package otelxtest

import (
	"fmt"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// fakeTB captures failures instead of failing the real test.
type fakeTB struct {
	testing.TB
	errs []string
}

func (f *fakeTB) Helper() {}
func (f *fakeTB) Errorf(format string, args ...any) {
	f.errs = append(f.errs, sprintf(format, args...))
}
func (f *fakeTB) Fatalf(format string, args ...any) {
	f.errs = append(f.errs, sprintf(format, args...))
}

func sprintf(format string, args ...any) string {
	return strings.TrimSpace(fmt.Sprintf(format, args...))
}

func rmWith(name string, keys ...string) metricdata.ResourceMetrics {
	kvs := make([]attribute.KeyValue, 0, len(keys))
	for _, k := range keys {
		kvs = append(kvs, attribute.String(k, "v"))
	}
	return metricdata.ResourceMetrics{ScopeMetrics: []metricdata.ScopeMetrics{{
		Metrics: []metricdata.Metrics{{
			Name: name,
			Data: metricdata.Sum[int64]{DataPoints: []metricdata.DataPoint[int64]{{
				Attributes: attribute.NewSet(kvs...),
			}}},
		}},
	}}}
}

func TestAssertAllowedRejectsUnknownKey(t *testing.T) {
	f := &fakeTB{TB: t}
	AssertAllowed(f, rmWith("http.server.request.duration", "http.route", "dt.delivery_id"))
	if len(f.errs) == 0 || !strings.Contains(strings.Join(f.errs, "\n"), "dt.delivery_id") {
		t.Fatalf("expected failure naming dt.delivery_id, got %v", f.errs)
	}
}

func TestAssertAllowedRejectsUnknownDTInstrument(t *testing.T) {
	f := &fakeTB{TB: t}
	AssertAllowed(f, rmWith("dt.something.new"))
	if len(f.errs) == 0 {
		t.Fatal("expected failure for unknown dt.* instrument")
	}
}

func TestAssertAllowedIgnoresForeignInstruments(t *testing.T) {
	f := &fakeTB{TB: t}
	AssertAllowed(f, rmWith("process.runtime.go.mem.heap_alloc", "anything"))
	if len(f.errs) != 0 {
		t.Fatalf("unexpected failures: %v", f.errs)
	}
}

func TestAssertAllowedAcceptsCatalogue(t *testing.T) {
	f := &fakeTB{TB: t}
	for name, keys := range Allowed {
		AssertAllowed(f, rmWith(name, keys...))
	}
	if len(f.errs) != 0 {
		t.Fatalf("unexpected failures: %v", f.errs)
	}
}
