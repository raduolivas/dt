package otelx_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/raduolivas/dt/backend/pkg/otelx"
)

func TestHTTPSpanNameUsesPattern(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev); _ = tp.Shutdown(context.Background()) })

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/things/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := otelx.HTTP(mux, "test")

	for _, path := range []string{"/v1/things/abc", "/secret/raw/path"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	spans := rec.Ended()
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2", len(spans))
	}
	if got := spans[0].Name(); got != "GET /v1/things/{id}" {
		t.Fatalf("matched span name = %q", got)
	}
	if got := spans[1].Name(); got != "GET" {
		t.Fatalf("unmatched span name = %q, want bare method", got)
	}
}

func TestInitWithoutEndpointIsNoopExport(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	shutdown, err := otelx.Init(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestPublicEndpointStartsNewTrace(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
		otel.SetTextMapPropagator(prevProp)
		_ = tp.Shutdown(context.Background())
	})

	const traceID, spanID = "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"
	run := func(h http.Handler) sdktrace.ReadOnlySpan {
		rec.Reset()
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("traceparent", "00-"+traceID+"-"+spanID+"-01")
		h.ServeHTTP(httptest.NewRecorder(), req)
		spans := rec.Ended()
		if len(spans) != 1 {
			t.Fatalf("got %d spans, want 1", len(spans))
		}
		return spans[0]
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /x", func(http.ResponseWriter, *http.Request) {})

	// Control: an internal endpoint continues the caller's trace.
	if got := run(otelx.HTTP(mux, "test")); got.SpanContext().TraceID().String() != traceID {
		t.Fatalf("internal endpoint trace id = %s, want the caller's", got.SpanContext().TraceID())
	}

	span := run(otelx.HTTP(mux, "test", otelx.PublicEndpoint()))
	if span.SpanContext().TraceID().String() == traceID {
		t.Fatal("public endpoint continued the caller's trace")
	}
	if span.Parent().IsValid() {
		t.Fatalf("public endpoint span has a parent: %v", span.Parent())
	}
	links := span.Links()
	if len(links) != 1 || links[0].SpanContext.TraceID().String() != traceID {
		t.Fatalf("links = %v, want one link to the caller's trace", links)
	}
}

func TestHTTPSpansNeverCarryCredentialPaths(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev); _ = tp.Shutdown(context.Background()) })

	var seen, pattern string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /t/{token}", func(_ http.ResponseWriter, r *http.Request) { seen, pattern = r.URL.Path, r.Pattern })
	h := otelx.HTTP(mux, "test")
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(m, "/t/secretsecret?x=1", nil))
	}

	if seen != "/t/secretsecret" || pattern != "GET /t/{token}" {
		t.Fatalf("inner handler saw path %q pattern %q", seen, pattern)
	}
	spans := rec.Ended()
	if len(spans) != 2 {
		t.Fatalf("got %d spans", len(spans))
	}
	for _, s := range spans {
		if strings.Contains(s.Name(), "secretsecret") {
			t.Fatalf("span name %q", s.Name())
		}
		for _, kv := range s.Attributes() {
			if strings.Contains(kv.Value.String(), "secretsecret") {
				t.Fatalf("attribute %s = %s", kv.Key, kv.Value.String())
			}
		}
	}
	if spans[0].Name() != "GET /t/{token}" {
		t.Fatalf("span name = %q", spans[0].Name())
	}
}
