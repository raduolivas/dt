package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app/apptest"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

func TestTrackPageFrameAncestors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	links := &app.TrackingLinks{Store: apptest.NewMemLinks(), Hasher: apptest.Hasher{}, Revoker: &apptest.MemRevoker{},
		Expiries: &apptest.MemExpiries{}, Clock: fixedClock{time.Unix(0, 0)}}
	settings := apptest.NewMemSettings()
	p := &app.TrackPage{Links: links, Settings: settings}
	owner := domain.Principal{TenantID: "ten_a", Env: domain.EnvTest}
	_ = settings.Put(ctx, "ten_a", domain.ViewerSettings{EmbedOrigins: []string{"https://a.example"}}, nil, time.Unix(1, 0))

	_, tok, err := links.Issue(ctx, owner, "del_1")
	if err != nil {
		t.Fatal(err)
	}
	got := func(tok string) string {
		fa, err := p.FrameAncestors(ctx, tok)
		if err != nil {
			t.Fatal(err)
		}
		return fa
	}
	if got(tok) != "https://a.example" {
		t.Fatalf("known = %q", got(tok))
	}
	// Revoked and expired links still name their tenant; unknown ones do not.
	if _, _, err := links.Rotate(ctx, owner, "del_1"); err != nil {
		t.Fatal(err)
	}
	if got(tok) != "https://a.example" {
		t.Fatalf("revoked = %q", got(tok))
	}
	unknown, _ := domain.NewLinkToken()
	if got(unknown) != "'none'" || got("junk") != "'none'" {
		t.Fatalf("unknown = %q / %q", got(unknown), got("junk"))
	}
	// A tenant without settings: no one may frame.
	_, tokB, _ := links.Issue(ctx, domain.Principal{TenantID: "ten_b", Env: domain.EnvTest}, "del_2")
	if got(tokB) != "'none'" {
		t.Fatalf("no settings = %q", got(tokB))
	}
	// Store failures are errors, not 'none'.
	settings.Fail(errors.New("down"))
	if _, err := p.FrameAncestors(ctx, tokB); err == nil {
		t.Fatal("store failure swallowed")
	}
}
