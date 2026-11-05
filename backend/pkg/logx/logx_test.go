package logx_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/raduolivas/dt/backend/pkg/logx"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func logTo(buf *bytes.Buffer) *slog.Logger { return logx.New(buf, slog.LevelDebug, "test") }

func TestRedactsSensitiveTopLevelKeys(t *testing.T) {
	var buf bytes.Buffer
	logTo(&buf).Info("x", "lat", 52.37, "first_name", "Sam", "api_key", "dt_sk_test_abc", "status", "picked_up")
	out := buf.String()
	for _, leak := range []string{"52.37", "Sam", "dt_sk_test_abc"} {
		if strings.Contains(out, leak) {
			t.Fatalf("leaked %q in %s", leak, out)
		}
	}
	for _, want := range []string{`"status":"picked_up"`, `"service":"test"`, `"lat":"[REDACTED]"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s in %s", want, out)
		}
	}
}

func TestRedactsInsideGroupsAndWith(t *testing.T) {
	var buf bytes.Buffer
	logTo(&buf).With("notes", "ring the bell").Info("x", slog.Group("courier", "lng", 4.9, "vehicle", "e_bike"))
	out := buf.String()
	// Match the JSON value, not the bare number: "4.9" also occurs in timestamps.
	if strings.Contains(out, "ring the bell") || strings.Contains(out, `"lng":4.9`) {
		t.Fatalf("leak: %s", out)
	}
	if !strings.Contains(out, `"courier":{"lng":"[REDACTED]"`) {
		t.Fatalf("lng not redacted: %s", out)
	}
	if !strings.Contains(out, `"vehicle":"e_bike"`) {
		t.Fatalf("over-redacted: %s", out)
	}
}

func TestRedactsWholeSensitiveGroup(t *testing.T) {
	var buf bytes.Buffer
	logTo(&buf).Info("x", slog.Group("position", "x", 1, "y", 2))
	if !strings.Contains(buf.String(), `"position":"[REDACTED]"`) {
		t.Fatalf("got %s", buf.String())
	}
}

func TestKeyMatchingIsCaseInsensitiveAndBySuffix(t *testing.T) {
	var buf bytes.Buffer
	logTo(&buf).Info("x", "Authorization", "Bearer abc", "refresh_token", "r1", "webhook_secret", "w1", "DB_PASSWORD", "p1")
	for _, leak := range []string{"Bearer abc", "r1", "w1", "p1"} {
		if strings.Contains(buf.String(), leak) {
			t.Fatalf("leaked %q", leak)
		}
	}
}

func TestParseLevel(t *testing.T) {
	if l, err := logx.ParseLevel("warn"); err != nil || l != slog.LevelWarn {
		t.Fatalf("got %v %v", l, err)
	}
	if _, err := logx.ParseLevel("loud"); err == nil {
		t.Fatal("expected error")
	}
}

func TestRedactsUnderSensitiveWithGroup(t *testing.T) {
	var buf bytes.Buffer
	logTo(&buf).WithGroup("location").Info("x", "x", 52.1234, "y", 4.5678)
	out := buf.String()
	if strings.Contains(out, "52.1234") || strings.Contains(out, "4.5678") {
		t.Fatalf("leak: %s", out)
	}
}

func TestRedactsCompoundKeyNames(t *testing.T) {
	var buf bytes.Buffer
	logTo(&buf).Info("x",
		"customer_first_name", "v01", "courier_name", "v02", "dropoff_lat", "v03", "pickup_address", "v04",
		"recipient_phone", "v05", "customer_email", "v06", "apikey", "v07", "jwt", "v08", "bearer", "v09",
		"dsn", "v10", "database_url", "v11", "dropoff.lng", "v12", "x-api-key", "v13",
		"delivery_id", "kept")
	out := buf.String()
	for i := 1; i <= 13; i++ {
		if leak := fmt.Sprintf("v%02d", i); strings.Contains(out, leak) {
			t.Fatalf("leaked %s in %s", leak, out)
		}
	}
	if !strings.Contains(out, `"delivery_id":"kept"`) {
		t.Fatalf("over-redacted: %s", out)
	}
}

type coord struct {
	Lat float64
	Lng float64 `json:"lng"`
}

type stop struct {
	Kind     string `json:"kind"`
	Position coord  `json:"position"`
	Contact  map[string]any
}

func TestRedactsInsideStructAndMapValues(t *testing.T) {
	var buf bytes.Buffer
	logTo(&buf).Info("x",
		"dropoff", coord{Lat: 52.3711, Lng: 4.8911},
		"ptr", &coord{Lat: 52.3722, Lng: 4.8922},
		"stops", []stop{{Kind: "pickup", Contact: map[string]any{"first_name": "Sam", "notes": "ring", "floor": 3}}},
		"customer", map[string]any{"first_name": "Alex", "tier": "gold"},
		"err", errors.New("boom"))
	out := buf.String()
	for _, leak := range []string{"52.3711", "4.8911", "52.3722", "4.8922", "Sam", "ring", "Alex"} {
		if strings.Contains(out, leak) {
			t.Fatalf("leaked %q in %s", leak, out)
		}
	}
	for _, want := range []string{`"Lat":"[REDACTED]"`, `"kind":"pickup"`, `"floor":3`, `"tier":"gold"`, `"err":"boom"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s in %s", want, out)
		}
	}
}

