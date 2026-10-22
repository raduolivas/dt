package consumer_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/migratex"
	"github.com/raduolivas/dt/backend/pkg/outbox"
	"github.com/raduolivas/dt/backend/pkg/sealbox"
	"github.com/raduolivas/dt/backend/pkg/testkit"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/adapters/inbound/consumer"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/adapters/outbound/postgres"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/domain"
	"github.com/raduolivas/dt/backend/services/deliveries/migrations"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const testKeys = "k1:AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="

func TestSettingsConsumerAppliesLatest(t *testing.T) {
	ctx := context.Background()
	pg := testkit.StartPostgres(t)
	url := pg.URL("deliveries")
	if err := migratex.Up(ctx, url, migrations.FS); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	box, err := sealbox.ParseKeys(testKeys, "k1")
	if err != nil {
		t.Fatal(err)
	}
	repo := postgres.New(pool, box)

	nc, err := nats.Connect(testkit.StartNATS(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	if err := outbox.EnsureStream(ctx, js, outbox.StreamAccounts); err != nil {
		t.Fatal(err)
	}

	cctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- outbox.Consume(cctx, js, pool, slog.New(slog.DiscardHandler),
			consumer.SettingsStream, consumer.SettingsConfig, consumer.Settings(repo.ApplySettings))
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Consume returned %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Consume did not stop")
		}
	})

	publish := func(models []string, def, policy string, at time.Time) {
		t.Helper()
		// Tenant-level event: environment is UNSPECIFIED.
		env, err := outbox.NewEnvelope("tenant.settings_set", "ten_s", eventsv1.Environment_ENVIRONMENT_UNSPECIFIED,
			"accounts", nil, &eventsv1.TenantSettingsSet{
				TenantId: "ten_s", AllowedModels: models, DefaultModel: def,
				PickupConfirmation: policy, UpdatedAt: timestamppb.New(at),
			}, at)
		if err != nil {
			t.Fatal(err)
		}
		data, err := proto.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		msg := nats.NewMsg("accounts.tenant.settings_set")
		msg.Data = data
		if _, err := js.PublishMsg(ctx, msg, jetstream.WithMsgID(env.GetEventId())); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	publish([]string{"end_to_end"}, "end_to_end", "courier_and_store", now)
	publish([]string{"direct"}, "direct", "store", now.Add(-time.Hour)) // older: ignored

	want := domain.TenantSettings{
		AllowedModels: []domain.Model{domain.ModelEndToEnd}, DefaultModel: domain.ModelEndToEnd,
		PickupPolicy: domain.PolicyCourierAndStore,
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, err := repo.Settings(ctx, "ten_s")
		if err != nil {
			t.Fatal(err)
		}
		if got.PickupPolicy == want.PickupPolicy && got.DefaultModel == want.DefaultModel {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("settings = %+v, want %+v", got, want)
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Let the older event be processed, then check it did not win.
	waitProcessed(t, ctx, pool, 2)
	got, err := repo.Settings(ctx, "ten_s")
	if err != nil {
		t.Fatal(err)
	}
	if got.PickupPolicy != want.PickupPolicy || got.DefaultModel != want.DefaultModel || len(got.AllowedModels) != 1 {
		t.Fatalf("after older event: %+v, want %+v", got, want)
	}
}

func waitProcessed(t *testing.T, ctx context.Context, pool *pgxpool.Pool, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var c int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM inbox`).Scan(&c); err != nil {
			t.Fatal(err)
		}
		if c >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("inbox has %d rows, want %d", c, n)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
