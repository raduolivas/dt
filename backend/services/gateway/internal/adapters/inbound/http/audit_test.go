package httpapi_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/raduolivas/dt/backend/pkg/apikey"
	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/pkg/ids"
	"github.com/raduolivas/dt/backend/pkg/migratex"
	"github.com/raduolivas/dt/backend/pkg/testkit"
	httpapi "github.com/raduolivas/dt/backend/services/gateway/internal/adapters/inbound/http"
	"github.com/raduolivas/dt/backend/services/gateway/internal/adapters/outbound/postgres"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
	"github.com/raduolivas/dt/backend/services/gateway/migrations"
)

const auditSpec = `
openapi: 3.1.0
info: {title: test, version: 1.0.0}
paths:
  /me:
    get:
      operationId: getMe
      security: [{SecretKey: []}]
      responses:
        '200': {description: ok}
  /things:
    post:
      operationId: createThing
      security: [{SecretKey: []}]
      requestBody:
        required: true
        content:
          application/json:
            schema: {type: object, required: [name]}
      responses:
        '201': {description: created}
  /things/{thing_id}/cancel:
    post:
      operationId: cancelThing
      security: [{SecretKey: []}]
      parameters:
        - {name: thing_id, in: path, required: true, schema: {type: string}}
      responses:
        '200': {description: ok}
components:
  securitySchemes:
    SecretKey: {type: http, scheme: bearer}
`

// auditRoutes are GET /v1/me, POST /v1/things (201, or a panic when the body
// is {"name":"panic"}) and POST /v1/things/{thing_id}/cancel (200).
func auditRoutes() []httpapi.Route { return auditRoutesScoped("") }

// auditRoutesScoped is auditRoutes with things.create requiring scope.
func auditRoutesScoped(scope domain.Scope) []httpapi.Route {
	creds := []domain.CredentialType{domain.CredentialSecretKey}
	return []httpapi.Route{
		httpapi.Routes(httpapi.Deps{})[0],
		{
			Name: "things.create", Method: http.MethodPost, Pattern: "/v1/things", Credentials: creds, Scope: scope,
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				if strings.Contains(string(b), "panic") {
					panic("boom")
				}
				w.WriteHeader(http.StatusCreated)
			}),
		},
		{
			Name: "things.cancel", Method: http.MethodPost, Pattern: "/v1/things/{thing_id}/cancel", Credentials: creds,
			Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		},
	}
}

func auditEdge(t *testing.T, s app.AuditStore, keys ...testKey) *edge {
	t.Helper()
	return auditEdgeWith(t, s, auditRoutes(), nil, keys...)
}

// auditEdgeWith mounts routes with the Audit hook over s; extra sets more
// hooks.
func auditEdgeWith(t *testing.T, s app.AuditStore, routes []httpapi.Route, extra func(*httpapi.Middleware), keys ...testKey) *edge {
	t.Helper()
	return newEdge(t, keys, withRoutes(routes...), withSpec(auditSpec),
		withMiddleware(func(m *httpapi.Middleware) {
			m.Audit = httpapi.Audit(s, fixedClock{}, m.Log)
			if extra != nil {
				extra(m)
			}
		}))
}

// memAudit is an in-memory AuditStore; err makes every Record fail.
type memAudit struct {
	mu      sync.Mutex
	entries []app.AuditEntry
	err     error
}

func (m *memAudit) Record(_ context.Context, e app.AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.entries = append(m.entries, e)
	return nil
}

func (m *memAudit) all() []app.AuditEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]app.AuditEntry(nil), m.entries...)
}

func auditPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pg := func() *testkit.Postgres {
		pgStarting <- struct{}{}
		defer func() { <-pgStarting }()
		return testkit.StartPostgres(t)
	}()
	ctx := context.Background()
	url := pg.URL("gateway")
	if err := migratex.Up(ctx, url, migrations.FS); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestAuditRecordsAuthenticatedWrites(t *testing.T) {
	t.Parallel()
	pool := auditPool(t)
	k := mint(t, apikey.Test, ids.New(ids.APIKey))
	e := auditEdge(t, postgres.NewAuditStore(pool), k)
	ctx := context.Background()

	req := post("/v1/things/thg_42/cancel", "Bearer "+k.raw, "")
	req.Header.Set("User-Agent", "dt-test/1.0")
	rec := e.do(req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST status = %d: %s", rec.Code, rec.Body.String())
	}
	reqID := rec.Header().Get(httpx.RequestIDHeader)

	type row struct {
		tenant, env, actorType, actorID, action, reqID string
		target, ua                                     *string
		status                                         int
		ip                                             *netip.Addr
		at                                             time.Time
	}
	rows := func() []row {
		t.Helper()
		rs, err := pool.Query(ctx, `SELECT tenant_id, environment, actor_type, actor_id, action, target,
			status, ip, user_agent, request_id, occurred_at FROM audit_log ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rs.Close()
		var out []row
		for rs.Next() {
			var r row
			if err := rs.Scan(&r.tenant, &r.env, &r.actorType, &r.actorID, &r.action, &r.target,
				&r.status, &r.ip, &r.ua, &r.reqID, &r.at); err != nil {
				t.Fatal(err)
			}
			out = append(out, r)
		}
		if err := rs.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}

	got := rows()
	if len(got) != 1 {
		t.Fatalf("rows = %d, want 1", len(got))
	}
	r := got[0]
	if r.tenant != tenantID || r.env != "test" || r.actorType != "secret_key" || r.actorID != k.rec.ID ||
		r.action != "things.cancel" || r.target == nil || *r.target != "thg_42" || r.status != http.StatusOK ||
		r.ip == nil || r.ip.String() != "198.51.100.9" || r.ua == nil || *r.ua != "dt-test/1.0" ||
		r.reqID == "" || r.reqID != reqID || !r.at.Equal(fixedClock{}.Now()) {
		t.Fatalf("row = %+v target %v ua %v ip %v; want things.cancel thg_42 200 from %s, request id %q",
			r, r.target, r.ua, r.ip, peer, reqID)
	}

	// The status recorded is the one the client got, also for a 4xx.
	if rec := e.do(post("/v1/things", "Bearer "+k.raw, `{}`)); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid body status = %d", rec.Code)
	}
	got = rows()
	if len(got) != 2 || got[1].status != http.StatusBadRequest || got[1].action != "things.create" || got[1].target != nil {
		t.Fatalf("rows after 400 = %+v, want a second row with status 400 and no target", got)
	}

	// Reads and unauthenticated writes are not recorded.
	if rec := e.do(get("/v1/me", "Bearer "+k.raw)); rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d", rec.Code)
	}
	if rec := e.do(post("/v1/things", "", `{"name":"x"}`)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated POST status = %d", rec.Code)
	}
	if n := len(rows()); n != 2 {
		t.Fatalf("rows = %d after GET and unauthenticated POST, want still 2", n)
	}
}

func TestAuditSkipsRequestsWithoutPrincipalOrReads(t *testing.T) {
	t.Parallel()
	m := &memAudit{}
	rt := auditRoutes()[1]
	h := httpapi.Audit(m, fixedClock{}, nil)(rt.Handler, rt)
	for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodHead} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, "/v1/things", strings.NewReader("{}")))
	}
	if n := len(m.all()); n != 0 {
		t.Fatalf("entries = %d, want 0 without a principal", n)
	}
}

func TestAuditEntryDetails(t *testing.T) {
	t.Parallel()
	k := mint(t, apikey.Live, ids.New(ids.APIKey))
	m := &memAudit{}
	e := auditEdge(t, m, k)

	// 510 ASCII bytes, then "€" (3 bytes): 512 would split it.
	ua := strings.Repeat("a", 510) + "€" + strings.Repeat("b", 100)
	req := post("/v1/things", "Bearer "+k.raw, `{"name":"x"}`)
	req.Header.Set("User-Agent", ua)
	req.RemoteAddr = "not-an-address"
	if rec := e.do(req); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	// A panicking handler is recorded with the 500 the client gets.
	if rec := e.do(post("/v1/things", "Bearer "+k.raw, `{"name":"panic"}`)); rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic status = %d", rec.Code)
	}

	got := m.all()
	if len(got) != 2 {
		t.Fatalf("entries = %d, want 2", len(got))
	}
	first := got[0]
	if first.UserAgent != strings.Repeat("a", 510) || !utf8.ValidString(first.UserAgent) {
		t.Fatalf("user agent = %d bytes %q…, want the first 510 bytes (cut on a rune boundary)", len(first.UserAgent), first.UserAgent[:8])
	}
	if first.IP.IsValid() {
		t.Fatalf("ip = %v, want the zero address for an unparsable peer", first.IP)
	}
	if first.Env != "live" || first.ActorType != "secret_key" || first.ActorID != k.rec.ID ||
		first.Action != "things.create" || first.Target != "" || first.Status != http.StatusCreated {
		t.Fatalf("entry = %+v", first)
	}
	if got[1].Status != http.StatusInternalServerError {
		t.Fatalf("panic entry status = %d, want 500", got[1].Status)
	}
}

func TestAuditFailureDoesNotChangeResponse(t *testing.T) {
	t.Parallel()
	k := mint(t, apikey.Test, ids.New(ids.APIKey))
	m := &memAudit{err: errors.New("db down")}
	e := auditEdge(t, m, k)

	rec := e.do(post("/v1/things/thg_1/cancel", "Bearer "+k.raw, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want the handler's 200 although the audit write failed", rec.Code)
	}
	logs := e.logs.String()
	if !strings.Contains(logs, `"msg":"audit_write_failed"`) || !strings.Contains(logs, `"level":"ERROR"`) {
		t.Fatalf("logs = %s, want audit_write_failed at error", logs)
	}
	for _, leak := range []string{k.raw, k.lookup, "198.51.100.9"} {
		if strings.Contains(logs, leak) {
			t.Fatalf("logs contain %q: %s", leak, logs)
		}
	}
}

func statuses(es []app.AuditEntry) []int {
	out := make([]int, len(es))
	for i, e := range es {
		out[i] = e.Status
	}
	return out
}

func TestAuditRecordsForbiddenWrites(t *testing.T) {
	t.Parallel()
	k := mint(t, apikey.Test, ids.New(ids.APIKey)) // no scopes
	m := &memAudit{}
	e := auditEdgeWith(t, m, auditRoutesScoped(domain.ScopeDeliveriesWrite), nil, k)

	wantProblem(t, e.do(post("/v1/things", "Bearer "+k.raw, `{"name":"x"}`)), http.StatusForbidden, "insufficient_scope")
	got := m.all()
	if len(got) != 1 || got[0].Status != http.StatusForbidden || got[0].Action != "things.create" || got[0].ActorID != k.rec.ID {
		t.Fatalf("entries = %+v, want one things.create entry with status 403", got)
	}
}

func TestAuditRecordsIdempotentReplaysAndConflicts(t *testing.T) {
	t.Parallel()
	idem := idemStore(t)
	k := mint(t, apikey.Test, ids.New(ids.APIKey))
	m := &memAudit{}
	e := auditEdgeWith(t, m, auditRoutes(), func(mw *httpapi.Middleware) {
		mw.Idempotency = httpapi.Idempotency(idem, fixedClock{}, mw.Log)
	}, k)

	first := e.do(postThing(k, `{"name":"x"}`, "idem-1"))
	replay := e.do(postThing(k, `{"name":"x"}`, "idem-1"))
	if first.Code != http.StatusCreated || replay.Code != http.StatusCreated || replay.Header().Get(httpapi.IdempotentReplayedHeader) != "true" {
		t.Fatalf("first %d, replay %d (replayed %q), want 201 twice with a replay", first.Code, replay.Code,
			replay.Header().Get(httpapi.IdempotentReplayedHeader))
	}
	wantProblem(t, e.do(postThing(k, `{"name":"other"}`, "idem-1")), http.StatusUnprocessableEntity, "idempotency_mismatch")
	wantProblem(t, e.do(postThing(k, `{"name":"x"}`)), http.StatusBadRequest, "idempotency_key_required")

	got := m.all()
	want := []int{http.StatusCreated, http.StatusCreated, http.StatusUnprocessableEntity, http.StatusBadRequest}
	if s := statuses(got); !slices.Equal(s, want) {
		t.Fatalf("audit statuses = %v, want %v (first run, replay, mismatch, missing key)", s, want)
	}
	if got[1].RequestID != replay.Header().Get(httpx.RequestIDHeader) {
		t.Fatalf("replay entry request id = %q, want the replay's %q", got[1].RequestID, replay.Header().Get(httpx.RequestIDHeader))
	}
}

// denyLimiter denies every request.
type denyLimiter struct{}

func (denyLimiter) Allow(context.Context, string, app.Limit) (app.Decision, error) {
	return app.Decision{Allowed: false, Limit: 100, RetryAfter: time.Second, ResetAfter: time.Second}, nil
}

func TestAuditSkipsRateLimited(t *testing.T) {
	t.Parallel()
	k := mint(t, apikey.Test, ids.New(ids.APIKey))
	m := &memAudit{}
	e := auditEdgeWith(t, m, auditRoutes(), func(mw *httpapi.Middleware) {
		mw.RateLimit = httpapi.PartnerRateLimit(denyLimiter{}, mw.Log)
	}, k)

	wantProblem(t, e.do(post("/v1/things", "Bearer "+k.raw, `{"name":"x"}`)), http.StatusTooManyRequests, "rate_limited")
	if got := m.all(); len(got) != 0 {
		t.Fatalf("entries = %+v, want none for a 429 (no audit amplification)", got)
	}
}

// burstLimiter is an in-memory limiter that allows l.Burst calls per key and
// then denies (no refill).
type burstLimiter struct {
	mu   sync.Mutex
	used map[string]int
}

func (b *burstLimiter) Allow(_ context.Context, key string, l app.Limit) (app.Decision, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used == nil {
		b.used = map[string]int{}
	}
	if b.used[key] >= l.Burst {
		return app.Decision{Allowed: false, Limit: l.Burst, RetryAfter: time.Second, ResetAfter: time.Second}, nil
	}
	b.used[key]++
	return app.Decision{Allowed: true, Limit: l.Burst, Remaining: l.Burst - b.used[key], ResetAfter: time.Second}, nil
}

// TestForbiddenRequestsAreRateLimited checks RateLimit runs before Authorize:
// a key lacking the route's scope is counted on every request, so past its
// burst it gets 429 (not 403), and those 429s are not audited while the 403s
// before them are.
func TestForbiddenRequestsAreRateLimited(t *testing.T) {
	t.Parallel()
	k := mint(t, apikey.Test, ids.New(ids.APIKey)) // no scopes
	m := &memAudit{}
	e := auditEdgeWith(t, m, auditRoutesScoped(domain.ScopeDeliveriesWrite), func(mw *httpapi.Middleware) {
		mw.RateLimit = httpapi.PartnerRateLimit(&burstLimiter{}, mw.Log)
	}, k)

	for i := range app.PartnerKey.Burst {
		rec := e.do(post("/v1/things", "Bearer "+k.raw, `{"name":"x"}`))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("request %d: status = %d, want 403: %s", i+1, rec.Code, rec.Body.String())
		}
	}
	if n := len(m.all()); n != app.PartnerKey.Burst {
		t.Fatalf("audit entries = %d, want %d (one per 403)", n, app.PartnerKey.Burst)
	}
	wantProblem(t, e.do(post("/v1/things", "Bearer "+k.raw, `{"name":"x"}`)), http.StatusTooManyRequests, "rate_limited")
	got := m.all()
	if n := len(got); n != app.PartnerKey.Burst {
		t.Fatalf("audit entries = %d after the 429, want %d (a 429 is never audited)", n, app.PartnerKey.Burst)
	}
	for _, e := range got {
		if e.Status != http.StatusForbidden {
			t.Fatalf("audit entry status = %d, want only 403s", e.Status)
		}
	}
}
