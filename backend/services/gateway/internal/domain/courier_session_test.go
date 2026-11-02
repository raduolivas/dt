package domain

import (
	"errors"
	"testing"
	"time"
)

var csT0 = time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)

func activeSession(t *testing.T) CourierSession {
	t.Helper()
	s := CourierSession{ID: "cs_1", TenantID: "ten_a", Env: EnvTest, CourierID: "cou_1",
		Mode: ModeSDK, State: SessionPendingLink, CreatedAt: csT0, ExpiresIn: time.Hour}
	s.Activate(csT0)
	return s
}

func TestActivateSetsExpiry(t *testing.T) {
	s := activeSession(t)
	if s.State != SessionActive || s.StartedAt == nil || !s.StartedAt.Equal(csT0) ||
		s.ExpiresAt == nil || !s.ExpiresAt.Equal(csT0.Add(time.Hour)) {
		t.Fatalf("activated = %+v", s)
	}
	if !s.IsUsable(csT0.Add(time.Hour-time.Second)) || s.Status(csT0) != SessionActive {
		t.Fatal("an active session before its expiry is usable")
	}
}

func TestStatusExpiredReadsEnded(t *testing.T) {
	s := activeSession(t)
	at := csT0.Add(time.Hour) // a session expiring at now is over
	if s.IsUsable(at) || s.Status(at) != SessionEnded {
		t.Fatalf("expired session: usable=%v status=%s", s.IsUsable(at), s.Status(at))
	}
	v := s.View(at.Add(time.Minute))
	if v.State != SessionEnded || v.EndReason != EndExpired || v.EndedAt == nil || !v.EndedAt.Equal(at) {
		t.Fatalf("view = %+v", v)
	}
	if s.State != SessionActive {
		t.Fatal("View must not change the session")
	}

	// A pending link that was never exchanged reads as expired once its code lapses.
	exp := csT0.Add(LinkCodeTTL)
	p := CourierSession{ID: "cs_2", Mode: ModeLink, State: SessionPendingLink, CreatedAt: csT0,
		ExpiresIn: time.Hour, LinkExpiresAt: &exp}
	if p.Status(exp.Add(-time.Second)) != SessionPendingLink || p.Status(exp) != SessionEnded {
		t.Fatalf("pending link status = %s / %s", p.Status(exp.Add(-time.Second)), p.Status(exp))
	}
	if p.IsUsable(csT0) {
		t.Fatal("a pending link is not usable")
	}
}

func TestEndIsIdempotent(t *testing.T) {
	s := activeSession(t)
	if !s.End(EndPartner, csT0.Add(time.Minute)) {
		t.Fatal("first End must end the session")
	}
	if s.End(EndCourier, csT0.Add(2*time.Minute)) {
		t.Fatal("second End must change nothing")
	}
	if s.State != SessionEnded || s.EndReason != EndPartner || !s.EndedAt.Equal(csT0.Add(time.Minute)) {
		t.Fatalf("ended = %+v", s)
	}
	if s.IsUsable(csT0.Add(time.Minute)) {
		t.Fatal("an ended session is not usable")
	}

	// Ending a session that already expired records the expiry, not the reason.
	e := activeSession(t)
	if !e.End(EndReplaced, csT0.Add(2*time.Hour)) || e.EndReason != EndExpired ||
		!e.EndedAt.Equal(csT0.Add(time.Hour)) {
		t.Fatalf("ended after expiry = %+v", e)
	}
}

func TestExpiresInBounds(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want time.Duration
		ok   bool
	}{
		{0, 43200 * time.Second, true},
		{899 * time.Second, 0, false},
		{900 * time.Second, 900 * time.Second, true},
		{43200 * time.Second, 43200 * time.Second, true},
		{43201 * time.Second, 0, false},
		{-time.Second, 0, false},
		{900*time.Second + time.Millisecond, 0, false},
	} {
		got, err := SessionLength(tc.in)
		if tc.ok && (err != nil || got != tc.want) {
			t.Errorf("SessionLength(%v) = %v, %v; want %v", tc.in, got, err, tc.want)
		}
		if !tc.ok && !errors.Is(err, ErrSessionLength) {
			t.Errorf("SessionLength(%v) = %v, %v; want ErrSessionLength", tc.in, got, err)
		}
	}
}

func TestCourierSecretsShape(t *testing.T) {
	code, err := NewCourierLinkCode()
	if err != nil || len(code) != 22 || !ValidCourierLinkCode(code) {
		t.Fatalf("link code = %q, %v", code, err)
	}
	ref, err := NewCourierRefreshToken()
	if err != nil || len(ref) != 43 || !ValidCourierRefreshToken(ref) {
		t.Fatalf("refresh token = %q, %v", ref, err)
	}
	if ValidCourierLinkCode(ref) || ValidCourierRefreshToken(code) || ValidCourierLinkCode("x") {
		t.Fatal("shape check accepts the wrong kind")
	}
}
