package httpapi_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/raduolivas/dt/backend/pkg/apikey"
	"github.com/raduolivas/dt/backend/pkg/migratex"
	"github.com/raduolivas/dt/backend/pkg/sealbox"
	"github.com/raduolivas/dt/backend/pkg/testkit"
	httpapi "github.com/raduolivas/dt/backend/services/gateway/internal/adapters/inbound/http"
	"github.com/raduolivas/dt/backend/services/gateway/internal/adapters/outbound/postgres"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
	"github.com/raduolivas/dt/backend/services/gateway/migrations"
)

// pgStarting limits concurrent container starts (rootless Docker's port
// forwarder fails when many start at once).
var pgStarting = make(chan struct{}, 2)

// idemStore starts Postgres with the gateway schema and returns the real
// idempotency store over it.
func idemStore(t *testing.T) *postgres.IdempotencyStore {
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
	return postgres.NewIdempotencyStore(pool, testBox(t))
}

const idemSpec = `
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
            schema: {type: object}
      responses:
        '201': {description: created}
components:
  securitySchemes:
    SecretKey: {type: http, scheme: bearer}
`

// counted wraps h and counts its calls.
type counted struct {
	n atomic.Int32
	h http.HandlerFunc
}

func (c *counted) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.n.Add(1)
	c.h(w, r)
}

func (c *counted) calls() int { return int(c.n.Load()) }

// created answers 201 with a body that changes on every call, so a replay is
// recognisable.
func created(c *counted) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Handler", "ran")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"call":%d}`, c.calls())
	}
}

// idemEdge mounts GET /v1/me and POST /v1/things (handled by h) with the
// Idempotency hook over store.
func idemEdge(t *testing.T, store app.IdempotencyStore, h http.Handler, keys ...testKey) *edge {
	t.Helper()
	things := httpapi.Route{
		Name: "things.create", Method: http.MethodPost, Pattern: "/v1/things",
		Credentials: []domain.CredentialType{domain.CredentialSecretKey}, Env: httpapi.AnyEnv,
		Handler: h,
	}
	return newEdge(t, keys,
		withRoutes(httpapi.Routes(httpapi.Deps{})[0], things),
		withSpec(idemSpec),
		withMiddleware(func(m *httpapi.Middleware) {
			m.Idempotency = httpapi.Idempotency(store, fixedClock{}, m.Log)
		}))
}

// postThing is POST /v1/things with body; idemKeys are sent as separate
// Idempotency-Key lines (none: header absent).
func postThing(k testKey, body string, idemKeys ...string) *http.Request {
	r := post("/v1/things", "Bearer "+k.raw, body)
	for _, ik := range idemKeys {
		r.Header.Add("Idempotency-Key", ik)
	}
	return r
}

// failingStore fails the test if the request ever reaches the store, or
// returns err from every call.
type failingStore struct {
	t   *testing.T
	err error
}

func (f failingStore) Begin(context.Context, app.IdempotencyScope, []byte, time.Time) (app.IdempotencyOutcome, error) {
	if f.err == nil {
		f.t.Error("store reached")
	}
	return app.IdempotencyOutcome{}, f.err
}

func (f failingStore) Complete(context.Context, app.IdempotencyScope, time.Time, app.StoredResponse) error {
	f.t.Error("Complete reached")
	return nil
}

func (f failingStore) Abandon(context.Context, app.IdempotencyScope, time.Time) error {
	f.t.Error("Abandon reached")
	return nil
}

func TestPostWithoutKey(t *testing.T) {
	t.Parallel()
	k := mint(t, apikey.Test, "ak_01j9z3k8m2q4r6s8t0v2w4x6y8")
	h := &counted{}
	h.h = created(h)
	e := idemEdge(t, failingStore{t: t}, h, k)

	wantProblem(t, e.do(postThing(k, `{}`)), http.StatusBadRequest, "idempotency_key_required")
	for name, keys := range map[string][]string{
		"256 chars":   {strings.Repeat("k", 256)},
		"newline":     {"abc\ndef"},
		"empty":       {""},
		"non-ascii":   {"café"},
		"two headers": {"a", "b"},
	} {
		rec := e.do(postThing(k, `{}`, keys...))
		if rec.Code != http.StatusBadRequest || problem(t, rec).Code != "idempotency_key_invalid" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if h.calls() != 0 {
		t.Fatalf("handler ran %d times", h.calls())
	}
	// GET routes need no key.
	if rec := e.do(get("/v1/me", "Bearer "+k.raw)); rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/me = %d: %s", rec.Code, rec.Body.String())
	}
	// An unauthenticated POST is a 401, not an idempotency error.
	wantProblem(t, e.do(post("/v1/things", "", `{}`)), http.StatusUnauthorized, "unauthenticated")
}

