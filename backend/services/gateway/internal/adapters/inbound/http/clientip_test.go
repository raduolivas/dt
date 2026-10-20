package httpapi_test

import (
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
