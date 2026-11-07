package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/api"
)

// I2c: a DNS-rebinding page reaches the loopback port with its own Host; the
// allow-list refuses it before any handler (the sim write routes included).
func TestAllowHostsLoopbackOnly(t *testing.T) {
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached = true })
	h, err := api.AllowHosts("0.0.0.0:8095", nil, next)
	require.NoError(t, err)
	for _, host := range []string{"127.0.0.1:8095", "localhost:8095", "LocalHost:8095", "[::1]:8095"} {
		reached = false
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/self", nil)
		req.Host = host
		h.ServeHTTP(rec, req)
		require.True(t, reached, host)
		require.Equal(t, http.StatusOK, rec.Code, host)
	}
	for _, host := range []string{"evil.example:8095", "evil.example", "127.0.0.1:9999", "127.0.0.1", "localhost",
		"127.0.0.1.evil.example:8095", "mc-backend:8095", "", "[::1]"} {
		for _, path := range []string{"/api/self", "/api/sim/runs", "/"} {
			reached = false
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, path, nil)
			req.Host = host
			h.ServeHTTP(rec, req)
			require.False(t, reached, "%q %s reached the handler", host, path)
			require.Equal(t, http.StatusMisdirectedRequest, rec.Code, "%q %s", host, path)
		}
	}
}

// The OTLP listener also accepts the collector, which addresses mc-backend:4318.
func TestAllowHostsOTLPAcceptsCollectorAlias(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) })
	h, err := api.AllowHosts("0.0.0.0:4318", []string{"mc-backend"}, ok)
	require.NoError(t, err)
	for host, want := range map[string]int{
		"mc-backend:4318": http.StatusAccepted, "127.0.0.1:4318": http.StatusAccepted,
		"evil.example:4318": http.StatusMisdirectedRequest, "mc-backend:8095": http.StatusMisdirectedRequest,
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/traces", nil)
		req.Host = host
		h.ServeHTTP(rec, req)
		require.Equal(t, want, rec.Code, host)
	}
	_, err = api.AllowHosts("no-port", nil, ok)
	require.Error(t, err)
}

// A port-80 listener: browsers omit the default port from Host.
func TestAllowHostsDefaultPort(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})
	h, err := api.AllowHosts("127.0.0.1:80", nil, ok)
	require.NoError(t, err)
	for host, want := range map[string]int{"localhost": http.StatusOK, "localhost:80": http.StatusOK, "evil.example": http.StatusMisdirectedRequest} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = host
		h.ServeHTTP(rec, req)
		require.Equal(t, want, rec.Code, host)
	}
}
