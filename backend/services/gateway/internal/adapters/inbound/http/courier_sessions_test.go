package httpapi_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/raduolivas/dt/backend/pkg/apikey"
	"github.com/raduolivas/dt/backend/pkg/courierjwt"
	"github.com/raduolivas/dt/backend/pkg/logx"
	httpapi "github.com/raduolivas/dt/backend/services/gateway/internal/adapters/inbound/http"
	"github.com/raduolivas/dt/backend/services/gateway/internal/adapters/outbound/postgres"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app/apptest"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

const courierBase = "https://courier.example.invalid"

type sessionGW struct {
	*gw
	svc   *app.CourierSessions
	store *apptest.MemCourierSessions
	deny  *apptest.MemCourierDeny
}

// newSessionGW is a gateway edge with the courier-session routes over the
// real app service on in-memory stores; extra configures the middleware
// (e.g. a real idempotency store).
func newSessionGW(t *testing.T, extra func(*httpapi.Middleware)) *sessionGW {
	t.Helper()
	k := mint(t, apikey.Test, actor, domain.AllScopes...)
	seed := bytes.Repeat([]byte{7}, 32)
	signer, err := courierjwt.ParseKeys("k1:"+base64.StdEncoding.EncodeToString(seed), "k1")
	if err != nil {
		t.Fatal(err)
	}
	couriers := &apptest.MemCouriers{}
	couriers.Add(tenantID, domain.EnvTest, couID)
	store, deny := apptest.NewMemCourierSessions(), &apptest.MemCourierDeny{}
	svc := &app.CourierSessions{Repo: store, Hasher: apptest.Hasher{}, Signer: signer, Deny: deny,
		Couriers: couriers, Audit: &apptest.MemAudit{}, Clock: fixedClock{}}
	au := &memAudit{}
	rpcLogs := &bytes.Buffer{}
	e := newEdge(t, []testKey{k},
		withRoutes(httpapi.Routes(httpapi.Deps{CourierSessions: svc, CourierURLBase: courierBase + "/",
			Log: logx.New(rpcLogs, slog.LevelDebug, "gateway")})...),
		withMiddleware(func(m *httpapi.Middleware) {
			m.Audit = httpapi.Audit(au, fixedClock{}, m.Log)
			if extra != nil {
				extra(m)
			}
		}))
	return &sessionGW{gw: &gw{edge: e, key: k, audit: au, rpc: rpcLogs}, svc: svc, store: store, deny: deny}
}

type sessionJSON struct {
	ID         string     `json:"id"`
	CourierID  string     `json:"courier_id"`
	Mode       string     `json:"mode"`
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"started_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	EndedAt    *time.Time `json:"ended_at"`
	EndReason  *string    `json:"end_reason"`
	LastSeenAt *time.Time `json:"last_seen_at"`
}

type createdJSON struct {
	Session         sessionJSON `json:"session"`
	AccessToken     string      `json:"access_token"`
	AccessExpiresAt *time.Time  `json:"access_expires_at"`
	RefreshToken    string      `json:"refresh_token"`
	LinkURL         string      `json:"link_url"`
	LinkExpiresAt   *time.Time  `json:"link_expires_at"`
}

