package httpapi_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	deliveriesv1 "github.com/raduolivas/dt/backend/gen/dt/deliveries/v1"
	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	trackingv1 "github.com/raduolivas/dt/backend/gen/dt/tracking/v1"
	"github.com/raduolivas/dt/backend/pkg/apikey"
	"github.com/raduolivas/dt/backend/pkg/courierjwt"
	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/pkg/logx"
	"github.com/raduolivas/dt/backend/pkg/viewerjwt"
	httpapi "github.com/raduolivas/dt/backend/services/gateway/internal/adapters/inbound/http"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app/apptest"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

const (
	otherCou = "cou_01j9z3k8m2q4r6s8t0v2w4x6z9"
	fixBody  = `{"fixes":[{"t":"2026-10-20T11:59:58Z","lat":-23.5612,"lng":-46.6557,"accuracy_m":6.5}]}`
	evtBody  = `{"event_id":"e-1","type":"pickup_confirmed"}`
)

var (
	partnerTest = domain.Principal{TenantID: tenantID, Env: domain.EnvTest, CredentialID: actor,
		Credential: domain.CredentialSecretKey, Scopes: domain.AllScopes}
	partnerLive  = domain.Principal{TenantID: tenantID, Env: domain.EnvLive, CredentialID: actor, Credential: domain.CredentialSecretKey}
	partnerOther = domain.Principal{TenantID: otherTenant, Env: domain.EnvTest, CredentialID: actor, Credential: domain.CredentialSecretKey}
)

// courierEdge is the courier routes over the real sessions service on
// in-memory stores, with fake deliveries and tracking services.
type courierEdge struct {
	t      *testing.T
	h      http.Handler
	svc    *app.CourierSessions
	store  *apptest.MemCourierSessions
	audit  *apptest.MemAudit
	signer *courierjwt.Signer
	dels   *fakeDeliveries
	trk    *fakeTracking
	logs   *bytes.Buffer
}

func openKey(tenant string, env eventsv1.Environment, courier string) string {
	return tenant + "/" + env.String() + "/" + courier
}

// newCourierEdge mounts MountCourier; lim nil means no rate limits.
func newCourierEdge(t *testing.T, lim app.Limiter) *courierEdge {
	t.Helper()
	signer, err := courierjwt.ParseKeys("k1:"+base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)), "k1")
	if err != nil {
		t.Fatal(err)
	}
	couriers := &apptest.MemCouriers{}
	for _, p := range []domain.Principal{partnerTest, partnerLive, partnerOther} {
		couriers.Add(p.TenantID, p.Env, couID)
	}
	couriers.Add(tenantID, domain.EnvTest, otherCou)
	e := &courierEdge{t: t, store: apptest.NewMemCourierSessions(), audit: &apptest.MemAudit{}, signer: signer,
		dels: newFake(), trk: newFakeTracking(), logs: &bytes.Buffer{}}
	e.svc = &app.CourierSessions{Repo: e.store, Hasher: apptest.Hasher{}, Signer: signer, Deny: &apptest.MemCourierDeny{},
		Couriers: couriers, Audit: e.audit, Clock: fixedClock{}, RequestID: httpx.RequestIDFrom}
	e.dels.open = map[string][]*deliveriesv1.CourierDelivery{
		openKey(tenantID, eventsv1.Environment_ENVIRONMENT_TEST, couID): {courierDelivery(delID)},
	}
	e.dels.assigned = map[string]string{openKey(tenantID, eventsv1.Environment_ENVIRONMENT_TEST, delID): couID}
	log := logx.New(e.logs, slog.LevelDebug, "gateway")
	mux := http.NewServeMux()
	httpapi.Mount(mux, httpapi.Routes(httpapi.Deps{}), httpapi.Middleware{
		Auth: &app.Authenticator{Keys: &store{byLookup: map[string]domain.KeyRecord{}}, Hasher: hasher{}, Clock: fixedClock{}}, Log: log,
	})
	httpapi.MountCourier(mux, httpapi.CourierDeps{Sessions: e.svc, Deliveries: e.dels, Tracking: e.trk, Limiter: lim,
		Clock: fixedClock{}, Log: log})
	e.h = httpx.RequestID(httpx.AccessLog(log)(httpx.Recover(log)(mux)))
	return e
}

