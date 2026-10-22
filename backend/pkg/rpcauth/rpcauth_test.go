package rpcauth_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	accountsv1 "github.com/raduolivas/dt/backend/gen/dt/accounts/v1"
	"github.com/raduolivas/dt/backend/gen/dt/accounts/v1/accountsv1connect"
	"github.com/raduolivas/dt/backend/pkg/rpcauth"
)

const tok = "test-only-internal-rpc-token-0123456789"

// stub serves CreateTenant through fn; any generated service would do.
type stub struct {
	accountsv1connect.UnimplementedAccountsServiceHandler
	fn func(ctx context.Context) error
}

func (s stub) CreateTenant(ctx context.Context, _ *accountsv1.CreateTenantRequest) (*accountsv1.CreateTenantResponse, error) {
	if err := s.fn(ctx); err != nil {
		return nil, err
	}
	return &accountsv1.CreateTenantResponse{}, nil
}

func testServer(t *testing.T, fn func(ctx context.Context) error) *httptest.Server {
	t.Helper()
	si, err := rpcauth.NewServerInterceptor(tok)
	if err != nil {
		t.Fatal(err)
	}
	srv := connect.NewServer(si)
	accountsv1connect.RegisterAccountsServiceHandler(srv, stub{fn: fn})
	mux := http.NewServeMux()
	connecthttp.Mount(mux, srv)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func testClient(t *testing.T, ts *httptest.Server, token string) accountsv1connect.AccountsServiceClient {
	t.Helper()
	ci, err := rpcauth.NewClientInterceptor(token)
	if err != nil {
		t.Fatal(err)
	}
	return accountsv1connect.NewAccountsServiceClient(
		connect.NewClient(connecthttp.NewTransport(ts.Client(), ts.URL), ci))
}

func TestClientInterceptorSendsBearer(t *testing.T) {
	t.Parallel()
	ts := testServer(t, func(context.Context) error { return nil })
	req := &accountsv1.CreateTenantRequest{Name: "Acme"}

	if _, err := testClient(t, ts, tok).CreateTenant(context.Background(), req); err != nil {
		t.Fatalf("right token: %v", err)
	}
	_, err := testClient(t, ts, strings.Repeat("x", len(tok))).CreateTenant(context.Background(), req)
	if got := connect.CodeOf(err); got != connect.CodeUnauthenticated {
		t.Fatalf("wrong token: code = %v (%v), want unauthenticated", got, err)
	}
}

func TestNewInterceptorsRejectShortToken(t *testing.T) {
	t.Parallel()
	short := strings.Repeat("x", rpcauth.MinTokenLen-1)
	if _, err := rpcauth.NewServerInterceptor(short); err == nil {
		t.Error("NewServerInterceptor accepted a 31-byte token")
	}
	if _, err := rpcauth.NewClientInterceptor(short); err == nil {
		t.Error("NewClientInterceptor accepted a 31-byte token")
	}
}

func TestCodeRoundTrip(t *testing.T) {
	t.Parallel()
	ts := testServer(t, func(ctx context.Context) error {
		rpcauth.SetCode(ctx, "invalid_transition")
		return connect.NewError(connect.CodeFailedPrecondition, "nope")
	})
	ctx, info := connect.NewClientContext(context.Background())
	_, err := testClient(t, ts, tok).CreateTenant(ctx, &accountsv1.CreateTenantRequest{Name: "Acme"})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("err = %v", err)
	}
	if got := rpcauth.CodeFrom(info); got != "invalid_transition" {
		t.Fatalf("CodeFrom = %q, want invalid_transition", got)
	}
}

// TestTokenCompareIsConstantTime pins the token check to
// subtle.ConstantTimeCompare, which a behavioural test cannot observe.
func TestTokenCompareIsConstantTime(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "rpcauth.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "subtle" && sel.Sel.Name == "ConstantTimeCompare" {
				found = true
			}
		}
		return true
	})
	if !found {
		t.Fatal("rpcauth.go does not use subtle.ConstantTimeCompare")
	}
}
