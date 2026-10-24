package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/ids"
	"github.com/raduolivas/dt/backend/pkg/migratex"
	"github.com/raduolivas/dt/backend/pkg/sealbox"
	"github.com/raduolivas/dt/backend/pkg/testkit"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/adapters/outbound/postgres"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/app"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/domain"
	"github.com/raduolivas/dt/backend/services/deliveries/migrations"
)

// t0 has microsecond precision, like timestamptz.
var t0 = time.Date(2026, 10, 22, 12, 0, 0, 123456000, time.UTC)

var (
	scopeA     = app.Scope{TenantID: "ten_a", Env: "test", ActorID: "ak_a"}
	scopeBTest = app.Scope{TenantID: "ten_b", Env: "test", ActorID: "ak_b"}
	scopeALive = app.Scope{TenantID: "ten_a", Env: "live", ActorID: "ak_a"}
)

// testKeys are two 32-byte keys; k2 is current.
const testKeys = "k1:AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=,k2:ICEiIyQlJicoKSorLC0uLzAxMjM0NTY3ODk6Ozw9Pj8="

// starting limits concurrent container starts: rootless Docker's port
// forwarder fails with "address already in use" when many start at once.
var starting = make(chan struct{}, 2)

type env struct {
	svc   *pgxpool.Pool // svc_deliveries: subject to RLS
	admin *pgxpool.Pool // superuser: bypasses RLS, for assertions only
	repo  *postgres.Repo
}

