package rpc_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	trackingv1 "github.com/raduolivas/dt/backend/gen/dt/tracking/v1"
	"github.com/raduolivas/dt/backend/pkg/rpcauth"
	"github.com/raduolivas/dt/backend/services/tracking/internal/adapters/outbound/postgres"
	"github.com/raduolivas/dt/backend/services/tracking/internal/app"
	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

func scopeOf(tenant string) *trackingv1.Scope {
	return &trackingv1.Scope{TenantId: tenant, Environment: eventsv1.Environment_ENVIRONMENT_TEST, ActorId: "ak_1"}
}

// seedTrail stores n fixes of courierA on delivery del, one second apart
// from base, straight into the trail table.
func seedTrail(t *testing.T, pool *pgxpool.Pool, del string, base time.Time, n int) {
	t.Helper()
	w := postgres.NewTrailWriter(pool, nil)
	var rows []app.TrailRow
	for i := range n {
		rows = append(rows, app.TrailRow{DeliveryID: del, TenantID: "ten_a", Env: "test", CourierID: courierA,
			Fix: domain.Fix{T: base.Add(time.Duration(i) * time.Second), Pos: domain.LatLng{Lat: 52.36 + float64(i)/1000, Lng: 4.9},
				AccuracyM: 5, Source: "relay"}})
	}
	w.Append(rows)
	if err := w.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func inScope(t *testing.T, pool *pgxpool.Pool, fn func(ctx context.Context, tx pgx.Tx) error) {
	t.Helper()
	ctx := context.Background()
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('dt.tenant_id', 'ten_a', true), set_config('dt.environment', 'test', true)`); err != nil {
			return err
		}
		return fn(ctx, tx)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestGetPartnerViewRPCShowsHiddenCourier(t *testing.T) {
	c, _ := serve(t)
	ctx := context.Background()
	if _, err := c.IngestLocations(ctx, req("ten_a", courierA, 2)); err != nil {
		t.Fatal(err)
	}
	v, err := c.GetPartnerView(ctx, &trackingv1.GetPartnerViewRequest{Scope: scopeOf("ten_a"), DeliveryId: delD})
	if err != nil {
		t.Fatal(err)
	}
	if v.GetDeliveryId() != delD || v.GetDeliveryModel() != "direct" || v.GetStatus() != "confirmed" || v.GetVersion() <= 0 {
		t.Fatalf("view = %v", v)
	}
	cou := v.GetCourier()
	if cou.GetId() != courierA || cou.GetExternalId() != "x" || cou.GetDisplayName() != "Sam" || cou.GetVehicle() != "car" {
		t.Fatalf("courier = %v", cou)
	}
	if cou.GetPosition() == nil || cou.GetPositionAt() == nil || cou.GetSignal() != "live" {
		t.Fatalf("courier position = %v signal %q", cou.GetPosition(), cou.GetSignal())
	}
	if v.GetPickup().GetLat() != 52.36 || v.GetPickupName() != "Hub" || v.GetDropoff().GetLng() != 4.89 {
		t.Fatalf("places = %v", v)
	}
	if len(v.GetTimeline()) != 1 || v.GetTimeline()[0].GetStatus() != "confirmed" || v.GetUpdatedAt() == nil {
		t.Fatalf("timeline = %v", v.GetTimeline())
	}
	// The customer's snapshot of the same delivery still hides the courier.
	snap, err := c.GetSnapshot(ctx, &trackingv1.GetSnapshotRequest{Scope: scopeOf("ten_a"), DeliveryId: delD})
	if err != nil || len(snap.GetSnapshotJson()) == 0 {
		t.Fatal(err)
	}

	for name, r := range map[string]*trackingv1.GetPartnerViewRequest{
		"other tenant": {Scope: scopeOf("ten_b"), DeliveryId: delD},
		"unknown":      {Scope: scopeOf("ten_a"), DeliveryId: "del_01jrpc0000000000000000000z"},
	} {
		cctx, info := connect.NewClientContext(ctx)
		if _, err := c.GetPartnerView(cctx, r); connect.CodeOf(err) != connect.CodeNotFound ||
			info.ResponseTrailer().Get(rpcauth.CodeKey) != "not_found" {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, r := range map[string]*trackingv1.GetPartnerViewRequest{
		"no scope": {DeliveryId: delD}, "bad id": {Scope: scopeOf("ten_a"), DeliveryId: "x"},
	} {
		if _, err := c.GetPartnerView(ctx, r); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestListTrailRPCPagesAndScopes(t *testing.T) {
	c, pool := serve(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	seedTrail(t, pool, delA, base, 5)

	var got []*trackingv1.Fix
	cursor := ""
	for range 4 {
		pg, err := c.ListTrail(ctx, &trackingv1.ListTrailRequest{Scope: scopeOf("ten_a"), DeliveryId: delA, Limit: 2, StartingAfter: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if !pg.GetRawAvailable() || len(pg.GetSimplified()) != 0 {
			t.Fatalf("page = %v", pg)
		}
		got = append(got, pg.GetFixes()...)
		if !pg.GetHasMore() {
			break
		}
		cursor = pg.GetNextCursor()
	}
	if len(got) != 5 {
		t.Fatalf("fixes = %d, want 5", len(got))
	}
	for i, f := range got {
		if !f.GetT().AsTime().Equal(base.Add(time.Duration(i)*time.Second)) || f.GetSource() != "relay" || f.GetAccuracyM() != 5 {
			t.Fatalf("fix %d = %v", i, f)
		}
	}
	// Default limit: one page.
	pg, err := c.ListTrail(ctx, &trackingv1.ListTrailRequest{Scope: scopeOf("ten_a"), DeliveryId: delA})
	if err != nil || len(pg.GetFixes()) != 5 || pg.GetHasMore() || pg.GetNextCursor() != "" {
		t.Fatalf("default page = %v, %v", pg, err)
	}

	for name, r := range map[string]*trackingv1.ListTrailRequest{
		"other tenant": {Scope: scopeOf("ten_b"), DeliveryId: delA},
		"unknown":      {Scope: scopeOf("ten_a"), DeliveryId: "del_01jrpc0000000000000000000z"},
	} {
		if _, err := c.ListTrail(ctx, r); connect.CodeOf(err) != connect.CodeNotFound {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, r := range map[string]*trackingv1.ListTrailRequest{
		"limit too big":  {Scope: scopeOf("ten_a"), DeliveryId: delA, Limit: 1001},
		"limit negative": {Scope: scopeOf("ten_a"), DeliveryId: delA, Limit: -1},
		"bad cursor":     {Scope: scopeOf("ten_a"), DeliveryId: delA, StartingAfter: "zzz"},
		"bad id":         {Scope: scopeOf("ten_a"), DeliveryId: "x"},
	} {
		cctx, info := connect.NewClientContext(ctx)
		if _, err := c.ListTrail(cctx, r); connect.CodeOf(err) != connect.CodeInvalidArgument ||
			info.ResponseTrailer().Get(rpcauth.CodeKey) != "validation_failed" {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestListTrailRPCAfterRetentionIsSimplified(t *testing.T) {
	c, pool := serve(t)
	ctx := context.Background()
	seedTrail(t, pool, delA, time.Now().Add(-time.Hour), 3)
	inScope(t, pool, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM trail_fixes WHERE delivery_id = $1`, delA); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO simplified_routes (delivery_id, tenant_id, environment, points, created_at)
			VALUES ($1, 'ten_a', 'test', '[[52.36,4.9],[52.37,4.91]]', now())`, delA)
		return err
	})
	pg, err := c.ListTrail(ctx, &trackingv1.ListTrailRequest{Scope: scopeOf("ten_a"), DeliveryId: delA})
	if err != nil {
		t.Fatal(err)
	}
	if pg.GetRawAvailable() || len(pg.GetFixes()) != 0 || pg.GetHasMore() || len(pg.GetSimplified()) != 2 ||
		pg.GetSimplified()[1].GetLat() != 52.37 || pg.GetSimplified()[1].GetLng() != 4.91 {
		t.Fatalf("page = %v", pg)
	}
}

func TestListTrailRPCErasedHidesRawFixes(t *testing.T) {
	c, pool := serve(t)
	ctx := context.Background()
	seedTrail(t, pool, delA, time.Now().Add(-time.Hour), 2)
	inScope(t, pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE deliveries SET erased_at = now() WHERE id = $1`, delA)
		return err
	})
	pg, err := c.ListTrail(ctx, &trackingv1.ListTrailRequest{Scope: scopeOf("ten_a"), DeliveryId: delA})
	if err != nil || pg.GetRawAvailable() || len(pg.GetFixes()) != 0 {
		t.Fatalf("page = %v, %v: raw fixes of an erased delivery must not be served", pg, err)
	}
}

