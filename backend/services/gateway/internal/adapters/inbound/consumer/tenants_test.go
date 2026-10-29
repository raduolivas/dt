package consumer_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/outbox"
	"github.com/raduolivas/dt/backend/services/gateway/internal/adapters/inbound/consumer"
	"github.com/raduolivas/dt/backend/services/gateway/internal/adapters/outbound/postgres"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app/apptest"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

var png = append([]byte("\x89PNG\r\n\x1a\n"), 1, 2, 3, 4)

func settingsEv(t *testing.T, at time.Time, origins []string, b *eventsv1.Branding) *eventsv1.Envelope {
	t.Helper()
	return envelope(t, consumer.EventTenantSettingsSet, &eventsv1.TenantSettingsSet{
		TenantId: "ten_01", UpdatedAt: timestamppb.New(at), EmbedOrigins: origins, Branding: b,
	})
}

func (e *env) runTenants(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- outbox.Consume(ctx, e.js, e.pool, nilLog(), consumer.APIKeysStream, consumer.TenantsConfig,
			consumer.Tenants(postgres.NewViewerSettingsStore(e.pool)))
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Consume returned %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Consume did not stop after cancel")
		}
	})
}

func (e *env) waitTenantsAcked(t *testing.T, n uint64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		c, err := e.js.Consumer(context.Background(), consumer.APIKeysStream, consumer.TenantsConfig.Durable)
		if err == nil {
			if info, err := c.Info(context.Background()); err == nil && info.AckFloor.Stream >= n && info.NumAckPending == 0 {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the tenants consumer to ack %d", n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestTenantSettingsConsumerStoresAndIgnoresOlder(t *testing.T) {
	t.Parallel()
	e := setup(t)
	store := postgres.NewViewerSettingsStore(e.pool)
	ctx := context.Background()
	sum := sha256.Sum256(png)
	sha := hex.EncodeToString(sum[:])

	first := settingsEv(t, t0, []string{"https://shop.example"}, &eventsv1.Branding{
		DisplayName: "Pizza Co", PrimaryColor: "#112233", Theme: "dark", Logo: png, LogoContentType: "image/png"})
	e.publish(t, first, first.GetEventId())
	e.runTenants(t)
	e.waitTenantsAcked(t, 1)

	got, ok, err := store.Get(ctx, "ten_01")
	if err != nil || !ok {
		t.Fatalf("Get = %v, %v", ok, err)
	}
	want := domain.ViewerSettings{EmbedOrigins: []string{"https://shop.example"}, Branding: &domain.Branding{
		DisplayName: "Pizza Co", PrimaryColor: "#112233", Theme: "dark", LogoSHA256: sha, LogoContentType: "image/png"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("settings = %+v / %+v, want %+v", got, got.Branding, want)
	}
	if b, ct, err := store.Logo(ctx, "ten_01", sha); err != nil || ct != "image/png" || !reflect.DeepEqual(b, png) {
		t.Fatalf("logo = %v %q %v", b, ct, err)
	}

	// An older event, and an equal-time one, change nothing.
	older := settingsEv(t, t0.Add(-time.Hour), []string{"https://old.example"}, nil)
	e.publish(t, older, older.GetEventId())
	same := settingsEv(t, t0, nil, nil)
	e.publish(t, same, same.GetEventId())
	e.waitTenantsAcked(t, 3)
	got, _, _ = store.Get(ctx, "ten_01")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("after older/equal events: %+v, want %+v", got, want)
	}

	// A newer event with no branding clears it and the logo.
	newer := settingsEv(t, t0.Add(time.Minute), nil, nil)
	e.publish(t, newer, newer.GetEventId())
	e.waitTenantsAcked(t, 4)
	got, ok, _ = store.Get(ctx, "ten_01")
	if !ok || got.Branding != nil || len(got.EmbedOrigins) != 0 {
		t.Fatalf("after newer: %+v", got)
	}
	if _, _, err := store.Logo(ctx, "ten_01", sha); err == nil {
		t.Fatal("logo survived the branding being cleared")
	}
}

func TestTenantSettingsHandlerRejectsMalformed(t *testing.T) {
	t.Parallel()
	h := consumer.Tenants(apptest.NewMemSettings())
	for name, ev := range map[string]*eventsv1.Envelope{
		"no tenant":     envelope(t, consumer.EventTenantSettingsSet, &eventsv1.TenantSettingsSet{UpdatedAt: timestamppb.New(t0)}),
		"bad logo type": settingsEv(t, t0, nil, &eventsv1.Branding{Logo: png, LogoContentType: "image/gif"}),
		"huge logo":     settingsEv(t, t0, nil, &eventsv1.Branding{Logo: make([]byte, 102401), LogoContentType: "image/png"}),
	} {
		if err := h(context.Background(), nil, ev); !errors.Is(err, outbox.ErrPermanent) {
			t.Errorf("%s: err = %v, want permanent", name, err)
		}
	}
}

func nilLog() *slog.Logger { return slog.New(slog.DiscardHandler) }
