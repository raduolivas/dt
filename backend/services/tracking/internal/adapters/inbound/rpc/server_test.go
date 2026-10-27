package rpc_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	trackingv1 "github.com/raduolivas/dt/backend/gen/dt/tracking/v1"
	"github.com/raduolivas/dt/backend/gen/dt/tracking/v1/trackingv1connect"
	"github.com/raduolivas/dt/backend/pkg/bootstrap"
	"github.com/raduolivas/dt/backend/pkg/migratex"
	"github.com/raduolivas/dt/backend/pkg/rpcauth"
	"github.com/raduolivas/dt/backend/pkg/testkit"
	"github.com/raduolivas/dt/backend/services/tracking/internal/adapters/inbound/rpc"
	"github.com/raduolivas/dt/backend/services/tracking/internal/adapters/outbound/postgres"
	"github.com/raduolivas/dt/backend/services/tracking/internal/adapters/outbound/redis"
	"github.com/raduolivas/dt/backend/services/tracking/internal/app"
	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
	"github.com/raduolivas/dt/backend/services/tracking/migrations"
)

const (
	testToken = "test-only-internal-rpc-token-0123456789"
	courierA  = "cou_01jrpc0000000000000000000a"
	delA      = "del_01jrpc0000000000000000000a"
	// courierB has no couriers row (its courier.upserted aged out of the
	// stream) but is assigned to delB.
	courierB = "cou_01jrpc0000000000000000000b"
	delB     = "del_01jrpc0000000000000000000b"
	// delD is a Direct delivery, still confirmed, with courierA assigned: the
	// customer's snapshot hides the courier, the partner's view does not.
	delD = "del_01jrpc0000000000000000000d"
)

type clock struct{}

func (clock) Now() time.Time { return time.Now() }

type bearer struct{ c *http.Client }

func (b bearer) Do(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", "Bearer "+testToken)
	return b.c.Do(r)
}

func serve(t *testing.T) (trackingv1connect.TrackingServiceClient, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	pg := testkit.StartPostgres(t)
	if err := migratex.Up(ctx, pg.MigratorURL("tracking"), migrations.FS); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, pg.URL("tracking"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ropts, err := bootstrap.RedisOptions(testkit.StartRedis(t))
	if err != nil {
		t.Fatal(err)
	}
	rdb := goredis.NewClient(ropts)
	t.Cleanup(func() { _ = rdb.Close() })
	repo := postgres.New(pool)
	if err := repo.EnsurePartitions(ctx, time.Now(), 1); err != nil {
		t.Fatal(err)
	}
	states := redis.NewStates(rdb, repo.CourierActiveDeliveries)
	log := slog.New(slog.DiscardHandler)
	svc := &app.Service{States: states, Reads: repo, Trail: postgres.NewTrailWriter(pool, log),
		Partner: repo, Pub: redis.NewPublisher(rdb), Events: repo, Sampler: states, Clock: clock{}, Log: log}
	mux := http.NewServeMux()
	if err := rpc.Mount(mux, svc, nil, testToken, log); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Tenant A's courier with one active delivery.
	sc := app.Scope{TenantID: "ten_a", Env: "test"}
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		p, err := repo.Bind(ctx, tx, sc)
		if err != nil {
			return err
		}
		if err := svc.OnCourierUpserted(ctx, p, sc, domain.Courier{ID: courierA, ExternalID: "x", DisplayName: "Sam", Vehicle: "car"}); err != nil {
			return err
		}
		now := time.Now()
		if err := svc.OnDeliveryCreated(ctx, p, sc, 1, app.DeliveryCreated{ID: delA, Model: domain.ModelEndToEnd,
			Status: domain.StatusConfirmed, Pickup: domain.LatLng{Lat: 52.36, Lng: 4.9}, PickupName: "Hub",
			Dropoff: domain.LatLng{Lat: 52.37, Lng: 4.89}, CreatedAt: now}); err != nil {
			return err
		}
		if err := svc.OnCourierAssigned(ctx, p, sc, 2, delA, courierA, now); err != nil {
			return err
		}
		if err := svc.OnDeliveryCreated(ctx, p, sc, 1, app.DeliveryCreated{ID: delB, Model: domain.ModelEndToEnd,
			Status: domain.StatusConfirmed, Pickup: domain.LatLng{Lat: 52.36, Lng: 4.9}, PickupName: "Hub",
			Dropoff: domain.LatLng{Lat: 52.37, Lng: 4.89}, CreatedAt: now}); err != nil {
			return err
		}
		if err := svc.OnCourierAssigned(ctx, p, sc, 2, delB, courierB, now); err != nil {
			return err
		}
		if err := svc.OnDeliveryCreated(ctx, p, sc, 1, app.DeliveryCreated{ID: delD, Model: domain.ModelDirect,
			Status: domain.StatusConfirmed, Pickup: domain.LatLng{Lat: 52.36, Lng: 4.9}, PickupName: "Hub",
			Dropoff: domain.LatLng{Lat: 52.37, Lng: 4.89}, CreatedAt: now}); err != nil {
			return err
		}
		return svc.OnCourierAssigned(ctx, p, sc, 2, delD, courierA, now)
	})
	if err != nil {
		t.Fatal(err)
	}
	c := trackingv1connect.NewTrackingServiceClient(connect.NewClient(connecthttp.NewTransport(bearer{srv.Client()}, srv.URL)))
	return c, pool
}

