package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
)

func TestOrderOrigin(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, st.UpsertOrder(ctx, store.OrderHit{TenantID: "t", Environment: "test", OrderRef: "mc-sim-1", DeliveryID: "del_s", Origin: "simulated", CreatedAt: now}))
	require.NoError(t, st.UpsertOrder(ctx, store.OrderHit{TenantID: "t", Environment: "test", OrderRef: "A-1", DeliveryID: "del_l", Origin: "live", CreatedAt: now}))
	got, err := st.OrderOrigin(ctx, "del_s")
	require.NoError(t, err)
	require.Equal(t, "simulated", got)
	got, err = st.OrderOrigin(ctx, "del_l")
	require.NoError(t, err)
	require.Equal(t, "live", got)
	got, err = st.OrderOrigin(ctx, "del_none")
	require.NoError(t, err)
	require.Empty(t, got)
}
