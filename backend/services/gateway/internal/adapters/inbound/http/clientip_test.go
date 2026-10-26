package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"testing"

	httpapi "github.com/raduolivas/dt/backend/services/gateway/internal/adapters/inbound/http"
)

func TestParseTrustedProxies(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"", "  ", " , "} {
		got, err := httpapi.ParseTrustedProxies(in)
		if err != nil || len(got) != 0 {
			t.Errorf("ParseTrustedProxies(%q) = %v, %v; want empty", in, got, err)
		}
	}
	got, err := httpapi.ParseTrustedProxies("10.0.0.0/8, 198.51.100.7/24 ,2001:db8::/32")
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("2001:db8::/32"),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for _, bad := range []string{"10.0.0.1", "nope", "10.0.0.0/33", "10.0.0.0/8,,"} {
		if _, err := httpapi.ParseTrustedProxies(bad); err == nil {
			t.Errorf("ParseTrustedProxies(%q) accepted", bad)
		}
	}
}

func TestForwardedHopWithPort(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.7":         "203.0.113.7",
		"203.0.113.7:51234":   "203.0.113.7",
		"[2001:db8::1]:443":   "2001:db8::1",
		"2001:db8::1":         "2001:db8::1",
		"::ffff:203.0.113.7":  "203.0.113.7",
		"[::ffff:10.0.0.1]:1": "10.0.0.1",
	} {
		got, ok := httpapi.ParseForwardedHop(in)
		if !ok || got.String() != want {
			t.Errorf("ParseForwardedHop(%q) = %v, %v; want %s", in, got, ok, want)
		}
	}
	for _, in := range []string{"garbage:1", "", "203.0.113.7:", "203.0.113.7:99999", "[2001:db8::1]", "unknown"} {
		if got, ok := httpapi.ParseForwardedHop(in); ok {
			t.Errorf("ParseForwardedHop(%q) = %v, true; want false", in, got)
		}
	}
}

func TestClientIPStripsPortFromTrustedProxyHops(t *testing.T) {
	trusted, _ := httpapi.ParseTrustedProxies("10.0.0.0/8")
	var got netip.Addr
	h := httpapi.ClientIP(trusted)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = httpapi.ClientIPFrom(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.5:4000"
	req.Header.Set("X-Forwarded-For", "203.0.113.7:51234, 10.0.0.9")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got.String() != "203.0.113.7" {
		t.Fatalf("client ip = %v, want 203.0.113.7", got)
	}
}
