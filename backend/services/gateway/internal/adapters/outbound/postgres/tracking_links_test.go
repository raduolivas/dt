package postgres_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/raduolivas/dt/backend/pkg/apikey"
	"github.com/raduolivas/dt/backend/services/gateway/internal/adapters/outbound/keyhash"
	"github.com/raduolivas/dt/backend/services/gateway/internal/adapters/outbound/postgres"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app/apptest"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

const linkDel = "del_01j9z3k8m2q4r6s8t0v2w4x6y8"

var linkOwner = domain.Principal{TenantID: "ten_a", Env: domain.EnvLive, CredentialID: "ak_1"}

type fixedNow struct{ t time.Time }

func (c *fixedNow) Now() time.Time { return c.t }

func linkService(t *testing.T) (*app.TrackingLinks, *postgres.TrackingLinkStore, *apptest.MemRevoker, *fixedNow) {
	t.Helper()
	pool, _ := setup(t)
	s := postgres.NewTrackingLinkStore(pool)
	rev, c := &apptest.MemRevoker{}, &fixedNow{t: t0}
	return &app.TrackingLinks{Store: s, Hasher: apptest.Hasher{}, Revoker: rev, Expiries: &apptest.MemExpiries{}, Clock: c}, s, rev, c
}

func TestLinkTokenStoredHashed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, _ := setup(t)
	tl := &app.TrackingLinks{Store: postgres.NewTrackingLinkStore(pool), Hasher: apptest.Hasher{}, Revoker: &apptest.MemRevoker{}, Expiries: &apptest.MemExpiries{}, Clock: &fixedNow{t: t0}}
	l, tok, err := tl.Issue(ctx, linkOwner, linkDel)
	if err != nil {
		t.Fatal(err)
	}
	// Read the whole row back as text: the token appears nowhere.
	var row string
	if err := pool.QueryRow(ctx, `SELECT t::text FROM tracking_links t WHERE id = $1`, l.ID).Scan(&row); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains([]byte(row), []byte(tok)) {
		t.Fatalf("plaintext token stored: %s", row)
	}
	var hash []byte
	var env string
	if err := pool.QueryRow(ctx, `SELECT token_hash, environment FROM tracking_links WHERE id = $1`, l.ID).Scan(&hash, &env); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(hash, apikey.Hash(apptest.TestPepper, tok)) || env != "live" {
		t.Fatalf("token_hash/environment = %x/%s", hash, env)
	}
	got, err := tl.Resolve(ctx, tok, t0)
	if err != nil || got.ID != l.ID || got.TenantID != "ten_a" || got.Env != domain.EnvLive || got.DeliveryID != linkDel ||
		!got.CreatedAt.Equal(t0) {
		t.Fatalf("resolve = %+v, %v", got, err)
	}
}

