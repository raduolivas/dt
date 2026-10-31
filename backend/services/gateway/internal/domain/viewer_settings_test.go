package domain

import (
	"testing"
)

func TestFrameAncestorsEmptyIsNone(t *testing.T) {
	t.Parallel()
	if got := FrameAncestors(nil); got != "'none'" {
		t.Fatalf("nil = %q", got)
	}
	if got := FrameAncestors([]string{}); got != "'none'" {
		t.Fatalf("empty = %q", got)
	}
	if got := FrameAncestors([]string{"https://a.example", "https://b.example:8443"}); got != "https://a.example https://b.example:8443" {
		t.Fatalf("two = %q", got)
	}
}

// An origin that could add a directive or a source never reaches the header.
func TestFrameAncestorsDropsInjection(t *testing.T) {
	t.Parallel()
	got := FrameAncestors([]string{"https://ok.example", "https://x.example; script-src *", "a b", "'self'", "https://y.example\r\nX: 1", ""})
	if got != "https://ok.example" {
		t.Fatalf("got %q", got)
	}
	if got := FrameAncestors([]string{"a;b"}); got != "'none'" {
		t.Fatalf("all dropped = %q", got)
	}
}

func TestPageCSPExact(t *testing.T) {
	t.Parallel()
	o := PageOrigins{CDN: "https://cdn.example", API: "https://api.example", Tiles: "https://cdn.example",
		Extra: []string{"https://tiles.openfreemap.org", "https://x.example"}}
	want := "default-src 'none'; script-src https://cdn.example; style-src https://cdn.example 'unsafe-inline'; " +
		"img-src https://cdn.example 'self' data: blob:; " +
		"connect-src https://api.example wss://api.example https://cdn.example https://cdn.example https://tiles.openfreemap.org https://x.example; " +
		"worker-src blob:; font-src https://cdn.example; base-uri 'none'; form-action 'none'; frame-ancestors https://shop.example"
	if got := PageCSP("https://shop.example", o); got != want {
		t.Fatalf("csp =\n%s\nwant\n%s", got, want)
	}
	// No extras: no trailing space.
	o.Extra = nil
	if got := PageCSP("'none'", o); got == "" || got[len(got)-len("frame-ancestors 'none'"):] != "frame-ancestors 'none'" ||
		!contains(got, "connect-src https://api.example wss://api.example https://cdn.example https://cdn.example; ") {
		t.Fatalf("csp = %s", got)
	}
}

// The viewer stream is a WebSocket on the API origin: a CSP source with an
// http(s) scheme does not match a ws(s) URL, so connect-src names both.
func TestPageCSPAllowsTheAPIWebSocket(t *testing.T) {
	t.Parallel()
	for api, ws := range map[string]string{"https://api.example": "wss://api.example", "http://localhost:8080": "ws://localhost:8080"} {
		got := PageCSP("'none'", PageOrigins{CDN: "https://cdn.example", API: api})
		if !contains(got, "connect-src "+api+" "+ws+" https://cdn.example; ") {
			t.Fatalf("api %s: csp = %s", api, got)
		}
	}
}

// Without a CDN (dev) the page's own assets are the origin's; empty API and
// tiles are left out rather than leaving blank sources.
func TestPageCSPDefaultsWithoutCDN(t *testing.T) {
	t.Parallel()
	got := PageCSP("'none'", PageOrigins{})
	want := "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' 'self' data: blob:; " +
		"connect-src 'self'; worker-src blob:; font-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	if got != want {
		t.Fatalf("csp = %s", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
