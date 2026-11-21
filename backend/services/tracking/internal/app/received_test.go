package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/raduolivas/dt/backend/services/tracking/internal/app"
)

func TestFixReceivedRoundTrip(t *testing.T) {
	if _, _, ok := app.FixReceived(context.Background()); ok {
		t.Fatal("empty ctx reports a received time")
	}
	at := time.UnixMilli(1_700_000_000_123)
	ctx := app.WithFixReceived(context.Background(), at, "relay")
	got, src, ok := app.FixReceived(ctx)
	if !ok || !got.Equal(at) || src != "relay" {
		t.Fatalf("got %v %q %v", got, src, ok)
	}
	if _, _, ok := app.FixReceived(app.WithFixReceived(context.Background(), time.Time{}, "relay")); ok {
		t.Fatal("zero time must read as unknown")
	}
}
