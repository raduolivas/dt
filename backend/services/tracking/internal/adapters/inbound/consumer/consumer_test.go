package consumer_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	goredis "github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/bootstrap"
	"github.com/raduolivas/dt/backend/pkg/migratex"
	"github.com/raduolivas/dt/backend/pkg/outbox"
	"github.com/raduolivas/dt/backend/pkg/testkit"
	"github.com/raduolivas/dt/backend/services/tracking/internal/adapters/inbound/consumer"
	"github.com/raduolivas/dt/backend/services/tracking/internal/adapters/outbound/postgres"
	"github.com/raduolivas/dt/backend/services/tracking/internal/adapters/outbound/redis"
	"github.com/raduolivas/dt/backend/services/tracking/internal/app"
	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
	"github.com/raduolivas/dt/backend/services/tracking/migrations"
)

type clock struct{}

func (clock) Now() time.Time { return time.Now() }

type stack struct {
	pool   *pgxpool.Pool
	rdb    *goredis.Client
	js     jetstream.JetStream
	repo   *postgres.Repo
	states *redis.States
}

func start(t *testing.T) stack {
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
	nc, err := nats.Connect(testkit.StartNATS(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []jetstream.StreamConfig{outbox.StreamDeliveries, outbox.StreamAccounts} {
		if err := outbox.EnsureStream(ctx, js, s); err != nil {
			t.Fatal(err)
		}
	}
	repo := postgres.New(pool)
	states := redis.NewStates(rdb, repo.CourierActiveDeliveries)
	log := slog.New(slog.DiscardHandler)
	svc := &app.Service{States: states, Reads: repo, Trail: postgres.NewTrailWriter(pool, log),
		Pub: redis.NewPublisher(rdb), Events: repo, Sampler: states, Clock: clock{}, Log: log}

	cctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 2)
	go func() {
		done <- outbox.Consume(cctx, js, pool, log, consumer.DeliveriesStream, consumer.DeliveriesConfig,
			consumer.Deliveries(repo, svc, log))
	}()
	go func() {
		done <- outbox.Consume(cctx, js, pool, log, consumer.AccountsStream, consumer.AccountsConfig,
			consumer.Settings(repo, svc, log))
	}()
	t.Cleanup(func() {
		cancel()
		for range 2 {
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("Consume: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("Consume did not stop")
			}
		}
	})
	return stack{pool: pool, rdb: rdb, js: js, repo: repo, states: states}
}

// publish sends env on subject; msgID (if not empty) is the JetStream
// de-duplication id, so a repeat with a fresh msgID reaches the consumer.
func publish(t *testing.T, js jetstream.JetStream, subject, msgID string, env *eventsv1.Envelope) {
	t.Helper()
	data, err := proto.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	msg := nats.NewMsg(subject)
	msg.Data = data
	var opts []jetstream.PublishOpt
	if msgID != "" {
		opts = append(opts, jetstream.WithMsgID(msgID))
	}
	if _, err := js.PublishMsg(context.Background(), msg, opts...); err != nil {
		t.Fatal(err)
	}
}

func envelope(t *testing.T, typ string, v uint64, delID string, payload proto.Message, at time.Time) *eventsv1.Envelope {
	t.Helper()
	e, err := outbox.NewEnvelope(typ, "ten_c", eventsv1.Environment_ENVIRONMENT_TEST, "deliveries",
		&eventsv1.Subject{DeliveryId: delID, DeliveryVersion: v}, payload, at)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func waitInbox(t *testing.T, pool *pgxpool.Pool, n int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var c int
		if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM inbox`).Scan(&c); err != nil {
			t.Fatal(err)
		}
		if c >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("inbox has %d rows, want %d", c, n)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestConsumersAreIdempotent(t *testing.T) {
	s := start(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	const del, cou = "del_01jc0nsumer0000000000000000", "cou_01jc0nsumer0000000000000000"
	sc := app.Scope{TenantID: "ten_c", Env: "test"}

	created := envelope(t, "delivery.created", 1, del, &eventsv1.DeliveryCreated{
		DeliveryId: del, DeliveryModel: "end_to_end", Status: "confirmed",
		Pickup: &eventsv1.LatLng{Lat: 52.368, Lng: 4.903}, PickupName: "Hub",
		Dropoff: &eventsv1.LatLng{Lat: 52.374, Lng: 4.889}, CreatedAt: timestamppb.New(now),
	}, now)
	upserted := envelope(t, "courier.upserted", 0, "", &eventsv1.CourierUpserted{
		CourierId: cou, ExternalId: "ext-1", DisplayName: "Sam", Vehicle: "e_bike",
	}, now)
	assigned := envelope(t, "courier.assigned", 2, del, &eventsv1.CourierAssigned{
		DeliveryId: del, CourierId: cou, AssignedAt: timestamppb.New(now.Add(time.Second)),
	}, now)
	toAssigned := envelope(t, "delivery.status_changed", 2, del, &eventsv1.DeliveryStatusChanged{
		DeliveryId: del, From: "confirmed", To: "courier_assigned", Actor: "partner", OccurredAt: timestamppb.New(now.Add(time.Second)),
	}, now)
	atPickup := envelope(t, "delivery.status_changed", 3, del, &eventsv1.DeliveryStatusChanged{
		DeliveryId: del, From: "courier_assigned", To: "at_pickup", Actor: "courier", OccurredAt: timestamppb.New(now.Add(2 * time.Second)),
	}, now)
	prep := int32(20)
	settings, err := outbox.NewEnvelope("tenant.settings_set", "ten_c", eventsv1.Environment_ENVIRONMENT_UNSPECIFIED, "accounts", nil,
		&eventsv1.TenantSettingsSet{TenantId: "ten_c", AllowedModels: []string{"end_to_end"}, DefaultModel: "end_to_end",
			PickupConfirmation: "any", UpdatedAt: timestamppb.New(now), DefaultPrepMinutes: &prep}, now)
	if err != nil {
		t.Fatal(err)
	}

	publish(t, s.js, "deliveries.delivery.created", created.GetEventId(), created)
	publish(t, s.js, "deliveries.courier.upserted", upserted.GetEventId(), upserted)
	publish(t, s.js, "deliveries.courier.assigned", assigned.GetEventId(), assigned)
	publish(t, s.js, "deliveries.delivery.status_changed", toAssigned.GetEventId(), toAssigned)
	publish(t, s.js, "deliveries.delivery.status_changed", atPickup.GetEventId(), atPickup)
	publish(t, s.js, "accounts.tenant.settings_set", settings.GetEventId(), settings)
	waitInbox(t, s.pool, 6)

	st1, ok, err := s.states.Get(ctx, del)
	if err != nil || !ok {
		t.Fatalf("state: %v %v", ok, err)
	}
	if st1.Status != domain.StatusAtPickup || st1.Courier == nil || st1.Courier.DisplayName != "Sam" ||
		st1.CourierVisibleSince == nil || st1.DefaultPrepMinutes == nil || *st1.DefaultPrepMinutes != 20 || len(st1.Timeline) != 3 {
		t.Fatalf("state after events: %+v", st1)
	}
	ids, err := s.states.ActiveDeliveriesForCourier(ctx, "ten_c", "test", cou)
	if err != nil || len(ids) != 1 || ids[0] != del {
		t.Fatalf("active = %v, %v", ids, err)
	}

	// The same events again, with fresh JetStream message ids so they are
	// delivered: the inbox absorbs them.
	for _, e := range []*eventsv1.Envelope{created, upserted, assigned, toAssigned, atPickup} {
		publish(t, s.js, "deliveries."+e.GetType(), "", e)
	}
	publish(t, s.js, "accounts.tenant.settings_set", "", settings)
	// A marker event after them proves they were all consumed (ordered consumer).
	marker := envelope(t, "delivery.updated", 4, del, &eventsv1.DeliveryUpdated{DeliveryId: del}, now)
	publish(t, s.js, "deliveries.delivery.updated", marker.GetEventId(), marker)
	waitInbox(t, s.pool, 7)
	time.Sleep(300 * time.Millisecond) // let the accounts consumer catch up too

	st2, _, _ := s.states.Get(ctx, del)
	if st2.Version != st1.Version || len(st2.Timeline) != 3 {
		t.Fatalf("duplicates changed the state: version %d->%d timeline %d", st1.Version, st2.Version, len(st2.Timeline))
	}
	var proofs, inbox int
	err = s.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM inbox)`).Scan(&inbox)
	if err != nil || inbox != 7 {
		t.Fatalf("inbox = %d, %v", inbox, err)
	}
	pts, err := s.repo.ProofPoints(ctx, sc, del)
	if err != nil {
		t.Fatal(err)
	}
	proofs = len(pts)
	if proofs != 1 || pts[0].Status != "at_pickup" || pts[0].Actor != "courier" || pts[0].Fix != nil {
		t.Fatalf("proof points = %+v", pts)
	}

	// Redis lost: the state is rebuilt from the read model on the next change.
	if err := s.rdb.FlushAll(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	pickedUp := envelope(t, "delivery.status_changed", 5, del, &eventsv1.DeliveryStatusChanged{
		DeliveryId: del, From: "at_pickup", To: "picked_up", Actor: "partner", OccurredAt: timestamppb.New(now.Add(3 * time.Second)),
	}, now)
	publish(t, s.js, "deliveries.delivery.status_changed", pickedUp.GetEventId(), pickedUp)
	waitInbox(t, s.pool, 8)
	st3, ok, _ := s.states.Get(ctx, del)
	if !ok || st3.Status != domain.StatusPickedUp || st3.Version <= st1.Version || st3.ViewerVersion <= st1.ViewerVersion ||
		st3.DefaultPrepMinutes == nil || st3.Courier == nil {
		t.Fatalf("rebuilt state: %+v", st3)
	}
}