func TestIdempotencyStoreFailureFailsClosed(t *testing.T) {
	t.Parallel()
	k := mint(t, apikey.Test, "ak_01j9z3k8m2q4r6s8t0v2w4x6y8")
	h := &counted{}
	h.h = created(h)
	e := idemEdge(t, failingStore{t: t, err: errors.New("db down")}, h, k)
	wantProblem(t, e.do(postThing(k, `{}`, "k1")), http.StatusInternalServerError, "internal_error")
	if h.calls() != 0 {
		t.Fatal("handler ran without an idempotency record")
	}
	if !strings.Contains(e.logs.String(), "idempotency_unavailable") {
		t.Fatalf("no error log: %s", e.logs.String())
	}
}

func TestReplaySameBody(t *testing.T) {
	t.Parallel()
	k := mint(t, apikey.Test, "ak_01j9z3k8m2q4r6s8t0v2w4x6y8")
	h := &counted{}
	h.h = created(h)
	e := idemEdge(t, idemStore(t), h, k)

	first := e.do(postThing(k, `{"a":1}`, "order-1"))
	second := e.do(postThing(k, `{"a":1}`, "order-1"))
	if h.calls() != 1 {
		t.Fatalf("handler ran %d times, want 1", h.calls())
	}
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated {
		t.Fatalf("status = %d, %d: %s", first.Code, second.Code, second.Body.String())
	}
	if first.Body.String() != `{"call":1}` || second.Body.String() != first.Body.String() {
		t.Fatalf("bodies = %q, %q", first.Body.String(), second.Body.String())
	}
	if second.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("replayed Content-Type = %q", second.Header().Get("Content-Type"))
	}
	if first.Header().Get("Idempotent-Replayed") != "" || second.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("Idempotent-Replayed = %q, %q", first.Header().Get("Idempotent-Replayed"), second.Header().Get("Idempotent-Replayed"))
	}
	if second.Header().Get("X-Handler") != "" {
		t.Fatal("replay restored a header outside the allowlist")
	}
	id1, id2 := first.Header().Get("Request-Id"), second.Header().Get("Request-Id")
	if id1 == "" || id2 == "" || id1 == id2 {
		t.Fatalf("Request-Id = %q, %q; the replay must carry its own", id1, id2)
	}
}

func TestMismatchBody(t *testing.T) {
	t.Parallel()
	k := mint(t, apikey.Test, "ak_01j9z3k8m2q4r6s8t0v2w4x6y8")
	h := &counted{}
	h.h = created(h)
	e := idemEdge(t, idemStore(t), h, k)

	if rec := e.do(postThing(k, `{"a":1}`, "order-1")); rec.Code != http.StatusCreated {
		t.Fatalf("first = %d", rec.Code)
	}
	wantProblem(t, e.do(postThing(k, `{"a":2}`, "order-1")), http.StatusUnprocessableEntity, "idempotency_mismatch")
	if h.calls() != 1 {
		t.Fatalf("handler ran %d times", h.calls())
	}
}

func TestIdempotencyConcurrentDuplicate(t *testing.T) {
	t.Parallel()
	k := mint(t, apikey.Test, "ak_01j9z3k8m2q4r6s8t0v2w4x6y8")
	entered, release := make(chan struct{}), make(chan struct{})
	h := &counted{}
	h.h = func(w http.ResponseWriter, r *http.Request) {
		close(entered) // a second call would panic: exactly once
		<-release
		created(h)(w, r)
	}
	e := idemEdge(t, idemStore(t), h, k)

	const n = 10
	results := make(chan *httptest.ResponseRecorder, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			<-start
			results <- e.do(postThing(k, `{"a":1}`, "race-1"))
		})
	}
	close(start)

	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatal("no request reached the handler")
	}
	// While the first is held in the handler, every other one is refused.
	for i := range n - 1 {
		select {
		case rec := <-results:
			wantProblem(t, rec, http.StatusConflict, "idempotency_in_progress")
		case <-time.After(30 * time.Second):
			t.Fatalf("only %d duplicates answered", i)
		}
	}
	close(release)
	wg.Wait()
	if rec := <-results; rec.Code != http.StatusCreated {
		t.Fatalf("first = %d: %s", rec.Code, rec.Body.String())
	}
	// Afterwards, an exact replay.
	rec := e.do(postThing(k, `{"a":1}`, "race-1"))
	if rec.Code != http.StatusCreated || rec.Header().Get("Idempotent-Replayed") != "true" || rec.Body.String() != `{"call":1}` {
		t.Fatalf("after: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	if h.calls() != 1 {
		t.Fatalf("handler ran %d times, want exactly 1", h.calls())
	}
}

