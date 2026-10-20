package consumer_test

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/migratex"
	"github.com/raduolivas/dt/backend/pkg/outbox"
	"github.com/raduolivas/dt/backend/pkg/testkit"
	"github.com/raduolivas/dt/backend/services/gateway/internal/adapters/inbound/consumer"
	"github.com/raduolivas/dt/backend/services/gateway/internal/adapters/outbound/postgres"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
	"github.com/raduolivas/dt/backend/services/gateway/migrations"
)

// t0 has microsecond precision, like timestamptz.
var t0 = time.Date(2026, 10, 20, 12, 0, 0, 123456000, time.UTC)

const lookup = "AAAAAAAAAAAA"

// starting limits concurrent container starts: rootless Docker's port
// forwarder fails with "address already in use" when many start at once.
var starting = make(chan struct{}, 2)

type env struct {
	pool  *pgxpool.Pool
	js    jetstream.JetStream
	store *postgres.Store
}

// setup starts Postgres (gateway migrations applied) and NATS (ACCOUNTS
// stream created), and runs the gateway-apikeys consumer until the test ends.
func setup(t *testing.T) *env {
	t.Helper()
	pg, nurl := func() (*testkit.Postgres, string) {
		starting <- struct{}{}
		defer func() { <-starting }()
		return testkit.StartPostgres(t), testkit.StartNATS(t)
	}()
	ctx := context.Background()
	url := pg.URL("gateway")
	if err := migratex.Up(ctx, url, migrations.FS); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	nc, err := nats.Connect(nurl)
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
	return &env{pool: pool, js: js, store: postgres.New(pool)}
}

