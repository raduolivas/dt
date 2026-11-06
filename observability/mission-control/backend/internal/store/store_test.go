package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
)

func startPostgres(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test: needs Docker (run without -short)")
	}
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgis/postgis:18-3.6",
		postgres.WithDatabase("mc"), postgres.WithUsername("mc"), postgres.WithPassword("mc"),
		postgres.BasicWaitStrategies())
	testcontainers.CleanupContainer(t, c)
	require.NoError(t, err)
	url, err := c.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	return url
}

func TestInsertAndDeleteOlderThan(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, startPostgres(t))
	require.NoError(t, err)
	t.Cleanup(s.Close)

	now := time.Now().UTC().Truncate(time.Millisecond)
	old := now.Add(-8 * 24 * time.Hour)
	events := []store.Event{
		{ObservedAt: old, OccurredAt: old, Kind: "span", Source: "gateway", Target: "deliveries", Outcome: "ok", DeliveryID: "d1"},
		{ObservedAt: now, OccurredAt: now, Kind: "span", Source: "gateway", Target: "deliveries", Outcome: "ok", DeliveryID: "d1", Attrs: []byte(`{"http.status":200}`)},
		{ObservedAt: now, OccurredAt: now, Kind: "nats", Source: "deliveries", Target: "tracking", Outcome: "failed", Reason: "timeout"},
	}
	require.NoError(t, s.InsertEvents(ctx, events))
	require.NoError(t, s.InsertEvents(ctx, nil), "empty batch is a no-op")

	n, err := s.CountEvents(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 3, n)

	deleted, err := s.DeleteOlderThan(ctx, now.Add(-7*24*time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)
	n, err = s.CountEvents(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 2, n)
}

func TestOpenIsIdempotentAndPingWorks(t *testing.T) {
	ctx := context.Background()
	url := startPostgres(t)
	s, err := store.Open(ctx, url)
	require.NoError(t, err)
	s.Close()
	s, err = store.Open(ctx, url)
	require.NoError(t, err, "second Open re-runs migrations")
	t.Cleanup(s.Close)
	require.NoError(t, s.Ping(ctx))
}

func TestRecordStartupWritesDowntimeFromHeartbeat(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, startPostgres(t))
	require.NoError(t, err)
	t.Cleanup(s.Close)

	t0 := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)
	require.NoError(t, s.RecordStartup(ctx, t0), "first start: no heartbeat, no gap")
	n, err := s.CountDowntime(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 0, n)

	require.NoError(t, s.Heartbeat(ctx, t0.Add(time.Minute)))
	require.NoError(t, s.RecordStartup(ctx, t0.Add(10*time.Minute)))
	n, err = s.CountDowntime(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
}