func courierDelivery(id string) *deliveriesv1.CourierDelivery {
	return &deliveriesv1.CourierDelivery{Id: id, Reference: "order-1042", Model: "direct", Status: "courier_assigned",
		Pickup:  &deliveriesv1.CourierPickup{Location: ll(-23.5614, -46.6559), Label: "Pizzaria Central"},
		Dropoff: &deliveriesv1.CourierDropoff{Location: ll(-23.5505, -46.6333)}, NextActions: []string{"at_pickup", "failed"}}
}

func (e *courierEdge) do(r *http.Request) *httptest.ResponseRecorder {
	e.t.Helper()
	if r.RemoteAddr == "192.0.2.1:1234" {
		r.RemoteAddr = peer
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

// sdk starts an sdk session for couID as p and returns it with its tokens.
func (e *courierEdge) sdk(p domain.Principal) app.StartResult {
	e.t.Helper()
	res, err := e.svc.Start(context.Background(), p, couID, "sdk", 0)
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

func cReq(method, path, token, body string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("Origin", "https://courier-app.example")
	return r
}

func tokenReq(body string) *http.Request { return cReq("POST", "/v1/courier/token", "", body) }

type tokensJSON struct {
	AccessToken      string    `json:"access_token"`
	AccessExpiresAt  time.Time `json:"access_expires_at"`
	RefreshToken     string    `json:"refresh_token"`
	SessionExpiresAt time.Time `json:"session_expires_at"`
}

func TestTokenLinkCodeExchange(t *testing.T) {
	t.Parallel()
	e := newCourierEdge(t, nil)
	res, err := e.svc.Start(context.Background(), partnerTest, couID, "link", 0)
	if err != nil {
		t.Fatal(err)
	}
	rec := e.do(tokenReq(`{"grant_type":"link_code","code":"` + res.LinkCode + `"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	got := decode[tokensJSON](t, rec.Body.Bytes())
	if got.AccessToken == "" || got.RefreshToken == "" || !got.AccessExpiresAt.Equal(fixedClock{}.Now().Add(courierjwt.TTL)) ||
		!got.SessionExpiresAt.Equal(fixedClock{}.Now().Add(domain.DefaultSessionLength)) {
		t.Fatalf("tokens = %+v", got)
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("headers = %v", rec.Header())
	}
	// The access token works at once.
	if rec := e.do(cReq("GET", "/v1/courier/deliveries", got.AccessToken, "")); rec.Code != http.StatusOK {
		t.Fatalf("deliveries with the new token = %d: %s", rec.Code, rec.Body.String())
	}
	// The code works once; an unknown one is refused with its own code.
	wantProblem(t, e.do(tokenReq(`{"grant_type":"link_code","code":"`+res.LinkCode+`"}`)), http.StatusUnauthorized, "link_code_used")
	wantProblem(t, e.do(tokenReq(`{"grant_type":"link_code","code":"nope"}`)), http.StatusUnauthorized, "link_code_invalid")
	if l := e.logs.String(); strings.Contains(l, res.LinkCode) || strings.Contains(l, got.RefreshToken) || strings.Contains(l, got.AccessToken) {
		t.Fatal("token or code logged")
	}
}

func TestTokenRefreshReuseEndsSession(t *testing.T) {
	t.Parallel()
	e := newCourierEdge(t, nil)
	first := e.sdk(partnerTest)
	rec := e.do(tokenReq(`{"grant_type":"refresh_token","refresh_token":"` + first.Tokens.RefreshToken + `"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh = %d: %s", rec.Code, rec.Body.String())
	}
	second := decode[tokensJSON](t, rec.Body.Bytes())
	if second.RefreshToken == first.Tokens.RefreshToken {
		t.Fatal("refresh token not rotated")
	}
	// The old token again: reuse. The session ends, the new tokens die with it.
	wantProblem(t, e.do(tokenReq(`{"grant_type":"refresh_token","refresh_token":"`+first.Tokens.RefreshToken+`"}`)),
		http.StatusUnauthorized, "refresh_token_invalid")
	s, err := e.svc.Get(context.Background(), partnerTest, first.Session.ID)
	if err != nil || s.State != domain.SessionEnded || s.EndReason != domain.EndRefreshReuse {
		t.Fatalf("session = %+v, %v", s, err)
	}
	wantProblem(t, e.do(cReq("GET", "/v1/courier/deliveries", second.AccessToken, "")), http.StatusUnauthorized, "unauthenticated")
	wantProblem(t, e.do(tokenReq(`{"grant_type":"refresh_token","refresh_token":"`+second.RefreshToken+`"}`)),
		http.StatusUnauthorized, "session_ended")
	wantProblem(t, e.do(tokenReq(`{"grant_type":"refresh_token","refresh_token":"`+strings.Repeat("A", 43)+`"}`)),
		http.StatusUnauthorized, "refresh_token_invalid")
}

// The schema is a flat object, so the handler enforces the pairing.
func TestTokenGrantPairing(t *testing.T) {
	t.Parallel()
	e := newCourierEdge(t, nil)
	res := e.sdk(partnerTest)
	for name, body := range map[string]string{
		"link_code without code":            `{"grant_type":"link_code"}`,
		"refresh_token without token":       `{"grant_type":"refresh_token"}`,
		"link_code with a refresh token":    `{"grant_type":"link_code","refresh_token":"` + res.Tokens.RefreshToken + `"}`,
		"refresh_token with a code":         `{"grant_type":"refresh_token","code":"abc"}`,
		"link_code with both":               `{"grant_type":"link_code","code":"abc","refresh_token":"` + res.Tokens.RefreshToken + `"}`,
		"refresh_token with both":           `{"grant_type":"refresh_token","code":"abc","refresh_token":"` + res.Tokens.RefreshToken + `"}`,
		"unknown grant (the schema's enum)": `{"grant_type":"password","code":"abc"}`,
	} {
		t.Run(name, func(t *testing.T) {
			wantProblem(t, e.do(tokenReq(body)), http.StatusBadRequest, "validation_failed")
		})
	}
	// None of them touched the session: its refresh token still works.
	if rec := e.do(tokenReq(`{"grant_type":"refresh_token","refresh_token":"` + res.Tokens.RefreshToken + `"}`)); rec.Code != http.StatusOK {
		t.Fatalf("refresh after rejected requests = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestTokenIPLimitCountsFailures(t *testing.T) {
	t.Parallel()
	e := newCourierEdge(t, &burstLimiterMem{n: map[string]int{}})
	for i := 1; i <= 20; i++ {
		wantProblem(t, e.do(tokenReq(`{"grant_type":"link_code","code":"guess-`+strconv.Itoa(i)+`"}`)), http.StatusUnauthorized, "link_code_invalid")
	}
	rec := e.do(tokenReq(`{"grant_type":"link_code","code":"guess-21"}`))
	wantProblem(t, rec, http.StatusTooManyRequests, "rate_limited")
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("no Retry-After")
	}
	// The browser can read the refusal.
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("429 without CORS: %v", rec.Header())
	}
	// Another address has its own bucket.
	r := tokenReq(`{"grant_type":"link_code","code":"guess"}`)
	r.RemoteAddr = "198.51.100.77:5000"
	wantProblem(t, e.do(r), http.StatusUnauthorized, "link_code_invalid")
}

// TestTokenUnknownRefreshChargesIP: an unknown refresh token has no session
// bucket, so its failure is charged to the client IP's link-code bucket,
// like a failed link code. A valid refresh is never charged there.
func TestTokenUnknownRefreshChargesIP(t *testing.T) {
	t.Parallel()
	e := newCourierEdge(t, &burstLimiterMem{n: map[string]int{}})
	refresh := e.sdk(partnerTest).Tokens.RefreshToken
	for i := 1; i <= 20; i++ {
		wantProblem(t, e.do(tokenReq(`{"grant_type":"refresh_token","refresh_token":"guess-`+strconv.Itoa(i)+`"}`)), http.StatusUnauthorized, "refresh_token_invalid")
	}
	rec := e.do(tokenReq(`{"grant_type":"refresh_token","refresh_token":"guess-21"}`))
	wantProblem(t, rec, http.StatusTooManyRequests, "rate_limited")
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("no Retry-After")
	}
	// The same bucket as the link codes.
	wantProblem(t, e.do(tokenReq(`{"grant_type":"link_code","code":"guess"}`)), http.StatusTooManyRequests, "rate_limited")
	// A real refresh from the same address still works: only failures count.
	if rec := e.do(tokenReq(`{"grant_type":"refresh_token","refresh_token":"` + refresh + `"}`)); rec.Code != http.StatusOK {
		t.Fatalf("valid refresh = %d: %s", rec.Code, rec.Body.String())
	}
	// Another address has its own bucket.
	r := tokenReq(`{"grant_type":"refresh_token","refresh_token":"guess"}`)
	r.RemoteAddr = "198.51.100.77:5000"
	wantProblem(t, e.do(r), http.StatusUnauthorized, "refresh_token_invalid")
}

func TestTokenRefreshLimitPerSession(t *testing.T) {
	t.Parallel()
	e := newCourierEdge(t, &burstLimiterMem{n: map[string]int{}})
	refresh := e.sdk(partnerTest).Tokens.RefreshToken
	for i := 0; i < 10; i++ {
		rec := e.do(tokenReq(`{"grant_type":"refresh_token","refresh_token":"` + refresh + `"}`))
		if rec.Code != http.StatusOK {
			t.Fatalf("refresh %d = %d: %s", i, rec.Code, rec.Body.String())
		}
		refresh = decode[tokensJSON](t, rec.Body.Bytes()).RefreshToken
	}
	// Refused before rotating: the session survives and the token still works later.
	rec := e.do(tokenReq(`{"grant_type":"refresh_token","refresh_token":"` + refresh + `"}`))
	wantProblem(t, rec, http.StatusTooManyRequests, "rate_limited")
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("no Retry-After")
	}
}

func TestLocationsSourceFromToken(t *testing.T) {
	t.Parallel()
	e := newCourierEdge(t, nil)
	sdkRes := e.sdk(partnerTest)
	rec := e.do(cReq("POST", "/v1/courier/locations", sdkRes.Tokens.AccessToken, fixBody))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"accepted":2,"dropped":1}` {
		t.Fatalf("locations = %d: %s", rec.Code, rec.Body.String())
	}
	req := e.trk.last().(*trackingv1.IngestRequest)
	if req.GetSource() != "sdk" || req.GetCourierId() != couID || req.GetScope().GetTenantId() != tenantID ||
		req.GetScope().GetEnvironment() != eventsv1.Environment_ENVIRONMENT_TEST || len(req.GetFixes()) != 1 ||
		req.GetFixes()[0].GetLat() != -23.5612 {
		t.Fatalf("ingest = %v", req)
	}
	// Touch: the session was seen.
	if s, err := e.svc.Get(context.Background(), partnerTest, sdkRes.Session.ID); err != nil || s.LastSeenAt == nil {
		t.Fatalf("last_seen_at not set: %+v, %v", s, err)
	}

	// A link session's token says "link".
	link, err := e.svc.Start(context.Background(), partnerTest, otherCou, "link", 0)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := e.svc.Exchange(context.Background(), link.LinkCode, "203.0.113.9")
	if err != nil {
		t.Fatal(err)
	}
	if rec := e.do(cReq("POST", "/v1/courier/locations", tok.AccessToken, fixBody)); rec.Code != http.StatusOK {
		t.Fatalf("link locations = %d: %s", rec.Code, rec.Body.String())
	}
	req = e.trk.last().(*trackingv1.IngestRequest)
	if req.GetSource() != "link" || req.GetCourierId() != otherCou {
		t.Fatalf("ingest = %v", req)
	}
}

func TestLocationsValidation(t *testing.T) {
	t.Parallel()
	e := newCourierEdge(t, nil)
	tok := e.sdk(partnerTest).Tokens.AccessToken
	for name, body := range map[string]string{
		"no fixes":     `{"fixes":[]}`,
		"bad latitude": `{"fixes":[{"t":"2026-10-20T11:59:58Z","lat":91,"lng":0,"accuracy_m":5}]}`,
		"extra field":  `{"fixes":[{"t":"2026-10-20T11:59:58Z","lat":1,"lng":0,"accuracy_m":5}],"courier_id":"` + otherCou + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			wantProblem(t, e.do(cReq("POST", "/v1/courier/locations", tok, body)), http.StatusBadRequest, "validation_failed")
		})
	}
	if e.trk.count() != 0 {
		t.Fatal("tracking called for an invalid batch")
	}
	// 16 KiB for a batch, 1 KiB elsewhere.
	big := `{"fixes":[],"pad":"` + strings.Repeat("x", httpapi.MaxRelayBodyBytes) + `"}`
	wantProblem(t, e.do(cReq("POST", "/v1/courier/locations", tok, big)), http.StatusRequestEntityTooLarge, "payload_too_large")
	small := `{"event_id":"e","type":"delivered","reason":"` + strings.Repeat("x", httpapi.MaxCourierBodyBytes) + `"}`
	wantProblem(t, e.do(cReq("POST", "/v1/courier/deliveries/"+delID+"/events", tok, small)), http.StatusRequestEntityTooLarge, "payload_too_large")
}

