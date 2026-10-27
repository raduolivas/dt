package app_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app/apptest"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

var (
	t0        = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	principal = domain.Principal{TenantID: "ten_a", Env: domain.EnvTest, CredentialID: "ak_1",
		Credential: domain.CredentialSecretKey}
)

const delA = "del_01j9z3k8m2q4r6s8t0v2w4x6y8"

type clock struct{ t time.Time }

func (c *clock) Now() time.Time { return c.t }

func links() (*app.TrackingLinks, *apptest.MemLinks, *apptest.MemRevoker, *clock) {
	store, rev, c := apptest.NewMemLinks(), &apptest.MemRevoker{}, &clock{t: t0}
	return &app.TrackingLinks{Store: store, Revoker: rev, Clock: c}, store, rev, c
}

func TestIssueStoresOnlyTheHash(t *testing.T) {
	t.Parallel()
	tl, store, _, _ := links()
	l, tok, err := tl.Issue(context.Background(), principal, delA)
	if err != nil {
		t.Fatal(err)
	}
	if !domain.ValidLinkToken(tok) || l.DeliveryID != delA || l.TenantID != "ten_a" || l.Env != domain.EnvTest ||
		l.ExpiresAt != nil || l.RevokedAt != nil || !l.CreatedAt.Equal(t0) || len(l.ID) < 4 || l.ID[:3] != "tl_" {
		t.Fatalf("link = %+v, token %q", l, tok)
	}
	h := sha256.Sum256([]byte(tok))
	if got := store.Hash(l.ID); string(got) != string(h[:]) {
		t.Fatal("stored hash is not SHA-256 of the token")
	}
}

func TestResolveRejectsRevokedAndExpired(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tl, _, _, c := links()
	l1, tok1, err := tl.Issue(ctx, principal, delA)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tl.Resolve(ctx, tok1, t0)
	if err != nil || got.ID != l1.ID {
		t.Fatalf("resolve fresh link: %+v, %v", got, err)
	}

	// Rotated: the old token is dead at once, the new one works.
	_, tok2, err := tl.Rotate(ctx, principal, delA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tl.Resolve(ctx, tok1, t0); !errors.Is(err, app.ErrLinkInvalid) {
		t.Fatalf("revoked link: %v", err)
	}
	if _, err := tl.Resolve(ctx, tok2, t0); err != nil {
		t.Fatalf("rotated link: %v", err)
	}

	// Final: valid for 30 minutes, then expired.
	if err := tl.ExpireOnFinal(ctx, delA, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := tl.Resolve(ctx, tok2, t0.Add(29*time.Minute)); err != nil {
		t.Fatalf("within 30 min of final: %v", err)
	}
	c.t = t0.Add(30 * time.Minute)
	if _, err := tl.Resolve(ctx, tok2, c.t); !errors.Is(err, app.ErrLinkInvalid) {
		t.Fatalf("expired link: %v", err)
	}

	// Unknown and malformed tokens look the same as revoked ones.
	other, _ := domain.NewLinkToken()
	for _, tok := range []string{other, "", "not-a-token", "dt_sk_test_aaaaaaaaaaaaaaaaaaaaaaaa"} {
		if _, err := tl.Resolve(ctx, tok, t0); !errors.Is(err, app.ErrLinkInvalid) {
			t.Errorf("token %q: %v", tok, err)
		}
	}
}

func TestRotateRevokesOldAndPublishes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tl, _, rev, _ := links()
	l1, _, err := tl.Issue(ctx, principal, delA)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(rev.Revoked()); n != 0 {
		t.Fatalf("issue revoked %d links", n)
	}
	l2, _, err := tl.Rotate(ctx, principal, delA)
	if err != nil {
		t.Fatal(err)
	}
	if l2.ID == l1.ID {
		t.Fatal("rotation kept the link id")
	}
	if got := rev.Revoked(); !slices.Equal(got, []string{l1.ID}) {
		t.Fatalf("revoked = %v, want [%s]", got, l1.ID)
	}

	// A failed revocation fails the rotation (the caller retries), and the
	// retry publishes every link revoked within a token's lifetime, so the
	// one whose revocation was lost is cut off too.
	rev.Fail(errors.New("redis down"))
	if _, _, err := tl.Rotate(ctx, principal, delA); err == nil {
		t.Fatal("rotation succeeded without publishing the revocation")
	}
	rev.Fail(nil)
	rev.Reset()
	l4, _, err := tl.Rotate(ctx, principal, delA)
	if err != nil {
		t.Fatal(err)
	}
	got := rev.Revoked()
	if len(got) != 3 || !slices.Contains(got, l1.ID) || !slices.Contains(got, l2.ID) || slices.Contains(got, l4.ID) {
		t.Fatalf("revoked on retry = %v", got)
	}
}

func TestRotateIssuesWhenNoLinkAndKeepsExpiry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tl, _, rev, _ := links()
	l, tok, err := tl.Rotate(ctx, principal, delA)
	if err != nil || tok == "" || l.RevokedAt != nil {
		t.Fatalf("rotate without a link: %+v %v", l, err)
	}
	if n := len(rev.Revoked()); n != 0 {
		t.Fatalf("revoked %d", n)
	}
	// After the delivery is final, a rotated link keeps the delivery's expiry.
	if err := tl.ExpireOnFinal(ctx, delA, t0); err != nil {
		t.Fatal(err)
	}
	l2, _, err := tl.Rotate(ctx, principal, delA)
	if err != nil {
		t.Fatal(err)
	}
	if l2.ExpiresAt == nil || !l2.ExpiresAt.Equal(t0.Add(30*time.Minute)) {
		t.Fatalf("rotated after final: expires_at = %v", l2.ExpiresAt)
	}
}

func TestExpireOnFinalFirstFinalWins(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tl, store, _, _ := links()
	l, _, _ := tl.Issue(ctx, principal, delA)
	if err := tl.ExpireOnFinal(ctx, delA, t0); err != nil {
		t.Fatal(err)
	}
	// A redelivered event later must not extend the link.
	if err := tl.ExpireOnFinal(ctx, delA, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, l.ID)
	if err != nil || got.ExpiresAt == nil || !got.ExpiresAt.Equal(t0.Add(30*time.Minute)) {
		t.Fatalf("expires_at = %v, %v", got.ExpiresAt, err)
	}
}
