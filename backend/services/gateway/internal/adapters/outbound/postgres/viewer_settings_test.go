package postgres_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/raduolivas/dt/backend/services/gateway/internal/adapters/outbound/postgres"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

func TestViewerSettingsRoundTripAndOrdering(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, _ := setup(t)
	s := postgres.NewViewerSettingsStore(pool)

	if _, ok, err := s.Get(ctx, "ten_a"); ok || err != nil {
		t.Fatalf("empty Get = %v, %v", ok, err)
	}
	logo := []byte{1, 2, 3}
	v1 := domain.ViewerSettings{EmbedOrigins: []string{"https://a.example"},
		Branding: &domain.Branding{DisplayName: "A", Theme: "light", LogoSHA256: "ab", LogoContentType: "image/webp"}}
	if err := s.Put(ctx, "ten_a", v1, logo, t0); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get(ctx, "ten_a")
	if err != nil || !ok || !reflect.DeepEqual(got, v1) {
		t.Fatalf("Get = %+v %v %v", got, ok, err)
	}
	b, ct, err := s.Logo(ctx, "ten_a", "ab")
	if err != nil || ct != "image/webp" || !reflect.DeepEqual(b, logo) {
		t.Fatalf("Logo = %v %q %v", b, ct, err)
	}
	// Another tenant's, or another hash's, logo is not found.
	for _, c := range [][2]string{{"ten_b", "ab"}, {"ten_a", "cd"}} {
		if _, _, err := s.Logo(ctx, c[0], c[1]); !errors.Is(err, app.ErrNotFound) {
			t.Errorf("Logo(%v) = %v", c, err)
		}
	}
	// Older and equal-time writes change nothing; a newer one replaces.
	for _, at := range []time.Time{t0.Add(-time.Second), t0} {
		if err := s.Put(ctx, "ten_a", domain.ViewerSettings{}, nil, at); err != nil {
			t.Fatal(err)
		}
	}
	if got, _, _ = s.Get(ctx, "ten_a"); !reflect.DeepEqual(got, v1) {
		t.Fatalf("stale write applied: %+v", got)
	}
	if err := s.Put(ctx, "ten_a", domain.ViewerSettings{}, nil, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got, _, _ = s.Get(ctx, "ten_a"); got.Branding != nil || got.EmbedOrigins != nil {
		t.Fatalf("newer write not applied: %+v", got)
	}
	if _, _, err := s.Logo(ctx, "ten_a", "ab"); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("Logo after clear = %v", err)
	}
}