func TestReplaceKeepsOneUnrevokedLink(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tl, s, rev, _ := linkService(t)
	l1, tok1, _ := tl.Issue(ctx, linkOwner, linkDel)
	// Concurrent rotations still leave exactly one usable link.
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, _, err := tl.Rotate(ctx, linkOwner, linkDel); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if _, err := tl.Resolve(ctx, tok1, t0); !errors.Is(err, app.ErrLinkInvalid) {
		t.Fatalf("old token: %v", err)
	}
	old, err := s.Get(ctx, l1.ID)
	if err != nil || old.RevokedAt == nil {
		t.Fatalf("old link = %+v, %v", old, err)
	}
	ids, err := s.RevokedSince(ctx, linkDel, t0.Add(-time.Minute))
	if err != nil || len(ids) != 8 {
		t.Fatalf("revoked = %v, %v", ids, err)
	}
	if len(rev.Revoked()) < 8 {
		t.Fatalf("published %d revocations", len(rev.Revoked()))
	}
	if _, err := s.Get(ctx, "tl_missing"); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestExpireOnFinalSets30Min(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tl, s, _, c := linkService(t)
	l, tok, _ := tl.Issue(ctx, linkOwner, linkDel)
	final := t0.Add(time.Hour)
	if err := tl.ExpireOnFinal(ctx, linkDel, final); err != nil {
		t.Fatal(err)
	}
	// Redelivered later: the first final time wins.
	if err := tl.ExpireOnFinal(ctx, linkDel, final.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, l.ID)
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(final.Add(30*time.Minute)) {
		t.Fatalf("expires_at = %v", got.ExpiresAt)
	}
	if _, err := tl.Resolve(ctx, tok, final.Add(30*time.Minute-time.Second)); err != nil {
		t.Fatalf("just before expiry: %v", err)
	}
	if _, err := tl.Resolve(ctx, tok, final.Add(30*time.Minute)); !errors.Is(err, app.ErrLinkInvalid) {
		t.Fatalf("at expiry: %v", err)
	}
	// The store answers a redelivery with the recorded expiry and every
	// unrevoked link carrying it, so a failed announcement can be retried.
	exp, ids, err := s.Expire(ctx, linkDel, final.Add(2*time.Hour))
	if err != nil || !exp.Equal(final.Add(30*time.Minute)) || len(ids) != 1 || ids[0] != l.ID {
		t.Fatalf("redelivered Expire = %v %v %v", exp, ids, err)
	}
	// A link rotated after the delivery is final keeps the expiry.
	c.t = final.Add(time.Minute)
	l2, _, err := tl.Rotate(ctx, linkOwner, linkDel)
	if err != nil || l2.ExpiresAt == nil || !l2.ExpiresAt.Equal(final.Add(30*time.Minute)) {
		t.Fatalf("rotated after final: %+v, %v", l2, err)
	}
	// Final with no link yet: a link issued later takes the expiry.
	const other = "del_01j9z3k8m2q4r6s8t0v2w4x6z9"
	if err := tl.ExpireOnFinal(ctx, other, final); err != nil {
		t.Fatal(err)
	}
	l3, _, err := tl.Rotate(ctx, linkOwner, other)
	if err != nil || l3.ExpiresAt == nil || !l3.ExpiresAt.Equal(final.Add(30*time.Minute)) {
		t.Fatalf("issued after final: %+v, %v", l3, err)
	}
}

func TestLinkTokenHashIsPeppered(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, _ := setup(t)
	store := postgres.NewTrackingLinkStore(pool)
	pepperA := []byte("test-only-pepper-A-0123456789abcdef")
	pepperB := []byte("test-only-pepper-B-0123456789abcdef")
	a := &app.TrackingLinks{Store: store, Hasher: keyhash.New(pepperA), Revoker: &apptest.MemRevoker{}, Expiries: &apptest.MemExpiries{}, Clock: &fixedNow{t: t0}}
	b := &app.TrackingLinks{Store: store, Hasher: keyhash.New(pepperB), Revoker: &apptest.MemRevoker{}, Expiries: &apptest.MemExpiries{}, Clock: &fixedNow{t: t0}}

	l, tok, err := a.Issue(ctx, linkOwner, linkDel)
	if err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := pool.QueryRow(ctx, `SELECT token_hash FROM tracking_links WHERE id = $1`, l.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	plain := sha256.Sum256([]byte(tok))
	if bytes.Equal(stored, plain[:]) {
		t.Fatal("stored hash is the unkeyed SHA-256 of the token")
	}
	if !bytes.Equal(stored, apikey.Hash(pepperA, tok)) || bytes.Equal(apikey.Hash(pepperA, tok), apikey.Hash(pepperB, tok)) {
		t.Fatal("stored hash is not HMAC-SHA256 under the pepper")
	}
	// The same token under another pepper resolves to nothing.
	if _, err := a.Resolve(ctx, tok, t0); err != nil {
		t.Fatalf("own pepper: %v", err)
	}
	if _, err := b.Resolve(ctx, tok, t0); !errors.Is(err, app.ErrLinkInvalid) {
		t.Fatalf("other pepper: %v", err)
	}
}
