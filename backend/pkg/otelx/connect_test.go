package otelx_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	accountsv1 "github.com/raduolivas/dt/backend/gen/dt/accounts/v1"
	"github.com/raduolivas/dt/backend/gen/dt/accounts/v1/accountsv1connect"
	"github.com/raduolivas/dt/backend/pkg/otelx"
)

const procedure = "/dt.accounts.v1.AccountsService/CreateTenant"

type connectStub struct {
	accountsv1connect.UnimplementedAccountsServiceHandler
	err error
}

func (s connectStub) CreateTenant(context.Context, *accountsv1.CreateTenantRequest) (*accountsv1.CreateTenantResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &accountsv1.CreateTenantResponse{}, nil
}

// callTraced installs a recording provider, serves the stub behind
// otelx.HTTP(.., opts) and calls it through the client interceptor. It
// returns the call error and the ended client and server spans.
func callTraced(t *testing.T, stubErr error, opts ...otelx.Option) (client, server sdktrace.ReadOnlySpan, err error) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
		_ = tp.Shutdown(context.Background())
	})

	srv := connect.NewServer()
	accountsv1connect.RegisterAccountsServiceHandler(srv, connectStub{err: stubErr})
	mux := http.NewServeMux()
	connecthttp.Mount(mux, srv)
	ts := httptest.NewServer(otelx.HTTP(mux, "svc", opts...))
	t.Cleanup(ts.Close)

	c := accountsv1connect.NewAccountsServiceClient(
		connect.NewClient(connecthttp.NewTransport(ts.Client(), ts.URL), otelx.ConnectClientInterceptor()))
	_, err = c.CreateTenant(context.Background(), &accountsv1.CreateTenantRequest{Name: "Acme"})

	for _, s := range rec.Ended() {
		switch {
		case s.SpanKind() == trace.SpanKindClient && s.Name() == procedure:
			client = s
		case s.SpanKind() == trace.SpanKindServer:
			server = s
		}
	}
	if client == nil || server == nil {
		t.Fatalf("missing spans: client=%v server=%v (ended=%d)", client, server, len(rec.Ended()))
	}
	return client, server, err
}

func TestClientSpanIsParentOfServerSpan(t *testing.T) {
	client, server, err := callTraced(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if client.SpanKind() != trace.SpanKindClient {
		t.Errorf("client span kind = %v", client.SpanKind())
	}
	if server.Parent().SpanID() != client.SpanContext().SpanID() {
		t.Errorf("server parent = %v, want client span %v", server.Parent().SpanID(), client.SpanContext().SpanID())
	}
	if server.SpanContext().TraceID() != client.SpanContext().TraceID() {
		t.Errorf("trace ids differ: server %v client %v", server.SpanContext().TraceID(), client.SpanContext().TraceID())
	}
	want := map[attribute.Key]string{
		"rpc.system":  "connect_rpc",
		"rpc.service": "dt.accounts.v1.AccountsService",
		"rpc.method":  "CreateTenant",
	}
	got := map[attribute.Key]string{}
	for _, kv := range client.Attributes() {
		got[kv.Key] = kv.Value.String()
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("attribute %s = %q, want %q", k, got[k], v)
		}
	}
	if client.Status().Code == codes.Error {
		t.Errorf("client status = %v on success", client.Status())
	}
}

func TestPublicEndpointStillStartsNewTrace(t *testing.T) {
	client, server, err := callTraced(t, nil, otelx.PublicEndpoint())
	if err != nil {
		t.Fatal(err)
	}
	if server.SpanContext().TraceID() == client.SpanContext().TraceID() {
		t.Error("public endpoint continued the caller's trace")
	}
	links := server.Links()
	if len(links) != 1 || links[0].SpanContext.SpanID() != client.SpanContext().SpanID() {
		t.Errorf("server links = %v, want one link to the client span", links)
	}
}

func TestClientSpanErrorStatus(t *testing.T) {
	client, _, err := callTraced(t, connect.NewError(connect.CodeNotFound, "no such tenant"))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("err = %v, want not_found", err)
	}
	if client.Status().Code != codes.Error {
		t.Errorf("client status = %v, want Error", client.Status())
	}
	var code string
	for _, kv := range client.Attributes() {
		if kv.Key == "rpc.connect_rpc.error_code" {
			code = kv.Value.String()
		}
	}
	if code != "not_found" {
		t.Errorf("rpc.connect_rpc.error_code = %q, want not_found", code)
	}
}
