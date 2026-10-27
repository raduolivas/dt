package domain

import (
	"strings"
	"testing"
	"time"
)

func TestLinkTokenIs128BitURLSafe(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for range 100 {
		tok, err := NewLinkToken()
		if err != nil {
			t.Fatal(err)
		}
		if len(tok) != 22 || strings.ContainsAny(tok, "+/=") || !ValidLinkToken(tok) {
			t.Fatalf("token %q is not 22 base64url characters", tok)
		}
		if seen[tok] {
			t.Fatal("duplicate token")
		}
		seen[tok] = true
	}
	for _, bad := range []string{"", "short", strings.Repeat("a", 23), strings.Repeat("a", 21) + "+",
		strings.Repeat("a", 21) + "B" /* non-canonical last char */, "dt_sk_test_" + strings.Repeat("a", 11)} {
		if ValidLinkToken(bad) {
			t.Errorf("ValidLinkToken(%q) = true", bad)
		}
	}
}

func TestLinkUsable(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Second), now.Add(time.Second)
	cases := []struct {
		l    TrackingLink
		want bool
	}{
		{TrackingLink{}, true},
		{TrackingLink{ExpiresAt: &future}, true},
		{TrackingLink{ExpiresAt: &now}, false},
		{TrackingLink{ExpiresAt: &past}, false},
		{TrackingLink{RevokedAt: &past}, false},
		{TrackingLink{RevokedAt: &future}, false}, // revoked is revoked
	}
	for i, c := range cases {
		if got := c.l.Usable(now); got != c.want {
			t.Errorf("case %d: Usable = %t, want %t", i, got, c.want)
		}
	}
	if got := LinkExpiry(now); !got.Equal(now.Add(30 * time.Minute)) {
		t.Fatalf("LinkExpiry = %v", got)
	}
	for s, want := range map[string]bool{"delivered": true, "cancelled": true, "failed": true,
		"picked_up": false, "arriving": false, "confirmed": false, "": false} {
		if FinalStatus(s) != want {
			t.Errorf("FinalStatus(%q) != %t", s, want)
		}
	}
}