func TestLocationsRateLimited(t *testing.T) {
	t.Parallel()
	e := newCourierEdge(t, &burstLimiterMem{n: map[string]int{}})
	tok := e.sdk(partnerTest).Tokens.AccessToken
	for i := 0; i < 5; i++ {
		if rec := e.do(cReq("POST", "/v1/courier/locations", tok, fixBody)); rec.Code != http.StatusOK {
			t.Fatalf("batch %d = %d: %s", i, rec.Code, rec.Body.String())
		}
	}
	rec := e.do(cReq("POST", "/v1/courier/locations", tok, fixBody))
	wantProblem(t, rec, http.StatusTooManyRequests, "rate_limited")
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("no Retry-After")
	}
	if e.trk.count() != 5 {
		t.Fatalf("tracking calls = %d, want 5", e.trk.count())
	}
	// Another session has its own bucket.
	other := e.sdk(partnerOther).Tokens.AccessToken
	if rec := e.do(cReq("POST", "/v1/courier/locations", other, fixBody)); rec.Code != http.StatusOK {
		t.Fatalf("other session = %d", rec.Code)
	}
}

func TestEventsRateLimited(t *testing.T) {
	t.Parallel()
	e := newCourierEdge(t, &burstLimiterMem{n: map[string]int{}})
	tok := e.sdk(partnerTest).Tokens.AccessToken
	for i := 0; i < 10; i++ {
		if rec := e.do(cReq("POST", "/v1/courier/deliveries/"+delID+"/events", tok, evtBody)); rec.Code != http.StatusOK {
			t.Fatalf("event %d = %d: %s", i, rec.Code, rec.Body.String())
		}
	}
	wantProblem(t, e.do(cReq("POST", "/v1/courier/deliveries/"+delID+"/events", tok, evtBody)), http.StatusTooManyRequests, "rate_limited")
}

