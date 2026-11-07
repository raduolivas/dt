package otelx

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"

	"connectrpc.com/connect/v2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const connectTracer = "github.com/raduolivas/dt/backend/pkg/otelx/connect"

// ConnectClientInterceptor carries trace context across internal Connect
// calls. It injects W3C trace context into the request headers with the
// global propagator. A unary call also gets a client span named after the
// procedure with rpc.system, rpc.service and rpc.method, ended when the
// response arrives (or the stream closes); a failed call sets the span status
// to Error and rpc.connect_rpc.error_code to the connect code. Streaming
// calls get injection only, no span. There is no server counterpart: servers
// behind [HTTP] extract the context there.
func ConnectClientInterceptor() connect.ClientInterceptor {
	return func(next connect.ClientFunc) connect.ClientFunc {
		return func(ctx context.Context, spec connect.Spec) (connect.ClientStream, error) {
			var span trace.Span
			if spec.StreamType == connect.StreamTypeUnary {
				service, method := splitProcedure(spec.Procedure)
				ctx, span = otel.Tracer(connectTracer).Start(ctx, spec.Procedure,
					trace.WithSpanKind(trace.SpanKindClient),
					trace.WithAttributes(
						attribute.String("rpc.system", "connect_rpc"),
						attribute.String("rpc.service", service),
						attribute.String("rpc.method", method),
					))
			}
			if info, ok := connect.CallInfoForClientContext(ctx); ok {
				otel.GetTextMapPropagator().Inject(ctx, headerCarrier{info.RequestHeader()})
			}
			stream, err := next(ctx, spec)
			if span == nil {
				return stream, err
			}
			if err != nil {
				endSpan(span, err)
				return nil, err
			}
			return &tracedStream{ClientStream: stream, span: span}, nil
		}
	}
}

// headerCarrier adapts connect request headers to a propagation carrier.
type headerCarrier struct{ h *connect.Header }

func (c headerCarrier) Get(key string) string { return c.h.Get(key) }
func (c headerCarrier) Set(key, value string) { c.h.Set(key, value) }
func (c headerCarrier) Keys() []string {
	var keys []string
	for k := range c.h.All() {
		keys = append(keys, k)
	}
	return keys
}

var _ propagation.TextMapCarrier = headerCarrier{}

// splitProcedure splits "/pkg.Service/Method" into service and method.
func splitProcedure(p string) (service, method string) {
	service, method, _ = strings.Cut(strings.TrimPrefix(p, "/"), "/")
	return service, method
}

func endSpan(span trace.Span, err error) {
	if err != nil && !errors.Is(err, io.EOF) {
		code := connect.CodeOf(err)
		span.SetAttributes(attribute.String("rpc.connect_rpc.error_code", code.String()))
		span.SetStatus(codes.Error, code.String())
	}
	span.End()
}

// tracedStream ends the unary call's span once: at the first Receive result
// (the response, an error or EOF) or at Close, whichever comes first.
type tracedStream struct {
	connect.ClientStream
	span trace.Span
	once sync.Once
}

func (s *tracedStream) finish(err error) { s.once.Do(func() { endSpan(s.span, err) }) }

func (s *tracedStream) Receive(msg any) error {
	err := s.ClientStream.Receive(msg)
	s.finish(err)
	return err
}

func (s *tracedStream) Close() error {
	err := s.ClientStream.Close()
	s.finish(nil)
	return err
}