func TestKeysScopedPerTenantAndEnv(t *testing.T) {
	t.Parallel()
	a := mint(t, apikey.Test, "ak_a")
	b := mint(t, apikey.Test, "ak_b")
	b.rec.TenantID = "ten_01j9z3k8m2q4r6s8t0v2w4x6yb"
	live := mint(t, apikey.Live, "ak_live")
	h := &counted{}
	h.h = created(h)
	e := idemEdge(t, idemStore(t), h, a, b, live)

	for _, k := range []testKey{a, b, live} {
		rec := e.do(postThing(k, `{"a":1}`, "shared-key"))
		if rec.Code != http.StatusCreated || rec.Header().Get("Idempotent-Replayed") != "" {
			t.Fatalf("%s: %d %v", k.rec.ID, rec.Code, rec.Header())
		}
	}
	if h.calls() != 3 {
		t.Fatalf("handler ran %d times, want 3", h.calls())
	}
}

func TestServerErrorIsNotStored(t *testing.T) {
	t.Parallel()
	k := mint(t, apikey.Test, "ak_01j9z3k8m2q4r6s8t0v2w4x6y8")
	store := idemStore(t)

	t.Run("500", func(t *testing.T) {
		h := &counted{}
		h.h = func(w http.ResponseWriter, r *http.Request) {
			if h.calls() == 1 {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			created(h)(w, r)
		}
		e := idemEdge(t, store, h, k)
		if rec := e.do(postThing(k, `{}`, "err-500")); rec.Code != http.StatusInternalServerError {
			t.Fatalf("first = %d", rec.Code)
		}
		if rec := e.do(postThing(k, `{}`, "err-500")); rec.Code != http.StatusCreated || rec.Header().Get("Idempotent-Replayed") != "" {
			t.Fatalf("retry = %d %v", rec.Code, rec.Header())
		}
		if rec := e.do(postThing(k, `{}`, "err-500")); rec.Header().Get("Idempotent-Replayed") != "true" {
			t.Fatalf("third was not a replay: %d", rec.Code)
		}
		if h.calls() != 2 {
			t.Fatalf("handler ran %d times, want 2", h.calls())
		}
	})

	t.Run("panic", func(t *testing.T) {
		h := &counted{}
		h.h = func(w http.ResponseWriter, r *http.Request) {
			if h.calls() == 1 {
				panic("handler bug")
			}
			created(h)(w, r)
		}
		e := idemEdge(t, store, h, k)
		wantProblem(t, e.do(postThing(k, `{}`, "err-panic")), http.StatusInternalServerError, "internal_error")
		if rec := e.do(postThing(k, `{}`, "err-panic")); rec.Code != http.StatusCreated {
			t.Fatalf("retry = %d", rec.Code)
		}
		if h.calls() != 2 {
			t.Fatalf("handler ran %d times, want 2", h.calls())
		}
	})

	t.Run("response over 64 KiB", func(t *testing.T) {
		big := strings.Repeat("x", app.MaxIdempotentResponseBytes+1)
		h := &counted{}
		h.h = func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusCreated)
			for i := 0; i < len(big); i += 1000 {
				_, _ = w.Write([]byte(big[i:min(i+1000, len(big))]))
			}
		}
		e := idemEdge(t, store, h, k)
		for i := range 2 {
			rec := e.do(postThing(k, `{}`, "big"))
			if rec.Code != http.StatusCreated || rec.Body.String() != big || rec.Header().Get("Idempotent-Replayed") != "" {
				t.Fatalf("request %d: %d, %d bytes", i+1, rec.Code, rec.Body.Len())
			}
		}
		if h.calls() != 2 {
			t.Fatalf("handler ran %d times, want 2 (large responses are not stored)", h.calls())
		}
	})

	t.Run("4xx is stored", func(t *testing.T) {
		h := &counted{}
		h.h = func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "conflict", http.StatusConflict)
		}
		e := idemEdge(t, store, h, k)
		e.do(postThing(k, `{}`, "err-409"))
		rec := e.do(postThing(k, `{}`, "err-409"))
		if rec.Code != http.StatusConflict || rec.Header().Get("Idempotent-Replayed") != "true" || h.calls() != 1 {
			t.Fatalf("4xx replay: %d %v calls %d", rec.Code, rec.Header(), h.calls())
		}
	})
}

// testBox seals with a test-only key.
func testBox(t *testing.T) *sealbox.Box {
	t.Helper()
	b, err := sealbox.ParseKeys("t1:"+base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)), "t1")
	if err != nil {
		t.Fatal(err)
	}
	return b
}