func TestListProofPointsRPC(t *testing.T) {
	c, pool := serve(t)
	ctx := context.Background()
	at := time.Now().Add(-time.Minute).Truncate(time.Millisecond)
	inScope(t, pool, func(ctx context.Context, tx pgx.Tx) error {
		p, err := postgres.New(pool).Bind(ctx, tx, app.Scope{TenantID: "ten_a", Env: "test"})
		if err != nil {
			return err
		}
		fix := domain.Fix{T: at.Add(-5 * time.Second), Pos: domain.LatLng{Lat: 52.361, Lng: 4.901}, AccuracyM: 6}
		if err := p.PutProofPoint(ctx, app.ProofPoint{DeliveryID: delA, Status: "at_pickup", At: at, Actor: "courier", Fix: &fix}); err != nil {
			return err
		}
		return p.PutProofPoint(ctx, app.ProofPoint{DeliveryID: delA, Status: "delivered", At: at.Add(time.Minute), Actor: "partner"})
	})
	res, err := c.ListProofPoints(ctx, &trackingv1.ListProofPointsRequest{Scope: scopeOf("ten_a"), DeliveryId: delA})
	if err != nil {
		t.Fatal(err)
	}
	pts := res.GetPoints()
	if len(pts) != 2 || pts[0].GetStatus() != "at_pickup" || pts[0].GetActor() != "courier" ||
		pts[0].GetPosition().GetLat() != 52.361 || pts[0].GetAccuracyM() != 6 ||
		!pts[0].GetPositionAt().AsTime().Equal(at.Add(-5*time.Second)) || !pts[0].GetAt().AsTime().Equal(at) {
		t.Fatalf("points = %v", pts)
	}
	if pts[1].GetStatus() != "delivered" || pts[1].GetPosition() != nil || pts[1].GetPositionAt() != nil || pts[1].AccuracyM != nil {
		t.Fatalf("point without a position = %v", pts[1])
	}
	if _, err := c.ListProofPoints(ctx, &trackingv1.ListProofPointsRequest{Scope: scopeOf("ten_b"), DeliveryId: delA}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("other tenant: %v", err)
	}
	if _, err := c.ListProofPoints(ctx, &trackingv1.ListProofPointsRequest{Scope: scopeOf("ten_a"), DeliveryId: "x"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad id: %v", err)
	}
}
