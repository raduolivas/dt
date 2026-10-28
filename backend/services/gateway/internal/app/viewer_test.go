package app_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/raduolivas/dt/backend/pkg/viewerjwt"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

type snapshots struct {
	err  error
	last domain.TrackingLink
}

func (s *snapshots) Snapshot(_ context.Context, l domain.TrackingLink) (json.RawMessage, error) {
	s.last = l
	if s.err != nil {
		return nil, s.err
	}
	return json.RawMessage(`{"delivery_id":"` + l.DeliveryID + `","version":1}`), nil
}

// countLimiter allows burst requests per key, then denies.
type countLimiter struct {
	n    map[string]int
	keys []string
}

func (c *countLimiter) Allow(_ context.Context, key string, l app.Limit) (app.Decision, error) {
	c.keys = append(c.keys, key)
	c.n[key]++
	if c.n[key] > l.Burst {
		return app.Decision{Limit: l.Burst, RetryAfter: 2 * time.Second}, nil
	}
	return app.Decision{Allowed: true, Limit: l.Burst, Remaining: l.Burst - c.n[key]}, nil
}

func newSigner(t *testing.T) *viewerjwt.Signer {
	t.Helper()
	seed := make([]byte, 32)
	_, _ = rand.Read(seed)
	s, err := viewerjwt.ParseKeys("v1:"+base64.StdEncoding.EncodeToString(seed), "v1")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type viewerFixture struct {
	v     *app.Viewer
	links *app.TrackingLinks
	snaps *snapshots
	lim   *countLimiter
	clock *clock
}

func newViewer(t *testing.T) *viewerFixture {
	t.Helper()
	tl, _, _, c := links()
	s := newSigner(t)
	f := &viewerFixture{links: tl, snaps: &snapshots{}, lim: &countLimiter{n: map[string]int{}}, clock: c}
	f.v = &app.Viewer{Links: tl, Tokens: app.SignerTokens{Signer: s, Verifier: s.Verifier()}, Snapshots: f.snaps, Limiter: f.lim}
	return f
}

func TestViewerStartBindsTokenToLink(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newViewer(t)
	l, tok, err := f.links.Issue(ctx, principal, delA)
	if err != nil {
		t.Fatal(err)
	}
	jwt, exp, snap, err := f.v.Start(ctx, tok, t0)
	if err != nil {
		t.Fatal(err)
	}
	if !exp.Equal(t0.Add(15*time.Minute)) || string(snap) != `{"delivery_id":"`+delA+`","version":1}` {
		t.Fatalf("exp %v snap %s", exp, snap)
	}
	if f.snaps.last.ID != l.ID {
		t.Fatalf("snapshot fetched for %+v", f.snaps.last)
	}
	c, err := f.v.Tokens.Verify(ctx, jwt, t0)
	if err != nil {
		t.Fatal(err)
	}
	if c != (viewerjwt.Claims{DeliveryID: delA, TenantID: "ten_a", Env: "test", LinkID: l.ID}) {
		t.Fatalf("claims = %+v", c)
	}
	if want := "rl:viewer:tl:" + l.ID; len(f.lim.keys) != 1 || f.lim.keys[0] != want {
		t.Fatalf("limited keys = %v, want [%s]", f.lim.keys, want)
	}
}

func TestViewerStartErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newViewer(t)
	_, tok, _ := f.links.Issue(ctx, principal, delA)

	if _, _, _, err := f.v.Start(ctx, "Xk3m9QpL2vN7aB4cD8eF1g", t0); !errors.Is(err, app.ErrLinkInvalid) {
		t.Fatalf("unknown token: %v", err)
	}
	f.snaps.err = errors.New("tracking down")
	if _, _, _, err := f.v.Start(ctx, tok, t0); !errors.Is(err, app.ErrTrackingUnavailable) {
		t.Fatalf("tracking down: %v", err)
	}
	f.snaps.err = nil
	// 30 per link per minute (the two calls above count too).
	var rl *app.RateLimitedError
	for i := range 40 {
		_, _, _, err := f.v.Start(ctx, tok, t0)
		if errors.As(err, &rl) {
			if i+2 != app.ViewerLink.Burst+1 {
				t.Fatalf("limited at call %d", i+2)
			}
			if rl.Decision.RetryAfter <= 0 {
				t.Fatalf("decision = %+v", rl.Decision)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("never rate limited")
}

func TestRefreshKeepsLinkAndFailsAfterRotationOrExpiry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newViewer(t)
	l, tok, _ := f.links.Issue(ctx, principal, delA)
	jwt, _, _, err := f.v.Start(ctx, tok, t0)
	if err != nil {
		t.Fatal(err)
	}
	later := t0.Add(10 * time.Minute)
	jwt2, exp, err := f.v.Refresh(ctx, jwt, later)
	if err != nil {
		t.Fatal(err)
	}
	if !exp.Equal(later.Add(15 * time.Minute)) {
		t.Fatalf("exp = %v", exp)
	}
	c, _ := f.v.Tokens.Verify(ctx, jwt2, later)
	if c.LinkID != l.ID || c.DeliveryID != delA {
		t.Fatalf("refreshed claims = %+v", c)
	}

	// Garbage and expired tokens.
	for name, tk := range map[string]string{"garbage": "x.y.z", "empty": ""} {
		if _, _, err := f.v.Refresh(ctx, tk, later); !errors.Is(err, app.ErrViewerTokenInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, _, err := f.v.Refresh(ctx, jwt, t0.Add(16*time.Minute)); !errors.Is(err, app.ErrViewerTokenInvalid) {
		t.Fatalf("expired jwt: %v", err)
	}

	// Rotation kills refresh of tokens bound to the old link.
	if _, _, err := f.links.Rotate(ctx, principal, delA); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.v.Refresh(ctx, jwt2, later); !errors.Is(err, app.ErrViewerTokenInvalid) {
		t.Fatalf("refresh after rotation: %v", err)
	}

	// Expiry too.
	f2 := newViewer(t)
	_, tok3, _ := f2.links.Issue(ctx, principal, delA)
	jwt3, _, _, _ := f2.v.Start(ctx, tok3, t0)
	if err := f2.links.ExpireOnFinal(ctx, delA, t0.Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f2.v.Refresh(ctx, jwt3, t0); !errors.Is(err, app.ErrViewerTokenInvalid) {
		t.Fatalf("refresh after expiry: %v", err)
	}
}