func TestEventNotAssigned404(t *testing.T) {
	t.Parallel()
	e := newCourierEdge(t, nil)
	tok := e.sdk(partnerTest).Tokens.AccessToken
	// A real delivery of the tenant that is not this courier's, and an id
	// that exists nowhere, look the same: deliveries refuses both under its
	// row lock because the gateway sends the token's courier as the guard.
	for _, id := range []string{delLive, "del_01j9z3k8m2q4r6s8t0v2w4x6zz"} {
		wantProblem(t, e.do(cReq("POST", "/v1/courier/deliveries/"+id+"/events", tok, evtBody)), http.StatusNotFound, "not_found")
		req := e.dels.last().(*deliveriesv1.ReportEventRequest)
		if req.GetCourierId() != couID || req.GetId() != id {
			t.Fatalf("ReportEvent = %v", req)
		}
	}
	// A malformed id is a 400, not a lookup.
	wantProblem(t, e.do(cReq("POST", "/v1/courier/deliveries/x/events", tok, evtBody)), http.StatusBadRequest, "validation_failed")
}

// TestEventRetryAfterFinalIsDuplicate pins Review Focus 4 on a final tap: a
// delivered report whose answer was lost is retried with the same event id
// after the delivery left the courier's open list, and the answer is still
// 200 duplicate, not 404.
func TestEventRetryAfterFinalIsDuplicate(t *testing.T) {
	t.Parallel()
	e := newCourierEdge(t, nil)
	tok := e.sdk(partnerTest).Tokens.AccessToken
	body := `{"event_id":"e-final","type":"delivered"}`
	if rec := e.do(cReq("POST", "/v1/courier/deliveries/"+delID+"/events", tok, body)); rec.Code != http.StatusOK {
		t.Fatalf("delivered = %d: %s", rec.Code, rec.Body.String())
	}
	// The delivery is final now: no longer among the courier's open ones.
	e.dels.mu.Lock()
	e.dels.open = map[string][]*deliveriesv1.CourierDelivery{}
	e.dels.del.Status = "delivered"
	e.dels.mu.Unlock()
	rec := e.do(cReq("POST", "/v1/courier/deliveries/"+delID+"/events", tok, body))
	if rec.Code != http.StatusOK {
		t.Fatalf("retry = %d: %s", rec.Code, rec.Body.String())
	}
	out := decode[struct {
		Outcome  string         `json:"outcome"`
		Delivery map[string]any `json:"delivery"`
	}](t, rec.Body.Bytes())
	if out.Outcome != "duplicate" || out.Delivery["status"] != "delivered" {
		t.Fatalf("retry = %s", rec.Body.String())
	}
}

