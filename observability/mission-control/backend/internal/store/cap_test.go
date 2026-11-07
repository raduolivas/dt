package store_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
)

// M3: an oversize id is dropped to "" (never truncated into a fake id) and
// counted; reason is truncated on a rune boundary.
func TestCapEventDropsOversizeIDs(t *testing.T) {
	big := strings.Repeat("x", 10*1024)
	e := store.Event{Kind: "request", Source: big, Target: "tracking.ingest", DeliveryID: big, TenantID: big, Env: big,
		TraceID: big, SpanID: big, OrderRef: big, Reason: strings.Repeat("é", 5000)}
	n := store.CapEvent(&e)
	require.Equal(t, 7, n)
	for _, v := range []string{e.Source, e.DeliveryID, e.TenantID, e.Env, e.TraceID, e.SpanID, e.OrderRef} {
		require.Empty(t, v)
	}
	require.Equal(t, "tracking.ingest", e.Target)
	require.LessOrEqual(t, len(e.Reason), store.MaxReasonLen)
	require.NotEmpty(t, e.Reason)
	require.True(t, utf8.ValidString(e.Reason))

	ok := store.Event{Kind: "request", Source: "gateway", DeliveryID: strings.Repeat("d", store.MaxIDLen), Reason: "fine"}
	require.Zero(t, store.CapEvent(&ok))
	require.Len(t, ok.DeliveryID, store.MaxIDLen)
	require.Equal(t, "fine", ok.Reason)
}

// A batch with one 10 KB id still inserts, and the drop is reported.
func TestInsertEventsSurvivesOversizeID(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, startPostgres(t))
	require.NoError(t, err)
	t.Cleanup(s.Close)
	var capped int
	s.SetOnCapped(func(n int) { capped += n })
	now := time.Now().UTC()
	// Incompressible: a repeated character would compress under the btree row limit.
	raw := make([]byte, 5*1024)
	_, _ = rand.Read(raw)
	big := hex.EncodeToString(raw) // 10 KB
	evs := []store.Event{
		{ObservedAt: now, OccurredAt: now, Kind: "request", Source: "gateway", DeliveryID: "del_1"},
		{ObservedAt: now, OccurredAt: now, Kind: "request", Source: "gateway", DeliveryID: big, TraceID: big, TenantID: big,
			Reason: strings.Repeat("r", 10*1024)},
		{ObservedAt: now, OccurredAt: now, Kind: "request", Source: "gateway", DeliveryID: "del_2"},
	}
	require.NoError(t, s.InsertEvents(ctx, evs))
	require.Equal(t, 3, capped)
	for _, e := range evs {
		require.NotZero(t, e.ID)
	}
	require.Empty(t, evs[1].DeliveryID, "the caller sees the stored (capped) values")
	got, err := s.EventsAfter(ctx, 0, 10)
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.Empty(t, got[1].DeliveryID)
	require.Empty(t, got[1].TraceID)
	require.Len(t, got[1].Reason, store.MaxReasonLen)
}
