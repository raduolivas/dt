package httpapi_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

	"github.com/raduolivas/dt/backend/pkg/apikey"
	"github.com/raduolivas/dt/backend/pkg/logx"
	httpapi "github.com/raduolivas/dt/backend/services/gateway/internal/adapters/inbound/http"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

// memIdem is an IdempotencyStore that starts every request and keeps what
// Complete stored.
type memIdem struct {
	mu        sync.Mutex
	completed []app.StoredResponse
}

func (m *memIdem) Begin(_ context.Context, _ app.IdempotencyScope, _ []byte, now time.Time) (app.IdempotencyOutcome, error) {
	return app.IdempotencyOutcome{Kind: app.OutcomeStarted, Lease: now.Add(time.Minute)}, nil
}

func (m *memIdem) Complete(_ context.Context, _ app.IdempotencyScope, _ time.Time, resp app.StoredResponse) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.completed = append(m.completed, resp)
	return nil
}

func (m *memIdem) Abandon(context.Context, app.IdempotencyScope, time.Time) error { return nil }

// purgeCall is one PurgeResource call.
type purgeCall struct {
	tenant   string
	env      domain.Environment
	resource string
}

// memPurger records PurgeResource calls; err makes them fail.
type memPurger struct {
	mu    sync.Mutex
	calls []purgeCall
	err   error
}

func (m *memPurger) PurgeResource(_ context.Context, tenant string, env domain.Environment, resource string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, purgeCall{tenant, env, resource})
	return 1, m.err
}

func (m *memPurger) all() []purgeCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]purgeCall(nil), m.calls...)
}

// idemGW is the production routes over a fake deliveries client with the
// Idempotency hook over store and purger as the erasure purger.
func idemGW(t *testing.T, store app.IdempotencyStore, purger app.IdempotencyPurger) *gw {
	t.Helper()
	k := mint(t, apikey.Test, actor, domain.AllScopes...)
	f := newFake()
	rpcLogs := &bytes.Buffer{}
	e := newEdge(t, []testKey{k},
		withRoutes(httpapi.Routes(httpapi.Deps{
			Deliveries: f, Idempotency: purger, Log: logx.New(rpcLogs, slog.LevelDebug, "gateway"),
		})...),
		withMiddleware(func(m *httpapi.Middleware) {
			m.Idempotency = httpapi.Idempotency(store, fixedClock{}, m.Log)
		}))
	return &gw{edge: e, fake: f, key: k, rpc: rpcLogs}
}

func TestIdempotencyRecordNamesTheDelivery(t *testing.T) {
	t.Parallel()
	store := &memIdem{}
	g := idemGW(t, store, nil)
	// No Audit hook is mounted: the resource id must not depend on it.
	g.do(t, g.req("POST", "/v1/deliveries", create), http.StatusCreated)
	g.do(t, g.req("POST", "/v1/deliveries/del_01j9z3k8m2q4r6s8t0v2w4x6zz/cancel", ""), http.StatusOK)

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.completed) != 2 {
		t.Fatalf("%d records completed, want 2", len(store.completed))
	}
	// create names the id it made; {id} routes name their path id.
	if got := store.completed[0].ResourceID; got != delID {
		t.Errorf("create record resource = %q, want %q", got, delID)
	}
	if got := store.completed[1].ResourceID; got != "del_01j9z3k8m2q4r6s8t0v2w4x6zz" {
		t.Errorf("cancel record resource = %q, want del_01j9z3k8m2q4r6s8t0v2w4x6zz", got)
	}
}

func TestErasureDropsStoredResponses(t *testing.T) {
	t.Parallel()
	store := idemStore(t)
	g := idemGW(t, store, store)

	createReq := func() *http.Request {
		r := g.req("POST", "/v1/deliveries", create)
		r.Header.Set("Idempotency-Key", "erase-create")
		return r
	}
	g.do(t, createReq(), http.StatusCreated)
	if rec := g.do(t, createReq(), http.StatusCreated); rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatal("second create was not a replay")
	}
	if n := g.fake.count(); n != 1 {
		t.Fatalf("deliveries called %d times before erasure, want 1", n)
	}

	g.do(t, g.req("DELETE", "/v1/deliveries/"+delID+"/personal-data", ""), http.StatusOK)

	// The stored response (with the pre-erasure personal data) is gone: the
	// same key runs the request again instead of replaying it.
	rec := g.do(t, createReq(), http.StatusCreated)
	if rec.Header().Get("Idempotent-Replayed") != "" {
		t.Fatal("create was replayed after erasure")
	}
	if n := g.fake.count(); n != 3 { // create, erase, create
		t.Fatalf("deliveries called %d times, want 3", n)
	}
}

func TestErasurePurgesOnlyAfterSuccess(t *testing.T) {
	t.Parallel()
	p := &memPurger{}
	g := idemGW(t, &memIdem{}, p)
	g.fake.fail = rpcFail(connect.CodeNotFound, "not_found")
	g.do(t, g.req("DELETE", "/v1/deliveries/"+delID+"/personal-data", ""), http.StatusNotFound)
	if calls := p.all(); len(calls) != 0 {
		t.Fatalf("purged after a failed erasure: %v", calls)
	}

	g.fake.fail = nil
	g.do(t, g.req("DELETE", "/v1/deliveries/"+delID+"/personal-data", ""), http.StatusOK)
	want := purgeCall{tenant: tenantID, env: domain.EnvTest, resource: delID}
	if calls := p.all(); len(calls) != 1 || calls[0] != want {
		t.Fatalf("purge calls = %v, want [%v]", calls, want)
	}
}

func TestErasurePurgeFailureIsLoggedNotFatal(t *testing.T) {
	t.Parallel()
	p := &memPurger{err: errors.New("db down")}
	g := idemGW(t, &memIdem{}, p)
	g.do(t, g.req("DELETE", "/v1/deliveries/"+delID+"/personal-data", ""), http.StatusOK)
	logs := g.rpc.String()
	if !strings.Contains(logs, "idempotency_purge_failed") || !strings.Contains(logs, delID) {
		t.Fatalf("no purge failure log naming the delivery: %s", logs)
	}
}