func TestEventSetsActorCourier(t *testing.T) {
	t.Parallel()
	e := newCourierEdge(t, nil)
	res := e.sdk(partnerTest)
	e.dels.nextActions = []string{"delivered", "failed"}
	e.dels.del.Status = "picked_up"
	e.dels.del.Notes = ptr("Ring twice")
	e.dels.del.Customer = &deliveriesv1.Customer{FirstName: ptr("Ana")}
	e.dels.del.Metadata = map[string]string{"table": "12"}
	rec := e.do(cReq("POST", "/v1/courier/deliveries/"+delID+"/events", res.Tokens.AccessToken,
		`{"event_id":"e-1","type":"pickup_confirmed","occurred_at":"2026-10-20T11:59:00Z","reason":"r"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("event = %d: %s", rec.Code, rec.Body.String())
	}
	req := e.dels.last().(*deliveriesv1.ReportEventRequest)
	if req.GetActor() != "courier" || req.GetCourierId() != couID || req.GetBy() != "" || req.GetId() != delID || req.GetEventId() != "e-1" ||
		req.GetType() != "pickup_confirmed" || req.GetScope().GetTenantId() != tenantID ||
		req.GetOccurredAt().AsTime() != time.Date(2026, 10, 20, 11, 59, 0, 0, time.UTC) {
		t.Fatalf("ReportEvent = %v", req)
	}
	var out struct {
		Outcome  string         `json:"outcome"`
		Delivery map[string]any `json:"delivery"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	d := out.Delivery
	if out.Outcome != "applied" || d["status"] != "picked_up" || d["reference"] != "order-1042" || d["model"] != "direct" ||
		d["next_actions"].([]any)[0] != "delivered" || d["dropoff"].(map[string]any)["notes"] != "Ring twice" {
		t.Fatalf("response = %s", rec.Body.String())
	}
	// Only the courier's fields: no customer, metadata, courier or policy.
	for _, k := range []string{"customer", "metadata", "courier", "external_id", "pickup_policy", "notes"} {
		if _, found := d[k]; found {
			t.Errorf("delivery has %q: %s", k, rec.Body.String())
		}
	}
	if strings.Contains(rec.Body.String(), "Ana") {
		t.Fatal("customer name leaked")
	}
}

func ptr[T any](v T) *T { return &v }

func TestListDeliveriesIsTheCouriersOwn(t *testing.T) {
	t.Parallel()
	e := newCourierEdge(t, nil)
	rec := e.do(cReq("GET", "/v1/courier/deliveries", e.sdk(partnerTest).Tokens.AccessToken, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Deliveries []map[string]any `json:"deliveries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Deliveries) != 1 || out.Deliveries[0]["id"] != delID || out.Deliveries[0]["pickup_confirmations"] == nil {
		t.Fatalf("deliveries = %s", rec.Body.String())
	}
	// Another tenant's courier with the same external id sees nothing of it.
	rec = e.do(cReq("GET", "/v1/courier/deliveries", e.sdk(partnerOther).Tokens.AccessToken, ""))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"deliveries":[]}` {
		t.Fatalf("other tenant = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSessionEndByCourier(t *testing.T) {
	t.Parallel()
	e := newCourierEdge(t, nil)
	res := e.sdk(partnerTest)
	rec := e.do(cReq("POST", "/v1/courier/session/end", res.Tokens.AccessToken, ""))
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("end = %d: %s", rec.Code, rec.Body.String())
	}
	s, err := e.svc.Get(context.Background(), partnerTest, res.Session.ID)
	if err != nil || s.State != domain.SessionEnded || s.EndReason != domain.EndCourier {
		t.Fatalf("session = %+v, %v", s, err)
	}
	// Audited once, by the app service.
	n := 0
	for _, a := range e.audit.Entries() {
		if a.Action == "courier_sessions.end" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("end audited %d times: %+v", n, e.audit.Entries())
	}
	wantProblem(t, e.do(cReq("POST", "/v1/courier/session/end", res.Tokens.AccessToken, "")), http.StatusUnauthorized, "unauthenticated")
}

func TestRevokedTokenGets401WithinOneRequest(t *testing.T) {
	t.Parallel()
	e := newCourierEdge(t, nil)
	res := e.sdk(partnerTest)
	if rec := e.do(cReq("GET", "/v1/courier/deliveries", res.Tokens.AccessToken, "")); rec.Code != http.StatusOK {
		t.Fatalf("before revoke = %d", rec.Code)
	}
	if _, err := e.svc.End(context.Background(), partnerTest, res.Session.ID, "partner"); err != nil {
		t.Fatal(err)
	}
	calls := e.trk.count()
	wantProblem(t, e.do(cReq("POST", "/v1/courier/locations", res.Tokens.AccessToken, fixBody)), http.StatusUnauthorized, "unauthenticated")
	wantProblem(t, e.do(cReq("GET", "/v1/courier/deliveries", res.Tokens.AccessToken, "")), http.StatusUnauthorized, "unauthenticated")
	if e.trk.count() != calls {
		t.Fatal("a revoked token reached tracking")
	}
	// A new session for the same courier replaces the old one the same way.
	again := e.sdk(partnerTest)
	repl := e.sdk(partnerTest)
	wantProblem(t, e.do(cReq("GET", "/v1/courier/deliveries", again.Tokens.AccessToken, "")), http.StatusUnauthorized, "unauthenticated")
	if rec := e.do(cReq("GET", "/v1/courier/deliveries", repl.Tokens.AccessToken, "")); rec.Code != http.StatusOK {
		t.Fatalf("replacing session = %d", rec.Code)
	}
}

func TestSecretKeyOnCourierRoute401(t *testing.T) {
	t.Parallel()
	e := newCourierEdge(t, nil)
	k := mint(t, apikey.Test, actor, domain.AllScopes...)
	for _, rt := range courierRoutes {
		if !rt.bearer {
			continue
		}
		wantProblem(t, e.do(cReq(rt.method, rt.path, k.raw, rt.body)), http.StatusUnauthorized, "unauthenticated")
	}
	if e.trk.count() != 0 || e.dels.count() != 0 {
		t.Fatal("a secret key reached a service")
	}
}

func TestViewerTokenOnCourierRoute401(t *testing.T) {
	t.Parallel()
	e := newCourierEdge(t, nil)
	jwt, _, err := testSigner(t).Sign(viewerjwt.Claims{DeliveryID: delID, TenantID: tenantID, Env: "test", LinkID: "tl_01j9z3k8m2q4r6s8t0v2w4x6y8"}, fixedClock{}.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, rt := range courierRoutes {
		if !rt.bearer {
			continue
		}
		wantProblem(t, e.do(cReq(rt.method, rt.path, jwt, rt.body)), http.StatusUnauthorized, "unauthenticated")
	}
	if e.trk.count() != 0 || e.dels.count() != 0 {
		t.Fatal("a viewer token reached a service")
	}
}

func TestCORSAnyOriginNoCredentials(t *testing.T) {
	t.Parallel()
	e := newCourierEdge(t, nil)
	tok := e.sdk(partnerTest).Tokens.AccessToken
	for _, rt := range courierRoutes {
		pre := cReq("OPTIONS", rt.path, "", "")
		pre.Header.Set("Origin", "https://partner-app.example")
		pre.Header.Set("Access-Control-Request-Method", rt.method)
		pre.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
		rec := e.do(pre)
		h := rec.Header()
		if rec.Code != http.StatusNoContent || h.Get("Access-Control-Allow-Origin") != "*" ||
			h.Get("Access-Control-Allow-Credentials") != "" ||
			!strings.Contains(h.Get("Access-Control-Allow-Headers"), "Authorization") ||
			!strings.Contains(h.Get("Access-Control-Allow-Headers"), "Content-Type") ||
			!strings.Contains(h.Get("Access-Control-Allow-Methods"), rt.method) {
			t.Errorf("%s preflight = %d %v", rt.path, rec.Code, h)
		}
		// The real answers carry it too, refusals included.
		for _, token := range []string{tok, "garbage"} {
			rec := e.do(cReq(rt.method, rt.path, token, rt.body))
			if rec.Header().Get("Access-Control-Allow-Origin") != "*" || rec.Header().Get("Access-Control-Allow-Credentials") != "" {
				t.Errorf("%s %s answer without CORS: %v", rt.method, rt.path, rec.Header())
			}
		}
	}
}

// courierRoutes are the courier routes with a request body that is valid for
// each; bearer is false for the one that takes no token.
var courierRoutes = []struct {
	name, method, path, body string
	bearer                   bool
	success                  int
}{
	{"courier.token", "POST", "/v1/courier/token", `{"grant_type":"link_code","code":"x"}`, false, http.StatusUnauthorized},
	{"courier.list_deliveries", "GET", "/v1/courier/deliveries", "", true, http.StatusOK},
	{"courier.report_locations", "POST", "/v1/courier/locations", fixBody, true, http.StatusOK},
	{"courier.report_event", "POST", "/v1/courier/deliveries/" + delID + "/events", evtBody, true, http.StatusOK},
	{"courier.end_session", "POST", "/v1/courier/session/end", "", true, http.StatusNoContent},
}

func TestReadRateLimited(t *testing.T) {
	t.Parallel()
	e := newCourierEdge(t, &burstLimiterMem{n: map[string]int{}})
	tok := e.sdk(partnerTest).Tokens.AccessToken
	for i := 1; i <= 30; i++ {
		if rec := e.do(cReq("GET", "/v1/courier/deliveries", tok, "")); rec.Code != http.StatusOK {
			t.Fatalf("call %d = %d: %s", i, rec.Code, rec.Body.String())
		}
	}
	rec := e.do(cReq("GET", "/v1/courier/deliveries", tok, ""))
	wantProblem(t, rec, http.StatusTooManyRequests, "rate_limited")
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("no Retry-After")
	}
	// The session end shares the bucket; another session is unaffected.
	wantProblem(t, e.do(cReq("POST", "/v1/courier/session/end", tok, "")), http.StatusTooManyRequests, "rate_limited")
	other := e.sdk(partnerOther).Tokens.AccessToken
	if rec := e.do(cReq("POST", "/v1/courier/session/end", other, "")); rec.Code != http.StatusNoContent {
		t.Fatalf("other session end = %d", rec.Code)
	}
}
