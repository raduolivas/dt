package api_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/api"
)

// The topology JSON uses lowercase keys (ruling R29); the web types mirror them.
func TestTopologyJSONKeysAreLowercase(t *testing.T) {
	st := openStore(t)
	srv := newServer(t, st, api.NewBroadcaster(), nil)
	var r struct {
		Components []map[string]any
		Wires      []map[string]any
	}
	getJSON(t, srv.URL+"/api/topology", &r)
	require.NotEmpty(t, r.Components)
	require.NotEmpty(t, r.Wires)
	for _, k := range []string{"id", "label", "zone", "parent", "instrumented", "status", "state", "lastSeen"} {
		require.Contains(t, r.Components[0], k)
	}
	for _, k := range []string{"id", "from", "to", "channel"} {
		require.Contains(t, r.Wires[0], k)
	}
	for _, k := range []string{"ID", "Label", "Zone", "From", "Channel"} {
		require.NotContains(t, r.Components[0], k)
		require.NotContains(t, r.Wires[0], k)
	}
}