func decode[T any](t *testing.T, body []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func sdkBody() string { return `{"courier_id":"` + couID + `","mode":"sdk"}` }

func TestCreateSessionSDKReturnsTokensOnce(t *testing.T) {
	t.Parallel()
	pool := idemPool(t)
	g := newSessionGW(t, func(m *httpapi.Middleware) {
		m.Idempotency = httpapi.Idempotency(postgres.NewIdempotencyStore(pool, testBox(t)), fixedClock{}, m.Log)
	})
	req := func() *http.Request {
		r := g.req("POST", "/v1/courier-sessions", sdkBody())
		r.Header.Set("Idempotency-Key", "cs-replay")
		return r
	}
	rec := g.do(t, req(), http.StatusCreated)
	first := decode[createdJSON](t, rec.Body.Bytes())
	s := first.Session
	if first.AccessToken == "" || first.RefreshToken == "" || first.AccessExpiresAt == nil || first.LinkURL != "" ||
		s.Mode != "sdk" || s.Status != "active" || s.CourierID != couID || s.ExpiresAt == nil ||
		!s.ExpiresAt.Equal(fixedClock{}.Now().Add(domain.DefaultSessionLength)) {
		t.Fatalf("created = %+v", first)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
	if got := g.audit.all(); len(got) != 1 || got[0].Action != "courier_sessions.create" || got[0].Target != s.ID {
		t.Fatalf("audit = %+v", got)
	}

	// The stored response is sealed: neither token is readable in the table.
	var stored []byte
	if err := pool.QueryRow(context.Background(), `SELECT response_body FROM idempotency_records`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{first.RefreshToken, first.AccessToken, s.ID} {
		if bytes.Contains(stored, []byte(secret)) {
			t.Fatalf("idempotency record holds %q in plaintext", secret)
		}
	}

	// A retry with the same key gets the same tokens and starts nothing new.
	replay := g.do(t, req(), http.StatusCreated)
	if replay.Header().Get("Idempotent-Replayed") != "true" || !bytes.Equal(replay.Body.Bytes(), rec.Body.Bytes()) {
		t.Fatalf("replay = %v %s", replay.Header(), replay.Body.String())
	}
	if n := len(g.store.Events()); n != 1 {
		t.Fatalf("session events = %d, want 1", n)
	}
	if strings.Contains(g.logs.String(), first.RefreshToken) || strings.Contains(g.logs.String(), first.AccessToken) ||
		strings.Contains(g.rpc.String(), first.RefreshToken) {
		t.Fatal("token logged")
	}
}

func TestCreateSessionLinkURL(t *testing.T) {
	t.Parallel()
	g := newSessionGW(t, nil)
	rec := g.do(t, g.req("POST", "/v1/courier-sessions",
		`{"courier_id":"`+couID+`","mode":"link","expires_in_seconds":900}`), http.StatusCreated)
	got := decode[createdJSON](t, rec.Body.Bytes())
	code, ok := strings.CutPrefix(got.LinkURL, courierBase+"/s/")
	if !ok || !domain.ValidCourierLinkCode(code) {
		t.Fatalf("link_url = %q", got.LinkURL)
	}
	if got.LinkExpiresAt == nil || !got.LinkExpiresAt.Equal(fixedClock{}.Now().Add(domain.LinkCodeTTL)) {
		t.Fatalf("link_expires_at = %v", got.LinkExpiresAt)
	}
	if got.AccessToken != "" || got.RefreshToken != "" || got.Session.Mode != "link" || got.Session.Status != "pending_link" ||
		got.Session.ExpiresAt != nil {
		t.Fatalf("created = %+v", got)
	}
	// The code is the one the courier can exchange.
	if _, err := g.svc.Exchange(context.Background(), code, "203.0.113.9"); err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if strings.Contains(g.logs.String(), code) {
		t.Fatal("link code logged")
	}
}

func TestCreateSessionExpiresInSeconds(t *testing.T) {
	t.Parallel()
	g := newSessionGW(t, nil)
	rec := g.do(t, g.req("POST", "/v1/courier-sessions",
		`{"courier_id":"`+couID+`","mode":"sdk","expires_in_seconds":3600}`), http.StatusCreated)
	s := decode[createdJSON](t, rec.Body.Bytes()).Session
	if s.ExpiresAt == nil || !s.ExpiresAt.Equal(fixedClock{}.Now().Add(time.Hour)) {
		t.Fatalf("expires_at = %v", s.ExpiresAt)
	}
	g.do(t, g.req("POST", "/v1/courier-sessions",
		`{"courier_id":"`+couID+`","mode":"sdk","expires_in_seconds":60}`), http.StatusBadRequest)
}

func TestCreateSessionUnknownCourier404(t *testing.T) {
	t.Parallel()
	g := newSessionGW(t, nil)
	rec := g.req("POST", "/v1/courier-sessions", `{"courier_id":"`+couOther+`","mode":"sdk"}`)
	wantProblem(t, g.do(t, rec, http.StatusNotFound), http.StatusNotFound, "courier_not_found")
	if len(g.store.Events()) != 0 {
		t.Fatal("a session was started")
	}
}

func TestGetSession(t *testing.T) {
	t.Parallel()
	g := newSessionGW(t, nil)
	created := decode[createdJSON](t, g.do(t, g.req("POST", "/v1/courier-sessions", sdkBody()), http.StatusCreated).Body.Bytes())
	rec := g.do(t, g.req("GET", "/v1/courier-sessions/"+created.Session.ID, ""), http.StatusOK)
	if got := decode[sessionJSON](t, rec.Body.Bytes()); got.ID != created.Session.ID || got.Status != "active" || got.CourierID != couID {
		t.Fatalf("session = %+v", got)
	}
}

func TestDeleteSessionIdempotent(t *testing.T) {
	t.Parallel()
	g := newSessionGW(t, nil)
	created := decode[createdJSON](t, g.do(t, g.req("POST", "/v1/courier-sessions", sdkBody()), http.StatusCreated).Body.Bytes())
	path := "/v1/courier-sessions/" + created.Session.ID

	var bodies [2][]byte
	for i := range bodies {
		rec := g.do(t, g.req("DELETE", path, ""), http.StatusOK)
		bodies[i] = rec.Body.Bytes()
		s := decode[sessionJSON](t, bodies[i])
		if s.Status != "ended" || s.EndReason == nil || *s.EndReason != "partner" || s.EndedAt == nil {
			t.Fatalf("delete %d: %+v", i+1, s)
		}
	}
	if !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatalf("second delete changed the session:\n%s\n%s", bodies[0], bodies[1])
	}
	if denied, _ := g.deny.Denied(context.Background(), created.Session.ID); !denied {
		t.Fatal("ended session is not deny-listed")
	}
	got := decode[sessionJSON](t, g.do(t, g.req("GET", path, ""), http.StatusOK).Body.Bytes())
	if got.Status != "ended" || *got.EndReason != "partner" {
		t.Fatalf("get after delete = %+v", got)
	}
	// One audit row per DELETE (the HTTP hook), none from the app layer for a partner end.
	var del int
	for _, e := range g.audit.all() {
		if e.Action == "courier_sessions.end" && e.Target == created.Session.ID {
			del++
		}
	}
	if del != 2 {
		t.Fatalf("delete audit rows = %d, want 2: %+v", del, g.audit.all())
	}
}

func TestGetSessionOtherTenant404(t *testing.T) {
	t.Parallel()
	g := newSessionGW(t, nil)
	other := domain.Principal{TenantID: otherTenant, Env: domain.EnvTest, Credential: domain.CredentialSecretKey}
	couriers := g.svc.Couriers.(*apptest.MemCouriers)
	couriers.Add(otherTenant, domain.EnvTest, couOther)
	started, err := g.svc.Start(context.Background(), other, couOther, "sdk", 0)
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/courier-sessions/" + started.Session.ID
	wantProblem(t, g.do(t, g.req("GET", path, ""), http.StatusNotFound), http.StatusNotFound, "not_found")
	wantProblem(t, g.do(t, g.req("DELETE", path, ""), http.StatusNotFound), http.StatusNotFound, "not_found")
	// The other tenant's session is untouched.
	if s, err := g.svc.Get(context.Background(), other, started.Session.ID); err != nil || s.State != domain.SessionActive {
		t.Fatalf("other tenant's session = %+v, %v", s, err)
	}
}
