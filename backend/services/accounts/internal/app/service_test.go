package app_test

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/services/accounts/internal/app"
	"github.com/raduolivas/dt/backend/services/accounts/internal/domain"
)

var now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

var (
	owner   = domain.Actor{Type: "console_user", ID: "u_owner", Role: domain.RoleOwner}
	dev     = domain.Actor{Type: "console_user", ID: "u_dev", Role: domain.RoleDeveloper}
	support = domain.Actor{Type: "console_user", ID: "u_sup", Role: domain.RoleSupport}
)

type env struct {
	repo *fakeRepo
	svc  *app.Service
	clk  *fixedClock
	ten  domain.Tenant
}

func setup(t *testing.T, live bool) *env {
	t.Helper()
	repo := newFakeRepo()
	clk := &fixedClock{now}
	svc := &app.Service{Repo: repo, Keys: &fakeKeys{}, Clock: clk}
	ten, err := svc.CreateTenant(context.Background(), "Acme")
	if err != nil {
		t.Fatal(err)
	}
	if live {
		if err := svc.SetLiveEnabled(context.Background(), ten.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	return &env{repo, svc, clk, ten}
}

func (e *env) create(t *testing.T, a domain.Actor, mode domain.Environment) (domain.APIKey, string) {
	t.Helper()
	k, plain, err := e.svc.CreateKey(context.Background(), app.CreateKeyInput{
		Actor: a, TenantID: e.ten.ID, Name: "server", Env: mode,
	})
	if err != nil {
		t.Fatal(err)
	}
	return k, plain
}

func (e *env) eventTypes() []string {
	var out []string
	for _, ev := range e.repo.events {
		out = append(out, ev.Type)
	}
	return out
}

func TestCreateTenantAndLiveFlag(t *testing.T) {
	e := setup(t, false)
	if !strings.HasPrefix(e.ten.ID, "ten_") || e.ten.Name != "Acme" || e.ten.LiveEnabled || !e.ten.CreatedAt.Equal(now) {
		t.Fatalf("tenant: %+v", e.ten)
	}
	if err := e.svc.SetLiveEnabled(context.Background(), e.ten.ID, true); err != nil {
		t.Fatal(err)
	}
	if !e.repo.tenants[e.ten.ID].LiveEnabled {
		t.Fatal("live flag not set")
	}
	if err := e.svc.SetLiveEnabled(context.Background(), "ten_missing", true); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
	for _, name := range []string{"", "   "} {
		if _, err := e.svc.CreateTenant(context.Background(), name); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("name %q: err=%v", name, err)
		}
	}
}

func TestCreateLiveKeyNeedsLiveEnabled(t *testing.T) {
	e := setup(t, false)
	_, _, err := e.svc.CreateKey(context.Background(), app.CreateKeyInput{
		Actor: owner, TenantID: e.ten.ID, Name: "k", Env: domain.EnvLive,
	})
	if !errors.Is(err, domain.ErrLiveNotEnabled) {
		t.Fatalf("err=%v", err)
	}
	if len(e.repo.events) != 0 || len(e.repo.keys) != 0 {
		t.Fatalf("events=%v keys=%d", e.eventTypes(), len(e.repo.keys))
	}
	// After enabling, it works.
	if err := e.svc.SetLiveEnabled(context.Background(), e.ten.ID, true); err != nil {
		t.Fatal(err)
	}
	e.create(t, owner, domain.EnvLive)
}

func TestCreateKeyPublishesCreatedWithHashNotPlaintext(t *testing.T) {
	e := setup(t, false)
	k, plain := e.create(t, dev, domain.EnvTest)
	if !strings.HasPrefix(k.ID, "ak_") || k.TenantID != e.ten.ID || k.CreatedBy != dev || !k.CreatedAt.Equal(now) {
		t.Fatalf("key: %+v", k)
	}
	if got := e.repo.keys[k.ID]; !bytes.Equal(got.Hash, k.Hash) {
		t.Fatal("stored hash differs")
	}
	if len(e.repo.events) != 1 || e.repo.events[0].Type != "apikey.created" {
		t.Fatalf("events=%v", e.eventTypes())
	}
	ev := e.repo.events[0]
	if ev.TenantID != e.ten.ID || ev.Env != domain.EnvTest {
		t.Fatalf("event meta: %+v", ev)
	}
	p, ok := ev.Payload.(*eventsv1.ApiKeyCreated)
	if !ok {
		t.Fatalf("payload %T", ev.Payload)
	}
	if !bytes.Equal(p.Hash, k.Hash) || p.KeyId != k.ID || p.TenantId != e.ten.ID || p.Lookup != k.Lookup ||
		p.Environment != eventsv1.Environment_ENVIRONMENT_TEST || !p.CreatedAt.AsTime().Equal(now) {
		t.Fatalf("payload: %v", p)
	}
	raw, err := proto.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(plain)) || bytes.Contains(raw, []byte("SECRET-PLAINTEXT")) {
		t.Fatal("plaintext leaked into event")
	}
	if len(p.Scopes) != len(domain.AllScopes) {
		t.Fatalf("default scopes: %v", p.Scopes)
	}
}