func (e *env) run(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- outbox.Consume(ctx, e.js, e.pool, slog.New(slog.DiscardHandler), consumer.APIKeysStream,
			consumer.APIKeysConfig, consumer.APIKeys(e.store))
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

func (e *env) publish(t *testing.T, ev *eventsv1.Envelope, msgID string) {
	t.Helper()
	b, err := proto.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	msg := nats.NewMsg(outbox.SubjectFor(outbox.StreamAccounts, ev.GetType()))
	msg.Data = b
	if _, err := e.js.PublishMsg(context.Background(), msg, jetstream.WithMsgID(msgID)); err != nil {
		t.Fatal(err)
	}
}

// waitAcked waits until the consumer has acked n messages.
func (e *env) waitAcked(t *testing.T, n uint64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		c, err := e.js.Consumer(context.Background(), consumer.APIKeysStream, consumer.APIKeysConfig.Durable)
		if err == nil {
			info, err := c.Info(context.Background())
			if err == nil && info.AckFloor.Stream == n && info.NumAckPending == 0 {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d acked messages", n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (e *env) count(t *testing.T, query string) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func envelope(t *testing.T, eventType string, payload proto.Message) *eventsv1.Envelope {
	t.Helper()
	ev, err := outbox.NewEnvelope(eventType, "ten_01", eventsv1.Environment_ENVIRONMENT_LIVE, "accounts",
		nil, payload, t0)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func created(t *testing.T) *eventsv1.Envelope {
	return envelope(t, consumer.EventKeyCreated, &eventsv1.ApiKeyCreated{
		KeyId: "ak_1", TenantId: "ten_01", Environment: eventsv1.Environment_ENVIRONMENT_LIVE, Lookup: lookup,
		Hash: []byte{1, 2, 3}, Scopes: []string{"deliveries:read", "webhooks:write"},
		AllowedIps: []string{"203.0.113.0/24", "2001:db8::1/128"}, CreatedAt: timestamppb.New(t0),
	})
}

func revoked(t *testing.T, at time.Time) *eventsv1.Envelope {
	return envelope(t, consumer.EventKeyRevoked, &eventsv1.ApiKeyRevoked{KeyId: "ak_1", RevokedAt: timestamppb.New(at)})
}

func TestConsumerAppliesEachEventOnce(t *testing.T) {
	t.Parallel()
	e := setup(t)
	c := created(t)
	e.publish(t, c, c.GetEventId())
	e.publish(t, c, "redelivered-"+c.GetEventId()) // same event, past the duplicate window
	again := created(t)                            // same key, a new event id (a replay)
	e.publish(t, again, again.GetEventId())
	e.run(t)
	e.waitAcked(t, 3)

	if n := e.count(t, `SELECT count(*) FROM api_keys`); n != 1 {
		t.Fatalf("api_keys has %d rows, want 1", n)
	}
	if n := e.count(t, `SELECT count(*) FROM inbox WHERE consumer = 'gateway-apikeys'`); n != 2 {
		t.Fatalf("inbox has %d rows, want 2 (the duplicate is absorbed)", n)
	}
	got, err := e.store.FindByLookup(context.Background(), lookup)
	if err != nil {
		t.Fatal(err)
	}
	want := domain.KeyRecord{
		ID: "ak_1", TenantID: "ten_01", Lookup: lookup, Env: domain.EnvLive, Hash: []byte{1, 2, 3},
		Scopes:     []domain.Scope{domain.ScopeDeliveriesRead, domain.ScopeWebhooksWrite},
		AllowedIPs: []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("2001:db8::1/128")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("key = %+v, want %+v", got, want)
	}

	r := revoked(t, t0)
	e.publish(t, r, r.GetEventId())
	e.publish(t, r, "redelivered-"+r.GetEventId())
	e.waitAcked(t, 5)
	got, err = e.store.FindByLookup(context.Background(), lookup)
	if err != nil {
		t.Fatal(err)
	}
	if got.RevokedAt == nil || !got.RevokedAt.Equal(t0) {
		t.Fatalf("revoked_at = %v, want %v", got.RevokedAt, t0)
	}
}

func TestConsumerKeepsOrderCreateThenRevoke(t *testing.T) {
	t.Parallel()
	e := setup(t)
	e.run(t)
	exp := t0.Add(24 * time.Hour)
	events := []*eventsv1.Envelope{
		created(t),
		envelope(t, consumer.EventKeyExpirySet, &eventsv1.ApiKeyExpirySet{KeyId: "ak_1", ExpiresAt: timestamppb.New(exp)}),
		revoked(t, t0.Add(time.Minute)),
		envelope(t, "apikey.renamed", &eventsv1.ApiKeyRevoked{KeyId: "ak_1"}), // unknown: acked, ignored
	}
	for _, ev := range events {
		e.publish(t, ev, ev.GetEventId())
	}
	e.waitAcked(t, uint64(len(events)))

	got, err := e.store.FindByLookup(context.Background(), lookup)
	if err != nil {
		t.Fatal(err)
	}
	if got.RevokedAt == nil || !got.RevokedAt.Equal(t0.Add(time.Minute)) {
		t.Errorf("revoked_at = %v, want %v", got.RevokedAt, t0.Add(time.Minute))
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(exp) {
		t.Errorf("expires_at = %v, want %v", got.ExpiresAt, exp)
	}
	if got.Usable(t0) {
		t.Error("the key is still usable after create then revoke")
	}

	c, err := e.js.Consumer(context.Background(), "ACCOUNTS", "gateway-apikeys")
	if err != nil {
		t.Fatal(err)
	}
	cfg := c.CachedInfo().Config
	if cfg.FilterSubject != "accounts.apikey.>" || cfg.DeliverPolicy != jetstream.DeliverAllPolicy ||
		cfg.AckPolicy != jetstream.AckExplicitPolicy || cfg.MaxAckPending != 1 || cfg.AckWait != 30*time.Second {
		t.Errorf("consumer config = %+v", cfg)
	}
}

// fakeWriter fails the test if the handler writes: these events are rejected
// before any write.
type fakeWriter struct{ t *testing.T }

func (f fakeWriter) InsertKey(context.Context, pgx.Tx, domain.KeyRecord) error {
	f.t.Error("unexpected InsertKey")
	return nil
}

func (f fakeWriter) SetExpiry(context.Context, pgx.Tx, string, time.Time) error {
	f.t.Error("unexpected SetExpiry")
	return nil
}

func (f fakeWriter) Revoke(context.Context, pgx.Tx, string, time.Time) error {
	f.t.Error("unexpected Revoke")
	return nil
}

func TestHandlerRejectsPoisonEventsPermanently(t *testing.T) {
	t.Parallel()
	h := consumer.APIKeys(fakeWriter{t})
	good := &eventsv1.ApiKeyCreated{
		KeyId: "ak_1", TenantId: "ten_01", Environment: eventsv1.Environment_ENVIRONMENT_TEST, Lookup: lookup,
		Hash: []byte{1}, Scopes: []string{"deliveries:read"},
	}
	mod := func(f func(p *eventsv1.ApiKeyCreated)) *eventsv1.ApiKeyCreated {
		p := proto.Clone(good).(*eventsv1.ApiKeyCreated)
		f(p)
		return p
	}
	cases := []struct {
		name, eventType string
		payload         proto.Message
	}{
		{"created with a revoked payload", consumer.EventKeyCreated, &eventsv1.ApiKeyRevoked{KeyId: "ak_1"}},
		{"revoked with a created payload", consumer.EventKeyRevoked, good},
		{"expiry_set with a created payload", consumer.EventKeyExpirySet, good},
		{"created without key id", consumer.EventKeyCreated, mod(func(p *eventsv1.ApiKeyCreated) { p.KeyId = "" })},
		{"created without lookup", consumer.EventKeyCreated, mod(func(p *eventsv1.ApiKeyCreated) { p.Lookup = "" })},
		{"created without hash", consumer.EventKeyCreated, mod(func(p *eventsv1.ApiKeyCreated) { p.Hash = nil })},
		{"created without tenant", consumer.EventKeyCreated, mod(func(p *eventsv1.ApiKeyCreated) { p.TenantId = "" })},
		{"created with unknown env", consumer.EventKeyCreated, mod(func(p *eventsv1.ApiKeyCreated) {
			p.Environment = eventsv1.Environment_ENVIRONMENT_UNSPECIFIED
		})},
		{"created with a bad CIDR", consumer.EventKeyCreated, mod(func(p *eventsv1.ApiKeyCreated) {
			p.AllowedIps = []string{"not-a-cidr"}
		})},
		{"expiry_set without expiry", consumer.EventKeyExpirySet, &eventsv1.ApiKeyExpirySet{KeyId: "ak_1"}},
		{"expiry_set without key id", consumer.EventKeyExpirySet,
			&eventsv1.ApiKeyExpirySet{ExpiresAt: timestamppb.New(t0)}},
		{"revoked without key id", consumer.EventKeyRevoked, &eventsv1.ApiKeyRevoked{RevokedAt: timestamppb.New(t0)}},
	}
	for _, tc := range cases {
		packed, err := anypb.New(tc.payload)
		if err != nil {
			t.Fatal(err)
		}
		ev := &eventsv1.Envelope{EventId: "evt_1", Type: tc.eventType, Payload: packed}
		if err := h(context.Background(), nil, ev); !errors.Is(err, outbox.ErrPermanent) {
			t.Errorf("%s: err = %v, want outbox.ErrPermanent", tc.name, err)
		}
	}
	if err := h(context.Background(), nil, &eventsv1.Envelope{EventId: "evt_1", Type: consumer.EventKeyRevoked}); !errors.Is(err, outbox.ErrPermanent) {
		t.Errorf("missing payload: err = %v, want outbox.ErrPermanent", err)
	}
	if err := h(context.Background(), nil, &eventsv1.Envelope{EventId: "evt_1", Type: "tenant.created"}); err != nil {
		t.Errorf("unknown type: err = %v, want nil (acked and ignored)", err)
	}
}

// recordingWriter records what the handler writes.
type recordingWriter struct {
	revokedAt time.Time
	inserted  domain.KeyRecord
	err       error
}

func (r *recordingWriter) InsertKey(_ context.Context, _ pgx.Tx, k domain.KeyRecord) error {
	r.inserted = k
	return r.err
}

func (r *recordingWriter) SetExpiry(context.Context, pgx.Tx, string, time.Time) error { return r.err }

func (r *recordingWriter) Revoke(_ context.Context, _ pgx.Tx, _ string, at time.Time) error {
	r.revokedAt = at
	return r.err
}

func TestHandlerRevokeFallsBackToOccurredAtAndKeepsStoreErrorsTransient(t *testing.T) {
	t.Parallel()
	w := &recordingWriter{}
	h := consumer.APIKeys(w)
	ev := envelope(t, consumer.EventKeyRevoked, &eventsv1.ApiKeyRevoked{KeyId: "ak_1"})
	if err := h(context.Background(), nil, ev); err != nil {
		t.Fatal(err)
	}
	if !w.revokedAt.Equal(t0) {
		t.Errorf("revoked at %v, want the envelope's occurred_at %v", w.revokedAt, t0)
	}
	ev.OccurredAt = nil
	if err := h(context.Background(), nil, ev); !errors.Is(err, outbox.ErrPermanent) {
		t.Errorf("no time at all: err = %v, want outbox.ErrPermanent", err)
	}

	w.err = errors.New("connection reset")
	for _, ev := range []*eventsv1.Envelope{created(t), revoked(t, t0),
		envelope(t, consumer.EventKeyExpirySet, &eventsv1.ApiKeyExpirySet{KeyId: "ak_1", ExpiresAt: timestamppb.New(t0)}),
	} {
		err := h(context.Background(), nil, ev)
		if !errors.Is(err, w.err) || errors.Is(err, outbox.ErrPermanent) {
			t.Errorf("%s: err = %v, want the transient store error", ev.GetType(), err)
		}
	}
	if w.inserted.ID != "ak_1" || len(w.inserted.AllowedIPs) != 2 {
		t.Errorf("inserted = %+v", w.inserted)
	}
}
