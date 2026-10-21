package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

type clientIPKey struct{}

// ClientIPFrom returns the client address ClientIP stored in ctx. The address
// may be invalid (the zero netip.Addr) when it could not be determined; an
// invalid address matches no IP allowlist.
func ClientIPFrom(ctx context.Context) (netip.Addr, bool) {
	ip, ok := ctx.Value(clientIPKey{}).(netip.Addr)
	return ip, ok
}

// ParseTrustedProxies parses TRUSTED_PROXIES: a comma-separated list of CIDR
// prefixes, empty by default. A bare address is refused (write 10.0.0.7/32)
// so the trusted set is always explicit. main wires the env var (Task 12).
func ParseTrustedProxies(s string) ([]netip.Prefix, error) {
	if strings.Trim(s, " ,") == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]netip.Prefix, 0, len(parts))
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("TRUSTED_PROXIES: entry %d is empty", i+1)
		}
		p, err := netip.ParsePrefix(part)
		if err != nil {
			return nil, fmt.Errorf("TRUSTED_PROXIES: entry %d is not a CIDR prefix: %w", i+1, err)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// ClientIP stores the client address in the request context.
//
// Without trusted proxies the client is the TCP peer and X-Forwarded-For is
// ignored. Otherwise, when the peer is trusted, X-Forwarded-For is walked
// right to left, skipping trusted hops; the first untrusted hop is the client.
// If every hop is trusted the leftmost one is the client. A hop that does not
// parse yields an invalid address, so it can never pass an allowlist.
func ClientIP(trusted []netip.Prefix) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientAddr(r, trusted)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientIPKey{}, ip)))
		})
	}
}

func clientAddr(r *http.Request, trusted []netip.Prefix) netip.Addr {
	ip := peerAddr(r.RemoteAddr)
	if len(trusted) == 0 || !isTrusted(ip, trusted) {
		return ip
	}
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(v, ",")...)
	}
	for _, hop := range slices.Backward(hops) {
		hop = strings.TrimSpace(hop)
		addr, err := netip.ParseAddr(hop)
		if err != nil {
			return netip.Addr{}
		}
		ip = addr.Unmap()
		if !isTrusted(ip, trusted) {
			return ip
		}
	}
	return ip
}

func peerAddr(remote string) netip.Addr {
	if ap, err := netip.ParseAddrPort(remote); err == nil {
		return ap.Addr().Unmap()
	}
	if a, err := netip.ParseAddr(remote); err == nil {
		return a.Unmap()
	}
	return netip.Addr{}
}

func isTrusted(ip netip.Addr, trusted []netip.Prefix) bool {
	return ip.IsValid() && slices.ContainsFunc(trusted, func(p netip.Prefix) bool { return p.Contains(ip) })
}