func TestLogIncludesTraceID(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()
	ctx, span := tp.Tracer("test").Start(context.Background(), "op")
	defer span.End()

	var buf bytes.Buffer
	logTo(&buf).InfoContext(ctx, "x", slog.Group("g", "a", 1))
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["trace_id"] != span.SpanContext().TraceID().String() {
		t.Fatalf("trace_id = %v in %s", rec["trace_id"], buf.String())
	}
	if rec["span_id"] != span.SpanContext().SpanID().String() {
		t.Fatalf("span_id = %v in %s", rec["span_id"], buf.String())
	}

	buf.Reset()
	logTo(&buf).InfoContext(context.Background(), "x")
	if strings.Contains(buf.String(), "trace_id") || strings.Contains(buf.String(), "span_id") {
		t.Fatalf("unexpected trace keys without span: %s", buf.String())
	}
}

// memExporter keeps exported log records in memory.
type memExporter struct {
	mu   sync.Mutex
	recs []sdklog.Record
}

func (m *memExporter) Export(_ context.Context, rs []sdklog.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range rs {
		m.recs = append(m.recs, r.Clone())
	}
	return nil
}
func (m *memExporter) Shutdown(context.Context) error   { return nil }
func (m *memExporter) ForceFlush(context.Context) error { return nil }

func installMemLogProvider(t *testing.T) *memExporter {
	t.Helper()
	exp := &memExporter{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exp)))
	prev := otel.GetLoggerProvider()
	otel.SetLoggerProvider(lp)
	t.Cleanup(func() { otel.SetLoggerProvider(prev); _ = lp.Shutdown(context.Background()) })
	return exp
}

func TestOTLPRecordIsRedacted(t *testing.T) {
	exp := installMemLogProvider(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://unused.invalid:4318")

	tp := sdktrace.NewTracerProvider()
	ctx, span := tp.Tracer("t").Start(context.Background(), "op")
	defer span.End()

	var buf bytes.Buffer
	logx.New(&buf, slog.LevelDebug, "test").InfoContext(ctx, "x", "lat", 52.1, "api_key", "dt_sk_test_x", "status", "ok")

	if len(exp.recs) != 1 {
		t.Fatalf("exported %d records, want 1", len(exp.recs))
	}
	got := map[string]string{}
	exp.recs[0].WalkAttributes(func(kv attribute.KeyValue) bool {
		got[string(kv.Key)] = kv.Value.String()
		return true
	})
	for _, k := range []string{"lat", "api_key"} {
		if got[k] != logx.Redacted {
			t.Fatalf("%s = %q, want %q (attrs %v)", k, got[k], logx.Redacted, got)
		}
	}
	if got["status"] != "ok" {
		t.Fatalf("status = %q", got["status"])
	}
	if want := span.SpanContext().TraceID().String(); got["trace_id"] != want {
		t.Fatalf("trace_id = %q, want %q", got["trace_id"], want)
	}
	if strings.Contains(buf.String(), "dt_sk_test_x") || !strings.Contains(buf.String(), `"api_key":"[REDACTED]"`) {
		t.Fatalf("stdout not redacted: %s", buf.String())
	}
}

func TestNoEndpointNoTee(t *testing.T) {
	exp := installMemLogProvider(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	var buf bytes.Buffer
	logx.New(&buf, slog.LevelDebug, "test").Info("x", "status", "ok")

	if len(exp.recs) != 0 {
		t.Fatalf("exported %d records without an endpoint", len(exp.recs))
	}
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("stdout not JSON: %v: %s", err, buf.String())
	}
	if m["msg"] != "x" || m["status"] != "ok" || m["service"] != "test" {
		t.Fatalf("unexpected stdout: %s", buf.String())
	}
}

func TestSensitiveKeysSortedAndComplete(t *testing.T) {
	keys := logx.SensitiveKeys()
	if !sort.StringsAreSorted(keys) {
		t.Fatalf("not sorted: %v", keys)
	}
	for _, want := range []string{"lat", "lng", "phone", "token", "api_key"} {
		if !slices.Contains(keys, want) {
			t.Fatalf("missing %q in %v", want, keys)
		}
	}
}
