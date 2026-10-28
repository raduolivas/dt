package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

func (f *fixture) withPartner() *fakePartner {
	p := newFakePartner()
	f.svc.Partner = p
	return p
}

// TestPartnerViewShowsHiddenCourier: a Direct delivery that is only
// confirmed hides its courier from the customer, but the partner sees the
// courier and the last position.
func TestPartnerViewShowsHiddenCourier(t *testing.T) {
	f := newFixture(t)
	f.courier(t, "cou_1")
	f.delivery(t, "del_h", domain.ModelDirect, "cou_1", domain.StatusConfirmed)
	f.clock.Advance(time.Second)
	now := f.clock.Now()
	if acc, _, err := f.svc.Ingest(context.Background(), sc, "cou_1",
		[]domain.Fix{fixAt(now, 52.369, 4.9)}, "relay"); err != nil || acc != 1 {
		t.Fatalf("Ingest = %d, %v", acc, err)
	}

	cust, err := f.svc.ViewerSnapshot(context.Background(), sc, "del_h")
	if err != nil || cust.Courier != nil {
		t.Fatalf("customer snapshot courier = %+v, %v; want hidden", cust.Courier, err)
	}
	v, err := f.svc.PartnerView(context.Background(), sc, "del_h")
	if err != nil {
		t.Fatal(err)
	}
	c := v.Courier
	if c == nil || c.ID != "cou_1" || c.ExternalID != "ext-cou_1" {
		t.Fatalf("partner courier = %+v, want cou_1", c)
	}
	if c.Position == nil || c.Position.Lat != 52.369 || c.PositionAt == nil || c.Signal != domain.SignalLive {
		t.Fatalf("partner courier position = %+v signal %q", c.Position, c.Signal)
	}
	if v.DeliveryID != "del_h" || v.Status != domain.StatusConfirmed {
		t.Fatalf("view = %+v", v)
	}
}

func TestPartnerViewNotFoundOutsideScope(t *testing.T) {
	f := newFixture(t)
	f.delivery(t, "del_x", domain.ModelDirect, "", domain.StatusConfirmed)
	for name, s := range map[string]Scope{
		"other tenant": {TenantID: "ten_b", Env: "test"}, "other environment": {TenantID: "ten_a", Env: "live"},
	} {
		if _, err := f.svc.PartnerView(context.Background(), s, "del_x"); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v, want ErrNotFound", name, err)
		}
	}
	if _, err := f.svc.PartnerView(context.Background(), sc, "del_none"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
}

func seedTrail(p *fakePartner, id string, n int) {
	for i := range n {
		p.entries[id] = append(p.entries[id], TrailEntry{ID: int64(i + 1),
			Fix: domain.Fix{T: t0.Add(time.Duration(i) * time.Second), Pos: domain.LatLng{Lat: 52 + float64(i)/1000, Lng: 4.9},
				AccuracyM: 5, Source: "relay"}})
	}
}

func TestTrailPagesWithOpaqueCursor(t *testing.T) {
	f := newFixture(t)
	p := f.withPartner()
	seedTrail(p, "del_t", 5)
	ctx := context.Background()

	var got []float64
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("paging does not end")
		}
		pg, err := f.svc.ListTrail(ctx, sc, "del_t", cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		if !pg.RawAvailable || pg.Simplified != nil {
			t.Fatalf("page = %+v", pg)
		}
		for _, fx := range pg.Fixes {
			got = append(got, fx.Pos.Lat)
		}
		if !pg.HasMore {
			if pg.NextCursor != "" {
				t.Fatalf("last page has cursor %q", pg.NextCursor)
			}
			break
		}
		if len(pg.Fixes) != 2 || pg.NextCursor == "" {
			t.Fatalf("page = %+v", pg)
		}
		cursor = pg.NextCursor
	}
	if len(got) != 5 || got[0] != 52 || got[4] != 52.004 {
		t.Fatalf("fixes = %v, want all 5 in order exactly once", got)
	}
}

func TestTrailLimitAndCursorValidation(t *testing.T) {
	f := newFixture(t)
	p := f.withPartner()
	seedTrail(p, "del_t", 3)
	ctx := context.Background()
	for _, l := range []int{-1, 1001} {
		if _, err := f.svc.ListTrail(ctx, sc, "del_t", "", l); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("limit %d: %v, want ErrInvalidArgument", l, err)
		}
	}
	if _, err := f.svc.ListTrail(ctx, sc, "del_t", "not-a-cursor", 10); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("bad cursor: %v", err)
	}
	// 0 is the default (500): everything fits in one page.
	pg, err := f.svc.ListTrail(ctx, sc, "del_t", "", 0)
	if err != nil || len(pg.Fixes) != 3 || pg.HasMore {
		t.Fatalf("default limit page = %+v, %v", pg, err)
	}
	if _, err := f.svc.ListTrail(ctx, Scope{TenantID: "ten_b", Env: "test"}, "del_t", "", 10); !errors.Is(err, ErrNotFound) {
		t.Errorf("other tenant: %v", err)
	}
}

func TestTrailAfterRetentionIsSimplified(t *testing.T) {
	f := newFixture(t)
	p := f.withPartner()
	seedTrail(p, "del_r", 3)
	p.gone["del_r"] = true
	p.simplified["del_r"] = []domain.LatLng{{Lat: 52, Lng: 4.9}, {Lat: 52.01, Lng: 4.91}}

	pg, err := f.svc.ListTrail(context.Background(), sc, "del_r", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if pg.RawAvailable || len(pg.Fixes) != 0 || pg.HasMore || pg.NextCursor != "" || len(pg.Simplified) != 2 {
		t.Fatalf("page = %+v, want raw_available false, no fixes, the simplified route", pg)
	}
}

func TestProofPointsPassThroughScoped(t *testing.T) {
	f := newFixture(t)
	p := f.withPartner()
	p.proofs["del_p"] = []ProofPoint{{DeliveryID: "del_p", Status: "at_pickup", At: t0, Actor: "courier"}}
	got, err := f.svc.ListProofPoints(context.Background(), sc, "del_p")
	if err != nil || len(got) != 1 || got[0].Status != "at_pickup" {
		t.Fatalf("proof points = %+v, %v", got, err)
	}
	if _, err := f.svc.ListProofPoints(context.Background(), Scope{TenantID: "ten_b", Env: "test"}, "del_p"); !errors.Is(err, ErrNotFound) {
		t.Errorf("other tenant: %v", err)
	}
}