func TestCreateKeyAuthorization(t *testing.T) {
	e := setup(t, true)
	tests := []struct {
		name  string
		actor domain.Actor
		env   domain.Environment
		want  error
	}{
		{"support cannot create test", support, domain.EnvTest, domain.ErrForbiddenRole},
		{"developer cannot create live", dev, domain.EnvLive, domain.ErrForbiddenRole},
		{"unknown env", owner, domain.Environment("x"), domain.ErrInvalidArgument},
		{"owner live ok", owner, domain.EnvLive, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := e.svc.CreateKey(context.Background(), app.CreateKeyInput{
				Actor: tc.actor, TenantID: e.ten.ID, Name: "k", Env: tc.env,
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want %v", err, tc.want)
			}
		})
	}
}

func TestCreateKeyUnknownTenant(t *testing.T) {
	e := setup(t, false)
	_, _, err := e.svc.CreateKey(context.Background(), app.CreateKeyInput{
		Actor: owner, TenantID: "ten_nope", Name: "k", Env: domain.EnvTest,
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestValidateScopesAndCIDRs(t *testing.T) {
	tests := []struct {
		name   string
		scopes []string
		ips    []string
		want   error
		wantIP []netip.Prefix
	}{
		{"unknown scope", []string{"nope:all"}, nil, domain.ErrInvalidScope, nil},
		{"cidr and host", nil, []string{"10.0.0.0/8", "203.0.113.7"}, nil,
			[]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("203.0.113.7/32")}},
		{"ipv6 host", nil, []string{"2001:db8::1"}, nil, []netip.Prefix{netip.MustParsePrefix("2001:db8::1/128")}},
		{"masked and deduped", nil, []string{"10.1.2.3/8", "10.0.0.0/8"}, nil, []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}},
		{"garbage", nil, []string{"nope"}, domain.ErrInvalidArgument, nil},
		{"bad mask", nil, []string{"10.0.0.0/99"}, domain.ErrInvalidArgument, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t, false)
			k, _, err := e.svc.CreateKey(context.Background(), app.CreateKeyInput{
				Actor: owner, TenantID: e.ten.ID, Name: "k", Env: domain.EnvTest,
				Scopes: tc.scopes, AllowedIPs: tc.ips,
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want %v", err, tc.want)
			}
			if err != nil {
				if len(e.repo.events) != 0 {
					t.Fatal("event published on failure")
				}
				return
			}
			if len(k.AllowedIPs) != len(tc.wantIP) {
				t.Fatalf("ips=%v", k.AllowedIPs)
			}
			for i := range tc.wantIP {
				if k.AllowedIPs[i] != tc.wantIP[i] {
					t.Fatalf("ips=%v want %v", k.AllowedIPs, tc.wantIP)
				}
			}
			p := e.repo.events[0].Payload.(*eventsv1.ApiKeyCreated)
			if len(p.AllowedIps) != len(tc.wantIP) || (len(tc.wantIP) > 0 && p.AllowedIps[0] != tc.wantIP[0].String()) {
				t.Fatalf("event ips=%v", p.AllowedIps)
			}
		})
	}
}