// setup starts Postgres, applies the migrations as mig_deliveries and
// returns pools for the service role and the admin, and a repo.
func setup(t *testing.T) env {
	t.Helper()
	pg := func() *testkit.Postgres {
		starting <- struct{}{}
		defer func() { <-starting }()
		return testkit.StartPostgres(t)
	}()
	ctx := context.Background()
	url := pg.URL("deliveries")
	if err := migratex.Up(ctx, pg.MigratorURL("deliveries"), migrations.FS); err != nil {
		t.Fatal(err)
	}
	svc, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	admin, err := pgxpool.New(ctx, pg.URL("admin"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	box, err := sealbox.ParseKeys(testKeys, "k2")
	if err != nil {
		t.Fatal(err)
	}
	return env{svc: svc, admin: admin, repo: postgres.New(svc, box)}
}

func ptr[T any](v T) *T { return &v }

func fullInput(ext string) domain.NewDeliveryInput {
	return domain.NewDeliveryInput{
		ExternalID: ext,
		Pickup:     domain.Location{Lat: 38.7223, Lng: -9.1393}, PickupName: "Padaria Central",
		Dropoff:      domain.Location{Lat: 38.7369, Lng: -9.1427},
		DropoffLabel: ptr("3rd floor, door B"), CustomerFirstName: ptr("Alice"),
		ItemsSummary: ptr("2x pastel de nata"), Notes: ptr("ring twice please"), ItemCount: ptr(2),
		Metadata: map[string]string{"order_ref": "A-1001", "channel": "app"},
	}
}

// newDelivery builds a confirmed delivery for scope s with settings st.
func newDelivery(t *testing.T, s app.Scope, in domain.NewDeliveryInput, st domain.TenantSettings, at time.Time) (*domain.Delivery, []domain.Event) {
	t.Helper()
	d, evts, err := domain.NewDelivery(ids.New(ids.Delivery), s.TenantID, s.Env, in, st, at)
	if err != nil {
		t.Fatal(err)
	}
	return d, evts
}

func create(t *testing.T, e env, s app.Scope, in domain.NewDeliveryInput, st domain.TenantSettings, at time.Time) *domain.Delivery {
	t.Helper()
	d, evts := newDelivery(t, s, in, st, at)
	if err := e.repo.Create(context.Background(), s, d, evts); err != nil {
		t.Fatal(err)
	}
	return d
}

func upsertCourier(t *testing.T, e env, s app.Scope, ext string) domain.Courier {
	t.Helper()
	c, err := domain.NewCourier(ids.New(ids.Courier), domain.CourierInput{
		ExternalID: ext, DisplayName: "Bruno", PhotoURL: "https://cdn.example/b.png", Vehicle: domain.VehicleEBike,
	}, t0)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := e.repo.UpsertCourier(context.Background(), s, c, []domain.Event{c.UpsertedEvent()})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func mutate(t *testing.T, e env, s app.Scope, id string, fn func(*domain.Delivery) ([]domain.Event, error)) *domain.Delivery {
	t.Helper()
	d, err := e.repo.Mutate(context.Background(), s, id, fn)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func report(r domain.Report) func(*domain.Delivery) ([]domain.Event, error) {
	return func(d *domain.Delivery) ([]domain.Event, error) {
		_, evts, err := d.Report(r)
		return evts, err
	}
}

// exported copies d's exported fields, dropping the unexported list of
// entries appended since load, so loaded and in-memory deliveries compare.
func exported(d *domain.Delivery) domain.Delivery {
	return domain.Delivery{
		ID: d.ID, TenantID: d.TenantID, Env: d.Env, ExternalID: d.ExternalID, Model: d.Model,
		Status: d.Status, Policy: d.Policy, Pickup: d.Pickup, PickupName: d.PickupName, Dropoff: d.Dropoff,
		DropoffLabel: d.DropoffLabel, CustomerFirstName: d.CustomerFirstName, ItemsSummary: d.ItemsSummary,
		Notes: d.Notes, ItemCount: d.ItemCount, Metadata: d.Metadata, Courier: d.Courier, History: d.History,
		ErasedAt: d.ErasedAt, DroppedReports: d.DroppedReports, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
}

type outboxRow struct {
	subject string
	env     *eventsv1.Envelope
	raw     []byte
}

// outboxRows returns the outbox in insertion order (event ids are monotonic).
func outboxRows(t *testing.T, e env) []outboxRow {
	t.Helper()
	rows, err := e.admin.Query(context.Background(),
		`SELECT subject, payload FROM deliveries.outbox ORDER BY event_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []outboxRow
	for rows.Next() {
		var r outboxRow
		if err := rows.Scan(&r.subject, &r.raw); err != nil {
			t.Fatal(err)
		}
		r.env = &eventsv1.Envelope{}
		if err := proto.Unmarshal(r.raw, r.env); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCreateGetRoundTrip(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	st := domain.DefaultTenantSettings()

	d := create(t, e, scopeA, fullInput("ord-1"), st, t0)
	c := upsertCourier(t, e, scopeA, "cour-1")
	mutate(t, e, scopeA, d.ID, func(d *domain.Delivery) ([]domain.Event, error) {
		return d.Assign(domain.CourierRef{ID: c.ID, ExternalID: c.ExternalID, DisplayName: c.DisplayName, Vehicle: c.Vehicle}, t0.Add(time.Second))
	})
	want := mutate(t, e, scopeA, d.ID, report(domain.Report{
		EventID: "dev-1", Type: domain.ReportPickupConfirmed, Actor: domain.ActorPartner, By: domain.ByStore,
		OccurredAt: t0.Add(2 * time.Second), ReceivedAt: t0.Add(3 * time.Second), Reason: "handed over",
	}))

	got, err := e.repo.Get(ctx, scopeA, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.History) != 3 {
		t.Fatalf("history has %d entries, want 3: %+v", len(got.History), got.History)
	}
	if !reflect.DeepEqual(exported(got), exported(want)) {
		t.Errorf("round trip:\n got %+v\nwant %+v", exported(got), exported(want))
	}
	if got.Courier == nil || got.Courier.DisplayName != "Bruno" || got.Courier.Vehicle != domain.VehicleEBike {
		t.Errorf("courier = %+v", got.Courier)
	}

	// The sealed columns hold no plaintext. (Searched as bytea: the brief's
	// convert_from(col, 'LATIN1') errors whenever the ciphertext has a 0x00.)
	for col, plain := range map[string]string{
		"customer_first_name_sealed": "Alice", "notes_sealed": "ring twice",
		"dropoff_label_sealed": "3rd floor", "metadata_sealed": "A-1001",
	} {
		var pos int
		err := e.admin.QueryRow(ctx, fmt.Sprintf(
			`SELECT position(convert_to($1, 'UTF8') in %s) FROM deliveries.deliveries WHERE id = $2`, col),
			plain, d.ID).Scan(&pos)
		if err != nil {
			t.Fatal(err)
		}
		if pos != 0 {
			t.Errorf("%s contains the plaintext %q", col, plain)
		}
	}

	// Sealed values are bound to their delivery and field: moving one to
	// another field makes the delivery unreadable instead of leaking it.
	if _, err := e.admin.Exec(ctx,
		`UPDATE deliveries.deliveries SET notes_sealed = customer_first_name_sealed WHERE id = $1`, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.repo.Get(ctx, scopeA, d.ID); err == nil {
		t.Error("Get opened a sealed value moved to another field")
	}
}

func TestCreateGetRoundTripOptionalFieldsAbsent(t *testing.T) {
	t.Parallel()
	e := setup(t)
	in := domain.NewDeliveryInput{
		ExternalID: "ord-min", Pickup: domain.Location{Lat: 1, Lng: 2}, PickupName: "P",
		Dropoff: domain.Location{Lat: 1.01, Lng: 2.01}, Metadata: map[string]string{},
	}
	want := create(t, e, scopeA, in, domain.DefaultTenantSettings(), t0)
	got, err := e.repo.Get(context.Background(), scopeA, want.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(exported(got), exported(want)) {
		t.Errorf("round trip:\n got %+v\nwant %+v", exported(got), exported(want))
	}
	if got.Metadata == nil {
		t.Error("an empty metadata map came back nil")
	}
}

func TestExternalIDTaken(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	st := domain.DefaultTenantSettings()
	create(t, e, scopeA, fullInput("ord-1"), st, t0)

	d, evts := newDelivery(t, scopeA, fullInput("ord-1"), st, t0)
	if err := e.repo.Create(ctx, scopeA, d, evts); !errors.Is(err, app.ErrExternalIDTaken) {
		t.Fatalf("second create = %v, want ErrExternalIDTaken", err)
	}
	// The failed create wrote nothing to the outbox.
	if n := len(outboxRows(t, e)); n != 1 {
		t.Errorf("outbox has %d rows, want 1", n)
	}
	// The same external id is free in another environment or tenant.
	create(t, e, scopeALive, fullInput("ord-1"), st, t0)
	create(t, e, scopeBTest, fullInput("ord-1"), st, t0)
}

func TestMutateEnqueuesEvents(t *testing.T) {
	t.Parallel()
	e := setup(t)
	st := domain.DefaultTenantSettings()
	st.DefaultModel = domain.ModelEndToEnd

	d := create(t, e, scopeA, fullInput("ord-1"), st, t0)
	c := upsertCourier(t, e, scopeA, "cour-1")
	mutate(t, e, scopeA, d.ID, func(d *domain.Delivery) ([]domain.Event, error) {
		return d.Assign(domain.CourierRef{ID: c.ID}, t0.Add(time.Second))
	})
	// A failing fn writes nothing.
	boom := errors.New("boom")
	if _, err := e.repo.Mutate(context.Background(), scopeA, d.ID, func(d *domain.Delivery) ([]domain.Event, error) {
		evts, _ := d.Cancel("", t0)
		return evts, boom
	}); !errors.Is(err, boom) {
		t.Fatalf("Mutate = %v, want boom", err)
	}

	rows := outboxRows(t, e)
	wantTypes := []string{
		domain.EventDeliveryCreated, domain.EventCourierUpserted,
		domain.EventCourierAssigned, domain.EventStatusChanged,
	}
	if len(rows) != len(wantTypes) {
		t.Fatalf("outbox has %d rows, want %d", len(rows), len(wantTypes))
	}
	for i, r := range rows {
		env := r.env
		if r.subject != "deliveries."+wantTypes[i] || env.GetType() != wantTypes[i] {
			t.Errorf("row %d: subject %q type %q, want %q", i, r.subject, env.GetType(), wantTypes[i])
		}
		if env.GetTenantId() != "ten_a" || env.GetEnvironment() != eventsv1.Environment_ENVIRONMENT_TEST ||
			env.GetSource() != "deliveries" {
			t.Errorf("row %d: envelope %v", i, env)
		}
		for _, plain := range []string{"Alice", "ring twice", "3rd floor", "A-1001"} {
			if bytes.Contains(r.raw, []byte(plain)) {
				t.Errorf("row %d (%s) carries personal data %q", i, env.GetType(), plain)
			}
		}
	}
	for _, i := range []int{0, 2, 3} {
		if rows[i].env.GetSubject().GetDeliveryId() != d.ID {
			t.Errorf("row %d: subject delivery_id = %q", i, rows[i].env.GetSubject().GetDeliveryId())
		}
	}
	if rows[1].env.GetSubject().GetCourierId() != c.ID || rows[2].env.GetSubject().GetCourierId() != c.ID {
		t.Errorf("courier subjects = %v, %v", rows[1].env.GetSubject(), rows[2].env.GetSubject())
	}

	created := &eventsv1.DeliveryCreated{}
	if err := rows[0].env.GetPayload().UnmarshalTo(created); err != nil {
		t.Fatal(err)
	}
	if created.GetDeliveryId() != d.ID || created.GetDeliveryModel() != "end_to_end" ||
		created.GetPickupPolicy() != "any" || created.GetPickup().GetLat() != 38.7223 ||
		!created.GetCreatedAt().AsTime().Equal(t0) {
		t.Errorf("delivery.created payload = %v", created)
	}
	if !rows[0].env.GetOccurredAt().AsTime().Equal(t0) {
		t.Errorf("delivery.created occurred_at = %v", rows[0].env.GetOccurredAt().AsTime())
	}
	changed := &eventsv1.DeliveryStatusChanged{}
	if err := rows[3].env.GetPayload().UnmarshalTo(changed); err != nil {
		t.Fatal(err)
	}
	if changed.GetFrom() != "confirmed" || changed.GetTo() != "courier_assigned" || changed.GetActor() != "partner" {
		t.Errorf("delivery.status_changed payload = %v", changed)
	}
}

func TestUpsertCourierKeepsID(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	first := upsertCourier(t, e, scopeA, "cour-1")

	c, err := domain.NewCourier(ids.New(ids.Courier), domain.CourierInput{
		ExternalID: "cour-1", DisplayName: "Bruno S.", Vehicle: domain.VehicleCar,
	}, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	got, created, err := e.repo.UpsertCourier(ctx, scopeA, c, []domain.Event{c.UpsertedEvent()})
	if err != nil {
		t.Fatal(err)
	}
	if created || got.ID != first.ID || !got.CreatedAt.Equal(t0) || got.DisplayName != "Bruno S." ||
		got.Vehicle != domain.VehicleCar || !got.UpdatedAt.Equal(t0.Add(time.Hour)) {
		t.Errorf("update = %+v created=%v, want id %s kept", got, created, first.ID)
	}
	loaded, err := e.repo.Courier(ctx, scopeA, first.ID)
	if err != nil || !reflect.DeepEqual(loaded, got) {
		t.Errorf("Courier = %+v, %v; want %+v", loaded, err, got)
	}
	// The event names the stored courier, not the discarded new id.
	rows := outboxRows(t, e)
	up := &eventsv1.CourierUpserted{}
	if err := rows[len(rows)-1].env.GetPayload().UnmarshalTo(up); err != nil {
		t.Fatal(err)
	}
	if up.GetCourierId() != first.ID || rows[len(rows)-1].env.GetSubject().GetCourierId() != first.ID {
		t.Errorf("courier.upserted names %q, want %q", up.GetCourierId(), first.ID)
	}
	if _, err := e.repo.Courier(ctx, scopeA, "cou_missing"); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("unknown courier = %v, want ErrNotFound", err)
	}
}

func TestUpdateSerializesConcurrentReports(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	st := domain.DefaultTenantSettings()
	d := create(t, e, scopeA, fullInput("ord-1"), st, t0)

	types := []domain.ReportType{domain.ReportAtPickup, domain.ReportPickupConfirmed, domain.ReportArriving, domain.ReportDelivered}
	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Go(func() {
			at := t0.Add(time.Duration(i) * time.Second)
			_, err := e.repo.Mutate(ctx, scopeA, d.ID, report(domain.Report{
				EventID: fmt.Sprintf("evt-%02d", i), Type: types[i%len(types)], Actor: domain.ActorCourier,
				OccurredAt: at, ReceivedAt: at,
			}))
			if err != nil && !errors.Is(err, domain.ErrInvalidTransition) {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	got, err := e.repo.Get(ctx, scopeA, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	var seqs []int
	if err := e.admin.QueryRow(ctx,
		`SELECT array_agg(seq ORDER BY seq) FROM deliveries.delivery_history WHERE delivery_id = $1`, d.ID).Scan(&seqs); err != nil {
		t.Fatal(err)
	}
	for i, s := range seqs {
		if s != i+1 {
			t.Fatalf("seq is not gap-free: %v", seqs)
		}
	}
	if len(seqs) != len(got.History) || len(seqs) < 2 {
		t.Fatalf("%d seqs, %d history entries", len(seqs), len(got.History))
	}

	// Replaying the history in seq order through the domain gives the
	// stored state and the stored outcomes.
	replay, _, _ := domain.NewDelivery(d.ID, d.TenantID, d.Env, fullInput("ord-1"), st, t0)
	for _, h := range got.History[1:] {
		o, _, err := replay.Report(domain.Report{
			EventID: h.EventID, Type: domain.ReportType(h.Type), Actor: h.Actor, By: h.By,
			OccurredAt: h.OccurredAt, ReceivedAt: h.ReceivedAt, Reason: h.Reason,
		})
		if err != nil || o != h.Outcome {
			t.Fatalf("replaying %+v: outcome %q err %v", h, o, err)
		}
	}
	if replay.Status != got.Status || !reflect.DeepEqual(replay.History, got.History) {
		t.Errorf("replay status %s, stored %s\nreplay %+v\nstored %+v", replay.Status, got.Status, replay.History, got.History)
	}
}

func TestListFiltersAndCursor(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	st := domain.DefaultTenantSettings()
	e2e := domain.ModelEndToEnd

	var all []*domain.Delivery
	for i := range 25 {
		in := fullInput(fmt.Sprintf("ord-%02d", i))
		if i%5 == 0 {
			in.Model = &e2e
		}
		d := create(t, e, scopeA, in, st, t0.Add(time.Duration(i)*time.Minute))
		if i%3 == 0 {
			d = mutate(t, e, scopeA, d.ID, func(d *domain.Delivery) ([]domain.Event, error) { return d.Cancel("", t0) })
		}
		all = append(all, d)
	}
	create(t, e, scopeBTest, fullInput("other"), st, t0) // never listed
	slices.Reverse(all)                                  // newest first

	ids := func(ds []*domain.Delivery) []string {
		out := make([]string, len(ds))
		for i, d := range ds {
			out[i] = d.ID
		}
		return out
	}
	list := func(f app.ListFilter) ([]*domain.Delivery, bool) {
		t.Helper()
		ds, more, err := e.repo.List(ctx, scopeA, f)
		if err != nil {
			t.Fatal(err)
		}
		return ds, more
	}

	var paged []*domain.Delivery
	after := ""
	for _, want := range []struct {
		n    int
		more bool
	}{{10, true}, {10, true}, {5, false}} {
		ds, more := list(app.ListFilter{Limit: 10, StartingAfter: after})
		if len(ds) != want.n || more != want.more {
			t.Fatalf("page after %q: %d rows more=%v, want %d more=%v", after, len(ds), more, want.n, want.more)
		}
		paged = append(paged, ds...)
		after = ds[len(ds)-1].ID
	}
	if !slices.Equal(ids(paged), ids(all)) {
		t.Errorf("paged ids = %v\nwant %v", ids(paged), ids(all))
	}
	if len(paged[0].History) == 0 {
		t.Error("listed deliveries have no history")
	}
	if *paged[0].CustomerFirstName != "Alice" {
		t.Error("listed deliveries are not unsealed")
	}

	filter := func(keep func(*domain.Delivery) bool) []string {
		var out []*domain.Delivery
		for _, d := range all {
			if keep(d) {
				out = append(out, d)
			}
		}
		return ids(out)
	}
	cancelled, model := domain.StatusCancelled, domain.ModelEndToEnd
	gte, lt := t0.Add(5*time.Minute), t0.Add(12*time.Minute)
	ext := "ord-07"
	for name, tc := range map[string]struct {
		f    app.ListFilter
		want []string
	}{
		"status": {app.ListFilter{Status: &cancelled}, filter(func(d *domain.Delivery) bool { return d.Status == cancelled })},
		"model":  {app.ListFilter{Model: &model}, filter(func(d *domain.Delivery) bool { return d.Model == model })},
		"created": {app.ListFilter{CreatedGTE: &gte, CreatedLT: &lt}, filter(func(d *domain.Delivery) bool {
			return !d.CreatedAt.Before(gte) && d.CreatedAt.Before(lt)
		})},
		"external_id": {app.ListFilter{ExternalID: &ext}, filter(func(d *domain.Delivery) bool { return d.ExternalID == ext })},
		"combined": {app.ListFilter{Status: &cancelled, Model: &model}, filter(func(d *domain.Delivery) bool {
			return d.Status == cancelled && d.Model == model
		})},
	} {
		tc.f.Limit = 100
		ds, more := list(tc.f)
		if more || !slices.Equal(ids(ds), tc.want) || len(tc.want) == 0 {
			t.Errorf("%s: got %v more=%v, want %v", name, ids(ds), more, tc.want)
		}
	}
}

func TestPickupWatchLifecycle(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	cas := domain.DefaultTenantSettings()
	cas.PickupPolicy = domain.PolicyCourierAndStore

	watchCount := func(id string) int {
		var n int
		if err := e.admin.QueryRow(ctx, `SELECT count(*) FROM deliveries.pickup_watch WHERE delivery_id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	pendingRows := func() []outboxRow {
		var out []outboxRow
		for _, r := range outboxRows(t, e) {
			if r.env.GetType() == domain.EventPickupConfirmationPending {
				out = append(out, r)
			}
		}
		return out
	}

	d := create(t, e, scopeA, fullInput("ord-1"), cas, t0)
	if watchCount(d.ID) != 0 {
		t.Fatal("a new delivery is watched")
	}
	since := t0.Add(time.Minute)
	mutate(t, e, scopeA, d.ID, report(domain.Report{
		EventID: "store-1", Type: domain.ReportPickupConfirmed, Actor: domain.ActorPartner, By: domain.ByStore,
		OccurredAt: since, ReceivedAt: since,
	}))
	// A second confirmation from the same side keeps the first "since".
	mutate(t, e, scopeA, d.ID, report(domain.Report{
		EventID: "store-2", Type: domain.ReportPickupConfirmed, Actor: domain.ActorPartner, By: domain.ByStore,
		OccurredAt: since.Add(time.Minute), ReceivedAt: since.Add(time.Minute),
	}))
	var gotSince, due time.Time
	if err := e.admin.QueryRow(ctx, `SELECT since, due_at FROM deliveries.pickup_watch WHERE delivery_id = $1`, d.ID).
		Scan(&gotSince, &due); err != nil {
		t.Fatal(err)
	}
	if !gotSince.Equal(since) || !due.Equal(since.Add(domain.PickupPendingAfter)) {
		t.Errorf("watch since %v due %v, want %v and +10m", gotSince, due, since)
	}

	// Not due yet.
	ws, err := e.repo.DuePickupWatches(ctx, due.Add(-time.Second), 10)
	if err != nil || len(ws) != 0 {
		t.Fatalf("early DuePickupWatches = %v, %v", ws, err)
	}
	ws, err = e.repo.DuePickupWatches(ctx, due, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []app.PickupWatch{{DeliveryID: d.ID, TenantID: "ten_a", Env: "test", Since: since}}
	if !reflect.DeepEqual(ws, want) {
		t.Fatalf("DuePickupWatches = %+v, want %+v", ws, want)
	}
	if err := e.repo.WarnPickupPending(ctx, ws[0], due); err != nil {
		t.Fatal(err)
	}
	// Warned once only.
	if err := e.repo.WarnPickupPending(ctx, ws[0], due); err != nil {
		t.Fatal(err)
	}
	if ws, _ := e.repo.DuePickupWatches(ctx, due.Add(time.Hour), 10); len(ws) != 0 {
		t.Errorf("a warned watch is due again: %v", ws)
	}
	var warnedAt *time.Time
	if err := e.admin.QueryRow(ctx, `SELECT warned_at FROM deliveries.pickup_watch WHERE delivery_id = $1`, d.ID).
		Scan(&warnedAt); err != nil {
		t.Fatal(err)
	}
	if warnedAt == nil || !warnedAt.Equal(due) {
		t.Errorf("warned_at = %v, want %v", warnedAt, due)
	}
	rows := pendingRows()
	if len(rows) != 1 {
		t.Fatalf("%d pickup_confirmation_pending rows, want 1", len(rows))
	}
	pending := &eventsv1.DeliveryPickupConfirmationPending{}
	if err := rows[0].env.GetPayload().UnmarshalTo(pending); err != nil {
		t.Fatal(err)
	}
	if pending.GetDeliveryId() != d.ID || !slices.Equal(pending.GetConfirmedBy(), []string{"store"}) ||
		!pending.GetSince().AsTime().Equal(since) || rows[0].env.GetSubject().GetDeliveryId() != d.ID ||
		rows[0].env.GetTenantId() != "ten_a" {
		t.Errorf("pending event = %v / %v", pending, rows[0].env)
	}

	// The courier's confirmation completes the pickup and ends the watch.
	got := mutate(t, e, scopeA, d.ID, report(domain.Report{
		EventID: "cour-1", Type: domain.ReportPickupConfirmed, Actor: domain.ActorCourier,
		OccurredAt: due, ReceivedAt: due,
	}))
	if got.Status != domain.StatusPickedUp || watchCount(d.ID) != 0 {
		t.Errorf("status %s, watches %d; want picked_up and none", got.Status, watchCount(d.ID))
	}

	// Cancelling a watched delivery ends the watch too.
	d2 := create(t, e, scopeA, fullInput("ord-2"), cas, t0)
	mutate(t, e, scopeA, d2.ID, report(domain.Report{
		EventID: "cour-1", Type: domain.ReportPickupConfirmed, Actor: domain.ActorCourier, OccurredAt: t0, ReceivedAt: t0,
	}))
	if watchCount(d2.ID) != 1 {
		t.Fatal("a courier confirmation did not start a watch")
	}
	mutate(t, e, scopeA, d2.ID, func(d *domain.Delivery) ([]domain.Event, error) { return d.Cancel("", t0) })
	if watchCount(d2.ID) != 0 {
		t.Error("cancelling did not end the watch")
	}

	// A watch whose delivery no longer waits is dropped without a warning.
	d3 := create(t, e, scopeA, fullInput("ord-3"), cas, t0)
	if _, err := e.admin.Exec(ctx, `INSERT INTO deliveries.pickup_watch (delivery_id, tenant_id, environment, since, due_at)
		VALUES ($1, 'ten_a', 'test', $2, $2)`, d3.ID, t0); err != nil {
		t.Fatal(err)
	}
	if err := e.repo.WarnPickupPending(ctx, app.PickupWatch{DeliveryID: d3.ID, TenantID: "ten_a", Env: "test", Since: t0}, due); err != nil {
		t.Fatal(err)
	}
	if watchCount(d3.ID) != 0 || len(pendingRows()) != 1 {
		t.Errorf("stale watch: %d watches, %d pending events", watchCount(d3.ID), len(pendingRows()))
	}

	// Policies other than courier_and_store are never watched.
	d4 := create(t, e, scopeA, fullInput("ord-4"), domain.DefaultTenantSettings(), t0)
	mutate(t, e, scopeA, d4.ID, report(domain.Report{
		EventID: "x", Type: domain.ReportAtPickup, Actor: domain.ActorCourier, OccurredAt: t0, ReceivedAt: t0,
	}))
	if watchCount(d4.ID) != 0 {
		t.Error("an any-policy delivery is watched")
	}
}

func TestApplySettingsIgnoresOlder(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()

	got, err := e.repo.Settings(ctx, "ten_new")
	if err != nil || !reflect.DeepEqual(got, domain.DefaultTenantSettings()) {
		t.Fatalf("unknown tenant settings = %+v, %v; want defaults", got, err)
	}

	apply := func(s domain.TenantSettings, at time.Time) {
		t.Helper()
		tx, err := e.svc.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := e.repo.ApplySettings(ctx, tx, "ten_a", s, at); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	newer := domain.TenantSettings{
		AllowedModels: []domain.Model{domain.ModelEndToEnd}, DefaultModel: domain.ModelEndToEnd,
		PickupPolicy: domain.PolicyCourierAndStore,
	}
	apply(newer, t0.Add(time.Minute))
	apply(domain.DefaultTenantSettings(), t0) // older: ignored
	if got, _ := e.repo.Settings(ctx, "ten_a"); !reflect.DeepEqual(got, newer) {
		t.Errorf("after an older update settings = %+v, want %+v", got, newer)
	}
	apply(domain.DefaultTenantSettings(), t0.Add(2*time.Minute))
	if got, _ := e.repo.Settings(ctx, "ten_a"); !reflect.DeepEqual(got, domain.DefaultTenantSettings()) {
		t.Errorf("after a newer update settings = %+v, want defaults", got)
	}
}

func TestDroppedReportsPersist(t *testing.T) {
	t.Parallel()
	e := setup(t)
	d := create(t, e, scopeA, fullInput("ord-cap"), domain.DefaultTenantSettings(), t0)
	mutate(t, e, scopeA, d.ID, report(domain.Report{
		EventID: "p1", Type: domain.ReportPickupConfirmed, Actor: domain.ActorCourier,
		OccurredAt: t0.Add(time.Minute), ReceivedAt: t0.Add(time.Minute),
	}))
	for i := 0; i < domain.MaxNoiseEntries+3; i++ {
		at := t0.Add(2 * time.Minute)
		mutate(t, e, scopeA, d.ID, report(domain.Report{
			EventID: fmt.Sprintf("a%d", i), Type: domain.ReportArriving, Actor: domain.ActorCourier,
			OccurredAt: at, ReceivedAt: at,
		}))
	}
	got, err := e.repo.Get(context.Background(), scopeA, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	// One arriving applied, the rest stale: the first 200 stored, 2 dropped.
	if got.DroppedReports != 2 {
		t.Fatalf("DroppedReports = %d, want 2", got.DroppedReports)
	}
	if len(got.History) != 1+1+1+domain.MaxNoiseEntries {
		t.Fatalf("history = %d", len(got.History))
	}
}

func TestReasonSealedAtRest(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	const reason = "customer Ana moved"
	d := create(t, e, scopeA, fullInput("ord-reason"), domain.DefaultTenantSettings(), t0)
	mutate(t, e, scopeA, d.ID, func(d *domain.Delivery) ([]domain.Event, error) {
		return d.Cancel(reason, t0.Add(time.Minute))
	})

	var pos, sealed int
	if err := e.admin.QueryRow(ctx,
		`SELECT coalesce(max(position(convert_to($1, 'UTF8') in reason_sealed)), 0), count(reason_sealed)
		 FROM deliveries.delivery_history WHERE delivery_id = $2`, reason, d.ID).Scan(&pos, &sealed); err != nil {
		t.Fatal(err)
	}
	if pos != 0 || sealed != 1 {
		t.Fatalf("plaintext position %d, sealed rows %d; want 0 and 1", pos, sealed)
	}
	got, err := e.repo.Get(ctx, scopeA, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if h := got.History[len(got.History)-1]; h.Reason != reason {
		t.Fatalf("reason = %q", h.Reason)
	}

	mutate(t, e, scopeA, d.ID, func(d *domain.Delivery) ([]domain.Event, error) {
		return d.ErasePersonalData(t0.Add(time.Hour))
	})
	var left int
	if err := e.admin.QueryRow(ctx,
		`SELECT count(*) FROM deliveries.delivery_history WHERE delivery_id = $1 AND reason_sealed IS NOT NULL`,
		d.ID).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("%d sealed reasons left after erasure", left)
	}
	got, err = e.repo.Get(ctx, scopeA, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range got.History {
		if h.Reason != "" {
			t.Fatalf("history keeps reason %q after erasure", h.Reason)
		}
	}
}
