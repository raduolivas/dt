package api

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// loopbackNames are the Host names a loopback listener answers to.
var loopbackNames = []string{"127.0.0.1", "localhost", "::1"}

// AllowHosts refuses (421) every request whose Host header is not a loopback
// name, or one of extra, with the port of the listen address addr. It runs
// before any handler. A loopback bind alone does not stop DNS rebinding: a
// page on evil.example that rebinds its name to 127.0.0.1 reaches the port
// with Host evil.example, and the browser treats it as same-origin.
func AllowHosts(addr string, extra []string, next http.Handler) (http.Handler, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("api: allow hosts: listen address must be host:port")
	}
	allowed := map[string]bool{}
	for _, n := range append(append([]string{}, loopbackNames...), extra...) {
		allowed[strings.ToLower(net.JoinHostPort(n, port))] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(r.Host)
		if _, _, err := net.SplitHostPort(host); err != nil && port == "80" && host != "" {
			host = net.JoinHostPort(strings.Trim(host, "[]"), port) // browsers omit the default port
		}
		if !allowed[host] {
			writeErr(w, http.StatusMisdirectedRequest, "unknown host")
			return
		}
		next.ServeHTTP(w, r)
	}), nil
}