func req(tenant string, courier string, n int) *trackingv1.IngestRequest {
	r := &trackingv1.IngestRequest{
		Scope:     &trackingv1.Scope{TenantId: tenant, Environment: eventsv1.Environment_ENVIRONMENT_TEST, ActorId: "ak_1"},
		CourierId: courier, Source: "relay",
	}
	now := time.Now()
	for i := range n {
		r.Fixes = append(r.Fixes, &trackingv1.Fix{T: timestamppb.New(now.Add(time.Duration(i-n) * time.Second)),
			Lat: 52.361 + float64(i)*0.00001, Lng: 4.9, AccuracyM: 5})
	}
	return r
}

func TestIngestRPCRejectsOtherTenantCourier(t *testing.T) {
	c, _ := serve(t)
	ctx := context.Background()
	// Tenant A's own courier works.
	res, err := c.IngestLocations(ctx, req("ten_a", courierA, 3))
	if err != nil || res.GetAccepted() != 3 || res.GetDropped() != 0 {
		t.Fatalf("own courier: %+v %v", res, err)
	}
	// Tenant B naming A's courier: not found, before and after A's set is cached.
	cctx, info := connect.NewClientContext(ctx)
	_, err = c.IngestLocations(cctx, req("ten_b", courierA, 1))
	if ce, ok := errors.AsType[*connect.Error](err); !ok || ce.Code() != connect.CodeNotFound {
		t.Fatalf("other tenant: %v", err)
	}
	if got := info.ResponseTrailer().Get(rpcauth.CodeKey); got != "not_found" {
		t.Fatalf("dt-code = %q", got)
	}
	// A live-environment scope is another scope too.
	r := req("ten_a", courierA, 1)
	r.Scope.Environment = eventsv1.Environment_ENVIRONMENT_LIVE
	if _, err := c.IngestLocations(ctx, r); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("other environment: %v", err)
	}
	// Unknown courier.
	if _, err := c.IngestLocations(ctx, req("ten_a", "cou_01jrpc00000000000000000zzz", 1)); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown courier: %v", err)
	}
}

