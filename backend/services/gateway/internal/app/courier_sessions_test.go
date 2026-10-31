package app_test

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raduolivas/dt/backend/pkg/courierjwt"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app/apptest"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

var (
	csNow   = time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	partner = domain.Principal{TenantID: "ten_a", Env: domain.EnvTest, CredentialID: "ak_1",
		Credential: domain.CredentialSecretKey, Scopes: []domain.Scope{domain.ScopeCouriersWrite}}
)

const (
	cou1 = "cou_01j9z3k8m2q4r6s8t0v2w4x6y1"
	cou2 = "cou_01j9z3k8m2q4r6s8t0v2w4x6y2"
)

type moveClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *moveClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *moveClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func testSigner(t *testing.T, seed byte) *courierjwt.Signer {
	t.Helper()
	b := make([]byte, 32)
	for i := range b {
		b[i] = seed
	}
	s, err := courierjwt.ParseKeys("k1:"+base64.StdEncoding.EncodeToString(b), "k1")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type courierFixture struct {
	svc   *app.CourierSessions
	store *apptest.MemCourierSessions
	deny  *apptest.MemCourierDeny
	audit *apptest.MemAudit
	clock *moveClock
}

func newCourierFixture(t *testing.T) *courierFixture {
	t.Helper()
	f := &courierFixture{store: apptest.NewMemCourierSessions(), deny: &apptest.MemCourierDeny{},
		audit: &apptest.MemAudit{}, clock: &moveClock{t: csNow}}
	couriers := &apptest.MemCouriers{}
	couriers.Add(partner.TenantID, partner.Env, cou1)
	couriers.Add(partner.TenantID, partner.Env, cou2)
	f.svc = &app.CourierSessions{Repo: f.store, Hasher: apptest.Hasher{}, Signer: testSigner(t, 7), Deny: f.deny,
		Couriers: couriers, Audit: f.audit, Clock: f.clock}
	return f
}

func (f *courierFixture) get(t *testing.T, id string) domain.CourierSession {
	t.Helper()
	s, err := f.svc.Get(context.Background(), partner, id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *courierFixture) startSDK(t *testing.T, courierID string) app.StartResult {
	t.Helper()
	r, err := f.svc.Start(context.Background(), partner, courierID, "sdk", 0)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (f *courierFixture) startLink(t *testing.T, courierID string) app.StartResult {
	t.Helper()
	r, err := f.svc.Start(context.Background(), partner, courierID, "link", 0)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestStartSDKReplacesActive(t *testing.T) {
	f := newCourierFixture(t)
	ctx := context.Background()
	r1 := f.startSDK(t, cou1)
	if r1.Session.State != domain.SessionActive || r1.Session.Mode != domain.ModeSDK || r1.LinkCode != "" ||
		r1.Tokens.AccessToken == "" || r1.Tokens.RefreshToken == "" ||
		!r1.Tokens.SessionExpiresAt.Equal(csNow.Add(12*time.Hour)) || !r1.Tokens.AccessExpiresAt.Equal(csNow.Add(courierjwt.TTL)) {
		t.Fatalf("start = %+v", r1)
	}
	if _, _, err := f.svc.Authenticate(ctx, r1.Tokens.AccessToken); err != nil {
		t.Fatalf("first token: %v", err)
	}
	r2 := f.startSDK(t, cou1)
	old := f.get(t, r1.Session.ID)
	if old.State != domain.SessionEnded || old.EndReason != domain.EndReplaced {
		t.Fatalf("old session = %+v", old)
	}
	if _, _, err := f.svc.Authenticate(ctx, r1.Tokens.AccessToken); !errors.Is(err, app.ErrUnauthenticated) {
		t.Fatalf("replaced token: %v", err)
	}
	if _, err := f.svc.Refresh(ctx, r1.Tokens.RefreshToken); !errors.Is(err, app.ErrSessionEnded) {
		t.Fatalf("replaced refresh: %v", err)
	}
	if _, _, err := f.svc.Authenticate(ctx, r2.Tokens.AccessToken); err != nil {
		t.Fatalf("new token: %v", err)
	}
	// Another courier's session is untouched.
	r3 := f.startSDK(t, cou2)
	if f.get(t, r2.Session.ID).State != domain.SessionActive || f.get(t, r3.Session.ID).State != domain.SessionActive {
		t.Fatal("a session of another courier was ended")
	}
	// The replacement is audited as a system end.
	e := f.audit.Entries()
	if len(e) != 1 || e[0].ActorType != "system" || e[0].Action != "courier_sessions.replaced" || e[0].Target != r1.Session.ID ||
		e[0].TenantID != partner.TenantID || e[0].Env != partner.Env {
		t.Fatalf("audit = %+v", e)
	}
}

func TestStartLinkDoesNotEndActiveUntilExchange(t *testing.T) {
	f := newCourierFixture(t)
	ctx := context.Background()
	sdk := f.startSDK(t, cou1)
	link := f.startLink(t, cou1)
	if link.Session.State != domain.SessionPendingLink || link.LinkCode == "" || link.Tokens.AccessToken != "" ||
		!link.LinkExpiresAt.Equal(csNow.Add(15*time.Minute)) || link.Session.StartedAt != nil {
		t.Fatalf("link start = %+v", link)
	}
	if f.get(t, sdk.Session.ID).State != domain.SessionActive {
		t.Fatal("minting a link ended the working session")
	}
	f.clock.Advance(time.Minute)
	tok, err := f.svc.Exchange(ctx, link.LinkCode, "203.0.113.9")
	if err != nil {
		t.Fatal(err)
	}
	if old := f.get(t, sdk.Session.ID); old.State != domain.SessionEnded || old.EndReason != domain.EndReplaced {
		t.Fatalf("old session after exchange = %+v", old)
	}
	s := f.get(t, link.Session.ID)
	if s.State != domain.SessionActive || !s.StartedAt.Equal(csNow.Add(time.Minute)) ||
		!tok.SessionExpiresAt.Equal(csNow.Add(time.Minute+12*time.Hour)) {
		t.Fatalf("exchanged session = %+v, tokens %+v", s, tok)
	}
	_, claims, err := f.svc.Authenticate(ctx, tok.AccessToken)
	if err != nil || claims.Source != "link" || claims.SessionID != link.Session.ID || claims.CourierID != cou1 {
		t.Fatalf("link token claims = %+v, %v", claims, err)
	}
}

func TestNewerPendingLinkCancelsOlder(t *testing.T) {
	f := newCourierFixture(t)
	ctx := context.Background()
	l1 := f.startLink(t, cou1)
	l2 := f.startLink(t, cou1)
	if old := f.get(t, l1.Session.ID); old.State != domain.SessionEnded || old.EndReason != domain.EndReplaced {
		t.Fatalf("older link = %+v", old)
	}
	if _, err := f.svc.Exchange(ctx, l1.LinkCode, ""); !errors.Is(err, app.ErrLinkCodeInvalid) {
		t.Fatalf("cancelled code: %v", err)
	}
	if _, err := f.svc.Exchange(ctx, l2.LinkCode, ""); err != nil {
		t.Fatalf("newer code: %v", err)
	}
	// A cancelled link never started, so it has no session events.
	for _, e := range f.store.Events() {
		if e.SessionID == l1.Session.ID {
			t.Fatalf("event for a link never exchanged: %+v", e)
		}
	}
}

func TestExchangeTwiceIsLinkCodeUsed(t *testing.T) {
	f := newCourierFixture(t)
	ctx := context.Background()
	l := f.startLink(t, cou1)
	tok, err := f.svc.Exchange(ctx, l.LinkCode, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Exchange(ctx, l.LinkCode, ""); !errors.Is(err, app.ErrLinkCodeUsed) {
		t.Fatalf("second exchange: %v", err)
	}
	// The phone that used it first keeps working.
	if _, _, err := f.svc.Authenticate(ctx, tok.AccessToken); err != nil {
		t.Fatalf("first phone: %v", err)
	}
	// Still "used" after the session ends.
	if _, err := f.svc.End(ctx, partner, l.Session.ID, "partner"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Exchange(ctx, l.LinkCode, ""); !errors.Is(err, app.ErrLinkCodeUsed) {
		t.Fatalf("exchange after end: %v", err)
	}
	for _, code := range []string{"", "nope", strings.Repeat("A", 22)} {
		if _, err := f.svc.Exchange(ctx, code, ""); !errors.Is(err, app.ErrLinkCodeInvalid) {
			t.Fatalf("exchange %q: %v", code, err)
		}
	}
}

func TestExchangeExpiredCode(t *testing.T) {
	f := newCourierFixture(t)
	l := f.startLink(t, cou1)
	f.clock.Advance(15*time.Minute + time.Second)
	if _, err := f.svc.Exchange(context.Background(), l.LinkCode, ""); !errors.Is(err, app.ErrLinkCodeInvalid) {
		t.Fatalf("expired code: %v", err)
	}
	if s := f.get(t, l.Session.ID); s.Status(f.clock.Now()) != domain.SessionEnded || s.EndReason != domain.EndExpired {
		t.Fatalf("expired link session = %+v", s)
	}
}

func TestRefreshRotates(t *testing.T) {
	f := newCourierFixture(t)
	ctx := context.Background()
	r := f.startSDK(t, cou1)
	f.clock.Advance(10 * time.Minute)
	t2, err := f.svc.Refresh(ctx, r.Tokens.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if t2.RefreshToken == r.Tokens.RefreshToken || t2.AccessToken == r.Tokens.AccessToken ||
		!t2.AccessExpiresAt.Equal(csNow.Add(10*time.Minute+courierjwt.TTL)) || !t2.SessionExpiresAt.Equal(csNow.Add(12*time.Hour)) {
		t.Fatalf("refreshed = %+v", t2)
	}
	if s := f.get(t, r.Session.ID); s.RefreshGeneration != 1 || s.State != domain.SessionActive {
		t.Fatalf("after rotate = %+v", s)
	}
	t3, err := f.svc.Refresh(ctx, t2.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	// The first (superseded) token is reuse: the session ends.
	if _, err := f.svc.Refresh(ctx, r.Tokens.RefreshToken); !errors.Is(err, app.ErrRefreshReuse) {
		t.Fatalf("reuse: %v", err)
	}
	if s := f.get(t, r.Session.ID); s.State != domain.SessionEnded || s.EndReason != domain.EndRefreshReuse {
		t.Fatalf("after reuse = %+v", s)
	}
	if _, _, err := f.svc.Authenticate(ctx, t3.AccessToken); !errors.Is(err, app.ErrUnauthenticated) {
		t.Fatalf("access token after reuse: %v", err)
	}
	if _, err := f.svc.Refresh(ctx, t3.RefreshToken); !errors.Is(err, app.ErrSessionEnded) {
		t.Fatalf("current token after reuse: %v", err)
	}
	if _, err := f.svc.Refresh(ctx, strings.Repeat("A", 43)); !errors.Is(err, app.ErrRefreshInvalid) {
		t.Fatalf("unknown token: %v", err)
	}
	if _, err := f.svc.Refresh(ctx, "short"); !errors.Is(err, app.ErrRefreshInvalid) {
		t.Fatalf("malformed token: %v", err)
	}
	e := f.audit.Entries()
	if len(e) != 1 || e[0].Action != "courier_sessions.refresh_reuse" || e[0].ActorType != "system" {
		t.Fatalf("audit = %+v", e)
	}

	// A session past its expiry refuses refresh.
	r2 := f.startSDK(t, cou2)
	f.clock.Advance(12 * time.Hour)
	if _, err := f.svc.Refresh(ctx, r2.Tokens.RefreshToken); !errors.Is(err, app.ErrSessionEnded) {
		t.Fatalf("expired session refresh: %v", err)
	}
}

func TestAuthenticateDeniedAfterEnd(t *testing.T) {
	f := newCourierFixture(t)
	ctx := context.Background()
	r := f.startSDK(t, cou1)
	p, claims, err := f.svc.Authenticate(ctx, r.Tokens.AccessToken)
	if err != nil || p.Credential != domain.CredentialCourier || p.CredentialID != r.Session.ID ||
		p.TenantID != partner.TenantID || p.Env != domain.EnvTest || !p.Has(domain.ScopeLocationWrite) ||
		!p.Has(domain.ScopeStatusWrite) || p.Has(domain.ScopeCouriersWrite) || claims.CourierID != cou1 || claims.Source != "sdk" {
		t.Fatalf("principal = %+v, claims %+v, %v", p, claims, err)
	}
	ended, err := f.svc.End(ctx, partner, r.Session.ID, "partner")
	if err != nil || ended.State != domain.SessionEnded || ended.EndReason != domain.EndPartner {
		t.Fatalf("end = %+v, %v", ended, err)
	}
	if _, _, err := f.svc.Authenticate(ctx, r.Tokens.AccessToken); !errors.Is(err, app.ErrUnauthenticated) {
		t.Fatalf("after end: %v", err)
	}
	// Ending again is idempotent and keeps the first reason.
	again, err := f.svc.End(ctx, partner, r.Session.ID, "partner")
	if err != nil || again.EndReason != domain.EndPartner || !again.EndedAt.Equal(*ended.EndedAt) {
		t.Fatalf("second end = %+v, %v", again, err)
	}
	// A partner end is audited by the HTTP hook, not here.
	if e := f.audit.Entries(); len(e) != 0 {
		t.Fatalf("audit = %+v", e)
	}

	// The courier ends its own session: deny-listed and audited.
	r2 := f.startSDK(t, cou2)
	if _, err := f.svc.EndSession(ctx, r2.Session.ID, "courier"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.Authenticate(ctx, r2.Tokens.AccessToken); !errors.Is(err, app.ErrUnauthenticated) {
		t.Fatalf("after courier end: %v", err)
	}
	e := f.audit.Entries()
	if len(e) != 1 || e[0].ActorType != "courier" || e[0].ActorID != r2.Session.ID || e[0].Action != "courier_sessions.end" {
		t.Fatalf("audit = %+v", e)
	}
	// A deny-list that cannot be read fails closed.
	r3 := f.startSDK(t, cou1)
	f.deny.Fail(errors.New("redis down"))
	if _, _, err := f.svc.Authenticate(ctx, r3.Tokens.AccessToken); err == nil {
		t.Fatal("authenticated without a deny-list check")
	}
	// And an end that cannot be deny-listed fails, so the caller retries.
	if _, err := f.svc.End(ctx, partner, r3.Session.ID, "partner"); err == nil {
		t.Fatal("end without deny-listing succeeded")
	}
	f.deny.Fail(nil)
	if _, err := f.svc.End(ctx, partner, r3.Session.ID, "partner"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.Authenticate(ctx, r3.Tokens.AccessToken); !errors.Is(err, app.ErrUnauthenticated) {
		t.Fatalf("after retried end: %v", err)
	}
}

func TestAuthenticateRejectsOtherEnvToken(t *testing.T) {
	f := newCourierFixture(t)
	ctx := context.Background()
	r := f.startSDK(t, cou1)
	// A token signed by another deployment's keys (staging vs production)
	// never verifies here.
	other, _, err := testSigner(t, 9).Sign(courierjwt.Claims{CourierID: cou1, TenantID: partner.TenantID, Env: "test",
		SessionID: r.Session.ID, Source: "sdk"}, csNow)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.Authenticate(ctx, other); !errors.Is(err, app.ErrUnauthenticated) {
		t.Fatalf("other deployment's token: %v", err)
	}
	// A test-environment session's token acts in test, never live.
	p, _, err := f.svc.Authenticate(ctx, r.Tokens.AccessToken)
	if err != nil || p.Env != domain.EnvTest {
		t.Fatalf("principal env = %q, %v", p.Env, err)
	}
	for _, tok := range []string{"", "garbage", r.Tokens.RefreshToken} {
		if _, _, err := f.svc.Authenticate(ctx, tok); !errors.Is(err, app.ErrUnauthenticated) {
			t.Fatalf("token %q: %v", tok, err)
		}
	}
	// An expired access token is refused.
	f.clock.Advance(courierjwt.TTL + courierjwt.Leeway + time.Second)
	if _, _, err := f.svc.Authenticate(ctx, r.Tokens.AccessToken); !errors.Is(err, app.ErrUnauthenticated) {
		t.Fatalf("expired token: %v", err)
	}
}

func TestStartUnknownCourier404(t *testing.T) {
	f := newCourierFixture(t)
	ctx := context.Background()
	if _, err := f.svc.Start(ctx, partner, "cou_01j9z3k8m2q4r6s8t0v2w4x6y9", "sdk", 0); !errors.Is(err, app.ErrCourierNotFound) {
		t.Fatalf("unknown courier: %v", err)
	}
	live := partner
	live.Env = domain.EnvLive
	if _, err := f.svc.Start(ctx, live, cou1, "sdk", 0); !errors.Is(err, app.ErrCourierNotFound) {
		t.Fatalf("courier of the other environment: %v", err)
	}
	if _, err := f.svc.Start(ctx, partner, cou1, "carrier-pigeon", 0); !errors.Is(err, app.ErrSessionMode) {
		t.Fatalf("bad mode: %v", err)
	}
	if _, err := f.svc.Start(ctx, partner, cou1, "sdk", 899*time.Second); !errors.Is(err, domain.ErrSessionLength) {
		t.Fatalf("short session: %v", err)
	}
	r, err := f.svc.Start(ctx, partner, cou1, "sdk", 900*time.Second)
	if err != nil || !r.Tokens.SessionExpiresAt.Equal(csNow.Add(15*time.Minute)) {
		t.Fatalf("15 min session = %+v, %v", r, err)
	}
	// Sessions of another tenant or environment are not found.
	other := partner
	other.TenantID = "ten_b"
	if _, err := f.svc.Get(ctx, other, r.Session.ID); !errors.Is(err, app.ErrCourierSessionNotFound) {
		t.Fatalf("get from other tenant: %v", err)
	}
	if _, err := f.svc.End(ctx, live, r.Session.ID, "partner"); !errors.Is(err, app.ErrCourierSessionNotFound) {
		t.Fatalf("end from other env: %v", err)
	}
	if _, err := f.svc.Get(ctx, partner, "cs_nope"); !errors.Is(err, app.ErrCourierSessionNotFound) {
		t.Fatalf("get unknown: %v", err)
	}
}

func TestEventsEnqueued(t *testing.T) {
	f := newCourierFixture(t)
	ctx := context.Background()
	l := f.startLink(t, cou1)
	if ev := f.store.Events(); len(ev) != 0 {
		t.Fatalf("events before exchange = %+v", ev)
	}
	if _, err := f.svc.Exchange(ctx, l.LinkCode, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.EndSession(ctx, l.Session.ID, "courier"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.EndSession(ctx, l.Session.ID, "courier"); err != nil { // idempotent: no second event
		t.Fatal(err)
	}
	want := []apptest.CourierEvent{
		{Type: "courier.session_started", SessionID: l.Session.ID, CourierID: cou1, Mode: domain.ModeLink},
		{Type: "courier.session_ended", SessionID: l.Session.ID, CourierID: cou1, Reason: domain.EndCourier},
	}
	got := f.store.Events()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("events = %+v, want %+v", got, want)
	}
}

func TestTouchCourierSession(t *testing.T) {
	f := newCourierFixture(t)
	ctx := context.Background()
	r := f.startSDK(t, cou1)
	if err := f.svc.Touch(ctx, r.Session.ID, csNow); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Touch(ctx, r.Session.ID, csNow.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if s := f.get(t, r.Session.ID); s.LastSeenAt == nil || !s.LastSeenAt.Equal(csNow) {
		t.Fatalf("last seen = %v", s.LastSeenAt)
	}
	if err := f.svc.Touch(ctx, r.Session.ID, csNow.Add(61*time.Second)); err != nil {
		t.Fatal(err)
	}
	if s := f.get(t, r.Session.ID); !s.LastSeenAt.Equal(csNow.Add(61 * time.Second)) {
		t.Fatalf("last seen = %v", s.LastSeenAt)
	}
}

func TestGetCourierSessionShowsExpiry(t *testing.T) {
	f := newCourierFixture(t)
	r := f.startSDK(t, cou1)
	f.clock.Advance(13 * time.Hour)
	s := f.get(t, r.Session.ID)
	if s.State != domain.SessionEnded || s.EndReason != domain.EndExpired || !s.EndedAt.Equal(csNow.Add(12*time.Hour)) {
		t.Fatalf("expired session = %+v", s)
	}
	// Ending it later keeps "expired".
	e, err := f.svc.End(context.Background(), partner, r.Session.ID, "partner")
	if err != nil || e.EndReason != domain.EndExpired {
		t.Fatalf("end of expired = %+v, %v", e, err)
	}
	// And a new session for the courier is not blocked by it.
	f.startSDK(t, cou1)
}

func TestAccessTokenEndsWithSession(t *testing.T) {
	f := newCourierFixture(t)
	ctx := context.Background()
	r, err := f.svc.Start(ctx, partner, cou1, "sdk", 900*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// Refresh one minute before the session expires: the access token is
	// capped at the session's expiry, not now + 15 min.
	f.clock.Advance(14 * time.Minute)
	tok, err := f.svc.Refresh(ctx, r.Tokens.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	end := csNow.Add(15 * time.Minute)
	if !tok.AccessExpiresAt.Equal(end) || !tok.SessionExpiresAt.Equal(end) {
		t.Fatalf("access_expires_at = %v, session_expires_at = %v; want both %v", tok.AccessExpiresAt, tok.SessionExpiresAt, end)
	}
	if _, _, err := f.svc.Authenticate(ctx, tok.AccessToken); err != nil {
		t.Fatalf("before expiry: %v", err)
	}
	f.clock.Advance(time.Minute + courierjwt.Leeway + time.Second)
	if _, _, err := f.svc.Authenticate(ctx, tok.AccessToken); !errors.Is(err, app.ErrUnauthenticated) {
		t.Fatalf("after session expiry: %v", err)
	}
}

func TestExchangeAuditsStart(t *testing.T) {
	f := newCourierFixture(t)
	f.svc.RequestID = func(context.Context) string { return "req_1" }
	l := f.startLink(t, cou1)
	if _, err := f.svc.Exchange(context.Background(), l.LinkCode, "203.0.113.9"); err != nil {
		t.Fatal(err)
	}
	e := f.audit.Entries()
	if len(e) != 1 || e[0].ActorType != "courier" || e[0].ActorID != l.Session.ID || e[0].Action != "courier_sessions.start" ||
		e[0].Target != l.Session.ID || e[0].TenantID != partner.TenantID || e[0].Env != partner.Env ||
		e[0].IP.String() != "203.0.113.9" || e[0].RequestID != "req_1" || e[0].Status != 0 {
		t.Fatalf("audit = %+v", e)
	}
}

func TestRefreshSessionNamesCurrentAndSupersededTokens(t *testing.T) {
	f := newCourierFixture(t)
	ctx := context.Background()
	r := f.startSDK(t, cou1)
	t2, err := f.svc.Refresh(ctx, r.Tokens.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	for name, tok := range map[string]string{"current": t2.RefreshToken, "superseded": r.Tokens.RefreshToken} {
		if id, err := f.svc.RefreshSession(ctx, tok); err != nil || id != r.Session.ID {
			t.Errorf("%s: id = %q, err = %v, want %q", name, id, err, r.Session.ID)
		}
	}
	// It changes nothing: the current token still rotates once.
	if _, err := f.svc.Refresh(ctx, t2.RefreshToken); err != nil {
		t.Fatalf("refresh after lookup: %v", err)
	}
	for _, bad := range []string{strings.Repeat("A", 43), "short"} {
		if _, err := f.svc.RefreshSession(ctx, bad); !errors.Is(err, app.ErrRefreshInvalid) {
			t.Errorf("RefreshSession(%q) = %v, want ErrRefreshInvalid", bad, err)
		}
	}
}
