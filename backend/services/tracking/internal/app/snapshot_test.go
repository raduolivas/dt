package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

func TestViewerSnapshotFiltersAndRebuildsMissingState(t *testing.T) {
	f := newFixture(t)
	f.courier(t, "cou_1")
	f.delivery(t, "del_s", domain.ModelDirect, "cou_1", domain.StatusConfirmed)
	ctx := context.Background()

	snap, err := f.svc.ViewerSnapshot(ctx, sc, "del_s")
	if err != nil {
		t.Fatal(err)
	}
	// Direct before pickup: the courier is hidden from the customer.
	if snap.DeliveryID != "del_s" || snap.Courier != nil || snap.Version == 0 {
		t.Fatalf("snapshot = %+v", snap)
	}
	last := snap.Version

	// Redis lost the state: it is rebuilt, stored and published with a
	// greater version.
	f.states.flush()
	f.clock.Advance(time.Millisecond)
	snap, err = f.svc.ViewerSnapshot(ctx, sc, "del_s")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Version <= last {
		t.Fatalf("version after rebuild %d <= %d", snap.Version, last)
	}
	if _, ok, _ := f.states.Get(ctx, "del_s"); !ok {
		t.Fatal("rebuilt state was not stored")
	}

	for name, c := range map[string]struct {
		sc Scope
		id string
	}{
		"other tenant":      {Scope{TenantID: "ten_b", Env: "test"}, "del_s"},
		"other environment": {Scope{TenantID: "ten_a", Env: "live"}, "del_s"},
		"unknown":           {sc, "del_none"},
	} {
		if _, err := f.svc.ViewerSnapshot(ctx, c.sc, c.id); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v, want ErrNotFound", name, err)
		}
	}
}