func TestEmptyNameRejected(t *testing.T) {
	e := setup(t, false)
	for _, name := range []string{"", "  \t"} {
		_, _, err := e.svc.CreateKey(context.Background(), app.CreateKeyInput{
			Actor: owner, TenantID: e.ten.ID, Name: name, Env: domain.EnvTest,
		})
		if !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("name %q: err=%v", name, err)
		}
	}
}

func TestNameOver100CharsRejected(t *testing.T) {
	e := setup(t, false)
	in := app.CreateKeyInput{Actor: owner, TenantID: e.ten.ID, Env: domain.EnvTest}
	in.Name = strings.Repeat("é", 101) // runes, not bytes
	if _, _, err := e.svc.CreateKey(context.Background(), in); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err=%v", err)
	}
	in.Name = strings.Repeat("é", 100)
	if _, _, err := e.svc.CreateKey(context.Background(), in); err != nil {
		t.Fatalf("100 chars should pass: %v", err)
	}
}

func TestRollSetsOverlapAndPublishesTwoEvents(t *testing.T) {
	e := setup(t, false)
	old, _ := e.create(t, owner, domain.EnvTest)
	e.repo.events = nil
	e.repo.locked = nil

	nk, plain, gotOld, err := e.svc.RollKey(context.Background(), dev, e.ten.ID, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := now.Add(domain.RollOverlap)
	if gotOld.ExpiresAt == nil || !gotOld.ExpiresAt.Equal(want) || !e.repo.keys[old.ID].ExpiresAt.Equal(want) {
		t.Fatalf("old expiry: %v", gotOld.ExpiresAt)
	}
	if nk.ID == old.ID || plain == "" || nk.ExpiresAt != nil || nk.Env != old.Env || nk.Name != old.Name || nk.CreatedBy != dev {
		t.Fatalf("new key: %+v", nk)
	}
	if got := e.eventTypes(); len(got) != 2 || got[0] != "apikey.created" || got[1] != "apikey.expiry_set" {
		t.Fatalf("events=%v", got)
	}
	if c := e.repo.events[0].Payload.(*eventsv1.ApiKeyCreated); c.KeyId != nk.ID {
		t.Fatalf("created for %s", c.KeyId)
	}
	x := e.repo.events[1].Payload.(*eventsv1.ApiKeyExpirySet)
	if x.KeyId != old.ID || !x.ExpiresAt.AsTime().Equal(want) {
		t.Fatalf("expiry event: %v", x)
	}
	if len(e.repo.locked) != 1 || e.repo.locked[0] != old.ID {
		t.Fatalf("row lock not taken: %v", e.repo.locked)
	}

	// Rolling an already-expiring key keeps the earlier expiry.
	e.clk.t = now.Add(2 * time.Hour)
	_, _, again, err := e.svc.RollKey(context.Background(), owner, e.ten.ID, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !again.ExpiresAt.Equal(want) {
		t.Fatalf("expiry moved to %v", again.ExpiresAt)
	}
}

func TestRollErrors(t *testing.T) {
	e := setup(t, true)
	rev, _ := e.create(t, owner, domain.EnvTest)
	if _, err := e.svc.RevokeKey(context.Background(), owner, e.ten.ID, rev.ID); err != nil {
		t.Fatal(err)
	}
	live, _ := e.create(t, owner, domain.EnvLive)
	tests := []struct {
		name  string
		actor domain.Actor
		id    string
		want  error
	}{
		{"revoked", owner, rev.ID, domain.ErrKeyRevoked},
		{"developer on live", dev, live.ID, domain.ErrForbiddenRole},
		{"support", support, live.ID, domain.ErrForbiddenRole},
		{"missing", owner, "ak_missing", domain.ErrNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := len(e.repo.events)
			_, _, _, err := e.svc.RollKey(context.Background(), tc.actor, e.ten.ID, tc.id)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want %v", err, tc.want)
			}
			if len(e.repo.events) != before {
				t.Fatal("event published on failure")
			}
		})
	}
}

