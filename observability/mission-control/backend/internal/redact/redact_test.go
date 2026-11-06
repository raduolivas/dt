package redact

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedactDropsSensitiveKeysAndSecretValues(t *testing.T) {
	in := map[string]any{
		"delivery_id":                       "del_1",
		"dropoff_lat":                       1.5,
		"customer_name":                     "Ann",
		"x-api-key":                         "abc",
		"api_key":                           "abc",
		"database_url":                      "postgres://x",
		"http.request.header.authorization": "Bearer x",
		"note":                              "dt_sk_test_abc123",
		"header":                            "key dt_pk_live_zzz in text",
		"count":                             3,
		"nested":                            map[string]any{"phone": "1", "ok": "fine"},
		"list":                              []any{"fine", "dt_sk_live_q"},
	}
	out, dropped := Attrs(in)
	require.Equal(t, 10, dropped)
	require.Equal(t, map[string]any{
		"delivery_id": "del_1", "count": 3,
		"nested": map[string]any{"ok": "fine"},
		"list":   []any{"fine"},
	}, out)
	require.Len(t, in, 12, "input untouched")
}

func TestSensitiveKeyMatchesLogx(t *testing.T) {
	for _, k := range []string{"lat", "Dropoff.Lng", "first-name", "pickup_name", "x-api-key", "database_url", "jwt"} {
		require.True(t, SensitiveKey(k), k)
	}
	for _, k := range []string{"delivery_id", "tenant_id", "reason", "count", "changed_fields"} {
		require.False(t, SensitiveKey(k), k)
	}
}
