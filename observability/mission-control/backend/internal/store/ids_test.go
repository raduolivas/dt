package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
)

func TestInsertEventsSetsIDs(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, startPostgres(t))
	require.NoError(t, err)
	t.Cleanup(s.Close)
	now := time.Now().UTC()
	evs := []store.Event{
		{ObservedAt: now, OccurredAt: now, Kind: "request", Source: "gateway"},
		{ObservedAt: now, OccurredAt: now, Kind: "request", Source: "gateway"},
	}
	require.NoError(t, s.InsertEvents(ctx, evs))
	require.NotZero(t, evs[0].ID, "the live stream needs the stored id")
	require.Greater(t, evs[1].ID, evs[0].ID)
}