func TestRollIsAtomicWhenPublishFails(t *testing.T) {
	e := setup(t, false)
	old, _ := e.create(t, owner, domain.EnvTest)
	e.repo.events = nil
	e.repo.failPublishAt = 2 // expiry_set fails
	nKeys := len(e.repo.keys)
	if _, _, _, err := e.svc.RollKey(context.Background(), owner, e.ten.ID, old.ID); err == nil {
		t.Fatal("expected error")
	}
	if len(e.repo.keys) != nKeys || e.repo.keys[old.ID].ExpiresAt != nil || len(e.repo.events) != 0 {
		t.Fatal("partial roll was committed")
	}
}

func TestRevokeIsIdempotent(t *testing.T) {
	e := setup(t, false)
	k, _ := e.create(t, owner, domain.EnvTest)
	e.repo.events = nil

	first, err := e.svc.RevokeKey(context.Background(), dev, e.ten.ID, k.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.RevokedAt == nil || !first.RevokedAt.Equal(now) {
		t.Fatalf("revoked at %v", first.RevokedAt)
	}
	if got := e.eventTypes(); len(got) != 1 || got[0] != "apikey.revoked" {
		t.Fatalf("events=%v", got)
	}
	if p := e.repo.events[0].Payload.(*eventsv1.ApiKeyRevoked); p.KeyId != k.ID || !p.RevokedAt.AsTime().Equal(now) {
		t.Fatalf("payload %v", p)
	}

	e.clk.t = now.Add(time.Hour)
	second, err := e.svc.RevokeKey(context.Background(), dev, e.ten.ID, k.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.repo.events) != 1 {
		t.Fatalf("second revoke published: %v", e.eventTypes())
	}
	if !second.RevokedAt.Equal(*first.RevokedAt) {
		t.Fatalf("RevokedAt changed: %v vs %v", second.RevokedAt, first.RevokedAt)
	}
}

func TestRevokeAuthorization(t *testing.T) {
	e := setup(t, true)
	live, _ := e.create(t, owner, domain.EnvLive)
	for _, a := range []domain.Actor{dev, support} {
		if _, err := e.svc.RevokeKey(context.Background(), a, e.ten.ID, live.ID); !errors.Is(err, domain.ErrForbiddenRole) {
			t.Fatalf("%s: err=%v", a.Role, err)
		}
	}
	if e.repo.keys[live.ID].RevokedAt != nil {
		t.Fatal("revoked despite forbidden")
	}
}

func TestKeyOfOtherTenantIsNotFound(t *testing.T) {
	e := setup(t, false)
	k, _ := e.create(t, owner, domain.EnvTest)
	other, err := e.svc.CreateTenant(context.Background(), "Other")
	if err != nil {
		t.Fatal(err)
	}
	e.repo.events = nil
	if _, _, _, err := e.svc.RollKey(context.Background(), owner, other.ID, k.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("roll err=%v", err)
	}
	if _, err := e.svc.RevokeKey(context.Background(), owner, other.ID, k.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("revoke err=%v", err)
	}
	if len(e.repo.events) != 0 || e.repo.keys[k.ID].RevokedAt != nil {
		t.Fatal("cross-tenant mutation happened")
	}
}

func TestListKeys(t *testing.T) {
	e := setup(t, false)
	e.create(t, owner, domain.EnvTest)
	e.create(t, owner, domain.EnvTest)
	for _, a := range []domain.Actor{owner, dev, support} {
		got, err := e.svc.ListKeys(context.Background(), a, e.ten.ID, domain.EnvTest)
		if err != nil || len(got) != 2 {
			t.Fatalf("%s: %v %d", a.Role, err, len(got))
		}
	}
	if got, err := e.svc.ListKeys(context.Background(), owner, e.ten.ID, domain.EnvLive); err != nil || len(got) != 0 {
		t.Fatalf("live: %v %d", err, len(got))
	}
	if _, err := e.svc.ListKeys(context.Background(), domain.Actor{Role: "bogus"}, e.ten.ID, domain.EnvTest); !errors.Is(err, domain.ErrForbiddenRole) {
		t.Fatalf("err=%v", err)
	}
	if _, err := e.svc.ListKeys(context.Background(), owner, e.ten.ID, "x"); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err=%v", err)
	}
}