func TestIngestRPCValidates(t *testing.T) {
	c, _ := serve(t)
	ctx := context.Background()
	for name, r := range map[string]*trackingv1.IngestRequest{
		"too many fixes": req("ten_a", courierA, rpc.MaxFixes+1),
		"no fixes":       req("ten_a", courierA, 0),
		"bad courier id": req("ten_a", "courier-1", 1),
		"bad source":     func() *trackingv1.IngestRequest { r := req("ten_a", courierA, 1); r.Source = "sdk"; return r }(),
		"no scope":       func() *trackingv1.IngestRequest { r := req("ten_a", courierA, 1); r.Scope = nil; return r }(),
		"no time":        func() *trackingv1.IngestRequest { r := req("ten_a", courierA, 1); r.Fixes[0].T = nil; return r }(),
	} {
		_, err := c.IngestLocations(ctx, r)
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Simulate is not implemented yet.
	if _, err := c.Simulate(ctx, &trackingv1.SimulateRequest{}); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("Simulate: %v", err)
	}
}

func TestIngestAcceptsCourierKnownOnlyFromDelivery(t *testing.T) {
	c, _ := serve(t)
	ctx := context.Background()
	res, err := c.IngestLocations(ctx, req("ten_a", courierB, 2))
	if err != nil || res.GetAccepted() != 2 {
		t.Fatalf("courier without a couriers row: %+v %v", res, err)
	}
	// Still scoped: another tenant naming it is not found.
	if _, err := c.IngestLocations(ctx, req("ten_b", courierB, 1)); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("other tenant: %v", err)
	}
}

func TestGetSnapshotRPC(t *testing.T) {
	c, _ := serve(t)
	ctx := context.Background()
	scope := func(tenant string) *trackingv1.Scope {
		return &trackingv1.Scope{TenantId: tenant, Environment: eventsv1.Environment_ENVIRONMENT_TEST, ActorId: "tl_1"}
	}
	res, err := c.GetSnapshot(ctx, &trackingv1.GetSnapshotRequest{Scope: scope("ten_a"), DeliveryId: delA})
	if err != nil {
		t.Fatal(err)
	}
	// Exactly the domain snapshot's JSON: decode into it strictly.
	var snap domain.TrackingSnapshot
	dec := json.NewDecoder(bytes.NewReader(res.GetSnapshotJson()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&snap); err != nil {
		t.Fatalf("snapshot_json: %v: %s", err, res.GetSnapshotJson())
	}
	if snap.DeliveryID != delA || snap.Status != domain.StatusConfirmed || snap.Version <= 0 ||
		snap.Pickup.Name != "Hub" || len(snap.Timeline) != 1 {
		t.Fatalf("snapshot = %s", res.GetSnapshotJson())
	}
	// The visibility filter applies: no courier while still confirmed.
	if snap.Courier != nil {
		t.Fatalf("courier = %+v, want hidden", snap.Courier)
	}
	// Never the partner fields.
	for _, k := range []string{`"external_id"`, `"id":"cou_`} {
		if bytes.Contains(res.GetSnapshotJson(), []byte(k)) {
			t.Fatalf("snapshot carries %s: %s", k, res.GetSnapshotJson())
		}
	}

	for name, r := range map[string]*trackingv1.GetSnapshotRequest{
		"other tenant":      {Scope: scope("ten_b"), DeliveryId: delA},
		"other environment": {Scope: &trackingv1.Scope{TenantId: "ten_a", Environment: eventsv1.Environment_ENVIRONMENT_LIVE}, DeliveryId: delA},
		"unknown delivery":  {Scope: scope("ten_a"), DeliveryId: "del_01jrpc0000000000000000000z"},
	} {
		if _, err := c.GetSnapshot(ctx, r); connect.CodeOf(err) != connect.CodeNotFound {
			t.Errorf("%s: %v, want NotFound", name, err)
		}
	}
	for name, r := range map[string]*trackingv1.GetSnapshotRequest{
		"no scope":       {DeliveryId: delA},
		"bad delivery":   {Scope: scope("ten_a"), DeliveryId: "order-1"},
		"empty delivery": {Scope: scope("ten_a")},
	} {
		if _, err := c.GetSnapshot(ctx, r); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v, want InvalidArgument", name, err)
		}
	}
}
