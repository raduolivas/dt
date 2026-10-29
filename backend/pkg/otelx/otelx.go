// Package otelx sets up OpenTelemetry tracing for every service: the global
// tracer provider, W3C propagation and an HTTP server wrapper.
package otelx

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strconv"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"github.com/raduolivas/dt/backend/pkg/httpx"
)

// Init installs the global tracer provider and a W3C traceparent + baggage
// propagator. With OTEL_EXPORTER_OTLP_ENDPOINT set, spans are exported over
// OTLP/HTTP in batches; unset, nothing is exported but spans are still
// created so trace ids propagate and show up in logs. Sampling is parent-based
// with the ratio from OTEL_TRACES_SAMPLER_ARG (default 1.0).
func Init(ctx context.Context, service string) (func(context.Context) error, error) {
	res, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceName(service)))
	if err != nil {
		return nil, err
	}
	ratio := 1.0
	if v := os.Getenv("OTEL_TRACES_SAMPLER_ARG"); v != "" {
		if f, perr := strconv.ParseFloat(v, 64); perr == nil && f >= 0 && f <= 1 {
			ratio = f
		}
	}
	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))),
	}
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		exp, err := otlptracehttp.New(ctx)
		if err != nil {
			return nil, err
		}
		opts = append(opts, sdktrace.WithBatcher(exp))
	}
	tp := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return tp.Shutdown, nil
}

// HTTP wraps h (normally the service mux) in a server span per request. The
// span is named after the matched route pattern (e.g. "GET /v1/things/{id}");
// requests that match no route are named by method only, so raw paths (which
// can carry ids) never become span names. otelhttp (>= v0.6x) re-applies the
// formatter after the handler returns, when the mux has set r.Pattern, so the
// formatter alone does the renaming.
//
// Behind a public listener pass PublicEndpoint: the caller is not trusted.
func HTTP(h http.Handler, service string, opts ...Option) http.Handler {
	o := []otelhttp.Option{otelhttp.WithSpanNameFormatter(spanName)}
	for _, opt := range opts {
		o = append(o, opt.o...)
	}
	traced := otelhttp.NewHandler(restoreURL(h), service, o...)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// otelhttp records url.path from the request it is given: hand it
		// one whose credential path segments are redacted, and let the inner
		// handler see the real URL again.
		if red := httpx.RedactPath(r.URL.Path); red != r.URL.Path {
			u := *r.URL
			u.Path, u.RawPath = red, ""
			r2 := r.WithContext(context.WithValue(r.Context(), realURLKey{}, r.URL))
			r2.URL = &u
			r = r2
		}
		traced.ServeHTTP(w, r)
	})
}

type realURLKey struct{}

// restoreURL puts the real URL back on the request in place, so the mux's
// Pattern lands on the request otelhttp holds (the span name reads it).
func restoreURL(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, ok := r.Context().Value(realURLKey{}).(*url.URL); ok {
			r.URL = u
		}
		next.ServeHTTP(w, r)
	})
}

// Option customises HTTP.
type Option struct{ o []otelhttp.Option }

// PublicEndpoint marks the server as reachable by untrusted callers: an
// incoming traceparent no longer becomes the span's parent (a caller could
// otherwise pick the trace id and force sampling); it is kept as a link and
// every request starts a new trace.
func PublicEndpoint() Option {
	return Option{o: []otelhttp.Option{otelhttp.WithPublicEndpointFn(func(*http.Request) bool { return true })}}
}

func spanName(_ string, r *http.Request) string {
	if r.Pattern != "" {
		return r.Pattern
	}
	return r.Method
}
