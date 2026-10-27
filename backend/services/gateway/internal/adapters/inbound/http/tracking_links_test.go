package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

	"github.com/raduolivas/dt/backend/pkg/apikey"
	"github.com/raduolivas/dt/backend/pkg/logx"
	httpapi "github.com/raduolivas/dt/backend/services/gateway/internal/adapters/inbound/http"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app/apptest"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

const trackingBase = "https://track.example.invalid"

type linkGW struct {
	*gw
	links *app.TrackingLinks
	store *apptest.MemLinks
	rev   *apptest.MemRevoker
}

// newLinkGW is newGW with tracking links over an in-memory store; extra
// configures the middleware (e.g. a real idempotency store).
func newLinkGW(t *testing.T, extra func(*httpapi.Middleware)) *linkGW {
	t.Helper()
	k := mint(t, apikey.Test, actor, domain.AllScopes...)
	f := newFake()
	st, rev := apptest.NewMemLinks(), &apptest.MemRevoker{}
	links := &app.TrackingLinks{Store: st, Revoker: rev, Clock: fixedClock{}}
	au := &memAudit{}
	rpcLogs := &bytes.Buffer{}
	e := newEdge(t, []testKey{k},
		withRoutes(httpapi.Routes(httpapi.Deps{Deliveries: f, Links: links, TrackingURLBase: trackingBase,
			Log: logx.New(rpcLogs, slog.LevelDebug, "gateway")})...),
		withMiddleware(func(m *httpapi.Middleware) {
			m.Audit = httpapi.Audit(au, fixedClock{}, m.Log)
			if extra != nil {
				extra(m)
			}
		}))
	return &linkGW{gw: &gw{edge: e, fake: f, key: k, audit: au, rpc: rpcLogs}, links: links, store: st, rev: rev}
}

type trackingJSON struct {
	URL       string     `json:"url"`
	Token     string     `json:"token"`
	ExpiresAt *time.Time `json:"expires_at"`
}

func TestCreateDeliveryReturnsTracking(t *testing.T) {
	t.Parallel()
	g := newLinkGW(t, nil)
	rec := g.do(t, g.req("POST", "/v1/deliveries", create), http.StatusCreated)
	var body struct {
		ID       string        `json:"id"`
		Tracking *trackingJSON `json:"tracking"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	tr := body.Tracking
	if tr == nil || !domain.ValidLinkToken(tr.Token) || tr.URL != trackingBase+"/t/"+tr.Token || tr.ExpiresAt != nil {
		t.Fatalf("tracking = %+v", tr)
	}
	l, err := g.links.Resolve(context.Background(), tr.Token, fixedClock{}.Now())
	if err != nil || l.DeliveryID != delID || l.TenantID != tenantID || l.Env != domain.EnvTest {
		t.Fatalf("issued link = %+v, %v", l, err)
	}
	if strings.Contains(g.logs.String(), tr.Token) || strings.Contains(g.rpc.String(), tr.Token) {
		t.Fatal("token logged")
	}
}

func TestCreateDeliveryLinkFailureStill201(t *testing.T) {
	t.Parallel()
	g := newLinkGW(t, nil)
	g.store.Fail(errors.New("db down"))
	rec := g.do(t, g.req("POST", "/v1/deliveries", create), http.StatusCreated)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if v, ok := body["tracking"]; !ok || v != nil {
		t.Fatalf("tracking = %v (present %t), want null", v, ok)
	}
	if body["id"] != delID || g.fake.count() != 1 {
		t.Fatalf("body %v, deliveries calls %d", body, g.fake.count())
	}
	if !strings.Contains(g.rpc.String(), "tracking_link_issue_failed") || strings.Contains(g.rpc.String(), "Ana") {
		t.Fatalf("logs = %s", g.rpc.String())
	}
}

func TestRotateIssuesWhenNoLink(t *testing.T) {
	t.Parallel()
	g := newLinkGW(t, nil)
	// No link yet (issuing failed at creation): rotate issues one.
	rec := g.do(t, g.req("POST", "/v1/deliveries/"+delID+"/tracking-link/rotate", ""), http.StatusOK)
	var tr trackingJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &tr); err != nil {
		t.Fatal(err)
	}
	if !domain.ValidLinkToken(tr.Token) || tr.URL != trackingBase+"/t/"+tr.Token {
		t.Fatalf("link = %+v", tr)
	}
	if len(g.rev.Revoked()) != 0 {
		t.Fatal("revoked a link that did not exist")
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}

	// Rotating again kills the first token and publishes the revocation.
	rec = g.do(t, g.req("POST", "/v1/deliveries/"+delID+"/tracking-link/rotate", ""), http.StatusOK)
	var tr2 trackingJSON
	_ = json.Unmarshal(rec.Body.Bytes(), &tr2)
	now := fixedClock{}.Now()
	if _, err := g.links.Resolve(context.Background(), tr.Token, now); !errors.Is(err, app.ErrLinkInvalid) {
		t.Fatalf("old token: %v", err)
	}
	if _, err := g.links.Resolve(context.Background(), tr2.Token, now); err != nil {
		t.Fatalf("new token: %v", err)
	}
	if len(g.rev.Revoked()) != 1 {
		t.Fatalf("revoked = %v", g.rev.Revoked())
	}

	// Another tenant's delivery (deliveries says not found): nothing issued.
	g.fake.fail = rpcFail(connect.CodeNotFound, "not_found")
	wantProblem(t, g.edge.do(g.req("POST", "/v1/deliveries/"+delID+"/tracking-link/rotate", "")), http.StatusNotFound, "not_found")
	g.fake.fail = nil

	// A revocation that cannot be published fails the rotation (5xx, so the
	// idempotent retry rotates again).
	g.rev.Fail(errors.New("redis down"))
	wantProblem(t, g.edge.do(g.req("POST", "/v1/deliveries/"+delID+"/tracking-link/rotate", "")),
		http.StatusServiceUnavailable, "tracking_unavailable")
}

func TestIdempotentCreateReplaysSameTracking(t *testing.T) {
	t.Parallel()
	idem := idemStore(t)
	g := newLinkGW(t, func(m *httpapi.Middleware) { m.Idempotency = httpapi.Idempotency(idem, fixedClock{}, m.Log) })
	first := g.req("POST", "/v1/deliveries", create)
	rec1 := g.do(t, first, http.StatusCreated)
	again := g.req("POST", "/v1/deliveries", create)
	again.Header.Set("Idempotency-Key", first.Header.Get("Idempotency-Key"))
	rec2 := g.do(t, again, http.StatusCreated)
	if rec2.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatal("not replayed")
	}
	if rec1.Body.String() != rec2.Body.String() || !strings.Contains(rec2.Body.String(), `"token":"`) {
		t.Fatalf("replayed body differs:\n%s\n%s", rec1.Body.String(), rec2.Body.String())
	}
	if g.fake.count() != 1 {
		t.Fatalf("deliveries calls = %d", g.fake.count())
	}
}
