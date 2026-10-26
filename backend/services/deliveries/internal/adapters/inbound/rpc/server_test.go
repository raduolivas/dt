package rpc_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/connect/v2/connectproto"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	deliveriesv1 "github.com/raduolivas/dt/backend/gen/dt/deliveries/v1"
	"github.com/raduolivas/dt/backend/gen/dt/deliveries/v1/deliveriesv1connect"
	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/ids"
	"github.com/raduolivas/dt/backend/pkg/migratex"
	"github.com/raduolivas/dt/backend/pkg/rpcauth"
	"github.com/raduolivas/dt/backend/pkg/sealbox"
	"github.com/raduolivas/dt/backend/pkg/testkit"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/adapters/inbound/rpc"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/adapters/outbound/postgres"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/app"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/domain"
	"github.com/raduolivas/dt/backend/services/deliveries/migrations"
)

const (
	testToken = "test-only-internal-rpc-token-0123456789"
	testKeys  = "k1:AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
)

var testScope = &deliveriesv1.Scope{
	TenantId: "ten_rpc", Environment: eventsv1.Environment_ENVIRONMENT_TEST, ActorId: "ak_rpc",
}

type clock struct{}

func (clock) Now() time.Time { return time.Now() }

type idGen struct{}

func (idGen) New(prefix string) string { return ids.New(prefix) }

// starting limits concurrent container starts: rootless Docker's port
// forwarder fails with "address already in use" when many start at once.
var starting = make(chan struct{}, 2)

func serve(t *testing.T, repo app.Repo) *httptest.Server {
	t.Helper()
	svc := &app.Service{Repo: repo, Clock: clock{}, IDs: idGen{}}
	mux := http.NewServeMux()
	if err := rpc.Mount(mux, svc, testToken, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func realServer(t *testing.T) (*httptest.Server, *bytes.Buffer) {
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
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	box, err := sealbox.ParseKeys(testKeys, "k1")
	if err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	svc := &app.Service{Repo: postgres.New(pool, box), Clock: clock{}, IDs: idGen{}}
	mux := http.NewServeMux()
	if err := rpc.Mount(mux, svc, testToken, slog.New(slog.NewTextHandler(logs, nil))); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, logs
}

type bearer struct {
	c     *http.Client
	value string
}

func (b bearer) Do(r *http.Request) (*http.Response, error) {
	if b.value != "" {
		r.Header.Set("Authorization", b.value)
	}
	return b.c.Do(r)
}

func client(srv *httptest.Server, authorization string) deliveriesv1connect.DeliveriesServiceClient {
	return deliveriesv1connect.NewDeliveriesServiceClient(connect.NewClient(
		connecthttp.NewTransport(bearer{srv.Client(), authorization}, srv.URL)))
}

func authed(srv *httptest.Server) deliveriesv1connect.DeliveriesServiceClient {
	return client(srv, "Bearer "+testToken)
}

func dtCode(info *connect.CallInfo) string { return info.ResponseTrailer().Get(rpcauth.CodeKey) }

func createReq(ext string) *deliveriesv1.CreateDeliveryRequest {
	return &deliveriesv1.CreateDeliveryRequest{
		Scope: testScope, ExternalId: ext,
		Pickup:   &deliveriesv1.Pickup{Location: &eventsv1.LatLng{Lat: -23.55, Lng: -46.63}, Name: "Pizzeria Uno"},
		Dropoff:  &deliveriesv1.Dropoff{Location: &eventsv1.LatLng{Lat: -23.56, Lng: -46.65}, Label: ptr("Apt 12 SECRET-LABEL")},
		Customer: &deliveriesv1.Customer{FirstName: ptr("Ana")},
		Notes:    ptr("ring twice"), Metadata: map[string]string{"k": "v"},
	}
}

func ptr[T any](v T) *T { return &v }

func TestRPCRequiresToken(t *testing.T) {
	t.Parallel()
	srv := serve(t, &stubRepo{})
	for _, auth := range []string{"", "Bearer " + strings.Repeat("x", len(testToken)), "Basic " + testToken} {
		_, err := client(srv, auth).GetDelivery(context.Background(),
			&deliveriesv1.GetDeliveryRequest{Scope: testScope, Id: "del_x"})
		if got := connect.CodeOf(err); got != connect.CodeUnauthenticated {
			t.Errorf("auth %q: code = %v, want Unauthenticated", auth, got)
		}
	}
	_, err := authed(srv).GetDelivery(context.Background(), &deliveriesv1.GetDeliveryRequest{Scope: testScope, Id: "del_x"})
	if got := connect.CodeOf(err); got == connect.CodeUnauthenticated {
		t.Fatalf("right token rejected: %v", err)
	}
}

// stubRepo returns err from every call that reaches it.
type stubRepo struct{ err error }

func (r *stubRepo) Settings(context.Context, string) (domain.TenantSettings, error) {
	return domain.DefaultTenantSettings(), r.err
}
func (r *stubRepo) Create(context.Context, app.Scope, *domain.Delivery, []domain.Event) error {
	return r.err
}
func (r *stubRepo) Get(context.Context, app.Scope, string) (*domain.Delivery, error) {
	return nil, r.err
}
func (r *stubRepo) List(context.Context, app.Scope, app.ListFilter) ([]*domain.Delivery, bool, error) {
	return nil, false, r.err
}
func (r *stubRepo) Mutate(context.Context, app.Scope, string, func(*domain.Delivery) ([]domain.Event, error)) (*domain.Delivery, error) {
	return nil, r.err
}
func (r *stubRepo) Courier(context.Context, app.Scope, string) (domain.Courier, error) {
	return domain.Courier{}, r.err
}
func (r *stubRepo) UpsertCourier(context.Context, app.Scope, domain.Courier, []domain.Event) (domain.Courier, bool, error) {
	return domain.Courier{}, false, r.err
}
func (r *stubRepo) DuePickupWatches(context.Context, time.Time, int) ([]app.PickupWatch, error) {
	return nil, r.err
}
func (r *stubRepo) WarnPickupPending(context.Context, app.PickupWatch, time.Time) error { return r.err }

func TestRPCErrorMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		err    error
		code   connect.Code
		dtCode string
	}{
		{"not found", app.ErrNotFound, connect.CodeNotFound, "not_found"},
		{"invalid transition", domain.ErrInvalidTransition, connect.CodeFailedPrecondition, "invalid_transition"},
		{"external id", app.ErrExternalIDTaken, connect.CodeAlreadyExists, "external_id_conflict"},
		{"dropoff locked", domain.ErrDropoffLocked, connect.CodeFailedPrecondition, "dropoff_locked"},
		{"delivery active", domain.ErrDeliveryActive, connect.CodeFailedPrecondition, "delivery_active"},
		{"model not allowed", domain.ErrModelNotAllowed, connect.CodeInvalidArgument, "model_not_allowed"},
		{"courier not found", app.ErrCourierNotFound, connect.CodeInvalidArgument, "courier_not_found"},
		{"wrapped not found", errors.Join(errors.New("ctx"), app.ErrNotFound), connect.CodeNotFound, "not_found"},
		{"validation", &domain.ValidationError{Field: "notes", Code: "too_long", Message: "too long"}, connect.CodeInvalidArgument, "validation_failed"},
		{"other", errors.New("pq: secret detail"), connect.CodeInternal, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := serve(t, &stubRepo{err: tc.err})
			ctx, info := connect.NewClientContext(context.Background())
			_, err := authed(srv).GetDelivery(ctx, &deliveriesv1.GetDeliveryRequest{Scope: testScope, Id: "del_x"})
			if got := connect.CodeOf(err); got != tc.code {
				t.Fatalf("code = %v (%v), want %v", got, err, tc.code)
			}
			if got := dtCode(info); got != tc.dtCode {
				t.Fatalf("dt-code = %q, want %q", got, tc.dtCode)
			}
			if tc.code == connect.CodeInternal && strings.Contains(err.Error(), "secret") {
				t.Fatalf("internal error leaks detail: %v", err)
			}
			if tc.dtCode == "validation_failed" {
				var verr *connect.Error
				if !errors.As(err, &verr) || len(verr.Details()) != 1 {
					t.Fatalf("want one detail, got %v", err)
				}
				m, derr := connectproto.UnmarshalErrorDetail(verr.Details()[0])
				if derr != nil {
					t.Fatal(derr)
				}
				fv, ok := m.(*deliveriesv1.FieldViolation)
				if !ok || fv.GetField() != "notes" || fv.GetCode() != "too_long" {
					t.Fatalf("detail = %v", m)
				}
			}
		})
	}
}

func TestRPCScopeRequired(t *testing.T) {
	t.Parallel()
	srv := serve(t, &stubRepo{})
	for name, sc := range map[string]*deliveriesv1.Scope{
		"nil":        nil,
		"no tenant":  {Environment: eventsv1.Environment_ENVIRONMENT_TEST},
		"no env":     {TenantId: "ten_x"},
		"unspecfied": {TenantId: "ten_x", Environment: eventsv1.Environment_ENVIRONMENT_UNSPECIFIED},
	} {
		ctx, info := connect.NewClientContext(context.Background())
		_, err := authed(srv).GetDelivery(ctx, &deliveriesv1.GetDeliveryRequest{Scope: sc, Id: "del_x"})
		if connect.CodeOf(err) != connect.CodeInvalidArgument || dtCode(info) != "validation_failed" {
			t.Errorf("%s: %v, dt-code %q", name, err, dtCode(info))
		}
	}
}

func TestReportEventSameEventIDTwice(t *testing.T) {
	t.Parallel()
	srv, _ := realServer(t)
	c := authed(srv)
	ctx := context.Background()
	cr, err := c.CreateDelivery(ctx, createReq("ext-report"))
	if err != nil {
		t.Fatal(err)
	}
	id := cr.GetDelivery().GetId()
	var outcomes []string
	for range 2 {
		res, err := c.ReportEvent(ctx, &deliveriesv1.ReportEventRequest{Scope: testScope, Id: id, EventId: "evt-1", Type: "at_pickup"})
		if err != nil {
			t.Fatal(err)
		}
		outcomes = append(outcomes, res.GetOutcome())
		if res.GetDelivery().GetStatus() != "at_pickup" {
			t.Fatalf("status = %q", res.GetDelivery().GetStatus())
		}
	}
	if outcomes[0] != "applied" || outcomes[1] != "duplicate" {
		t.Fatalf("outcomes = %v", outcomes)
	}
}

func TestRPCRoundTripAllMethods(t *testing.T) {
	t.Parallel()
	srv, logs := realServer(t)
	c := authed(srv)
	ctx := context.Background()

	cr, err := c.CreateDelivery(ctx, createReq("ext-1"))
	if err != nil {
		t.Fatal(err)
	}
	d := cr.GetDelivery()
	if !strings.HasPrefix(d.GetId(), "del_") || d.GetStatus() != "confirmed" || d.GetDeliveryModel() != "direct" ||
		d.GetCustomer().GetFirstName() != "Ana" || d.GetDropoff().GetLabel() != "Apt 12 SECRET-LABEL" ||
		d.GetNotes() != "ring twice" || d.GetMetadata()["k"] != "v" || d.GetPickupPolicy() != "any" ||
		d.GetEnvironment() != eventsv1.Environment_ENVIRONMENT_TEST {
		t.Fatalf("created = %v", d)
	}

	ctx2, info := connect.NewClientContext(ctx)
	_, err = c.CreateDelivery(ctx2, createReq("ext-1"))
	if connect.CodeOf(err) != connect.CodeAlreadyExists || dtCode(info) != "external_id_conflict" {
		t.Fatalf("duplicate external id: %v / %q", err, dtCode(info))
	}

	g, err := c.GetDelivery(ctx, &deliveriesv1.GetDeliveryRequest{Scope: testScope, Id: d.GetId()})
	if err != nil || g.GetDelivery().GetExternalId() != "ext-1" {
		t.Fatalf("get: %v %v", g, err)
	}
	other := &deliveriesv1.Scope{TenantId: "ten_other", Environment: eventsv1.Environment_ENVIRONMENT_TEST}
	if _, err := c.GetDelivery(ctx, &deliveriesv1.GetDeliveryRequest{Scope: other, Id: d.GetId()}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("other tenant get: %v", err)
	}

	if _, err := c.CreateDelivery(ctx, createReq("ext-2")); err != nil {
		t.Fatal(err)
	}
	l, err := c.ListDeliveries(ctx, &deliveriesv1.ListDeliveriesRequest{Scope: testScope, Limit: 1})
	if err != nil || len(l.GetData()) != 1 || !l.GetHasMore() {
		t.Fatalf("list: %v %v", l, err)
	}
	l, err = c.ListDeliveries(ctx, &deliveriesv1.ListDeliveriesRequest{Scope: testScope, ExternalId: "ext-1"})
	if err != nil || len(l.GetData()) != 1 || l.GetHasMore() {
		t.Fatalf("list filter: %v %v", l, err)
	}

	u, err := c.UpdateDelivery(ctx, &deliveriesv1.UpdateDeliveryRequest{
		Scope: testScope, Id: d.GetId(),
		UpdateMask:   &fieldmaskpb.FieldMask{Paths: []string{"notes", "dropoff_label"}},
		DropoffLabel: ptr("Door 3"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if u.GetDelivery().Notes != nil || u.GetDelivery().GetDropoff().GetLabel() != "Door 3" || u.GetDelivery().GetMetadata()["k"] != "v" {
		t.Fatalf("update: %v", u.GetDelivery())
	}
	ctx3, info := connect.NewClientContext(ctx)
	_, err = c.UpdateDelivery(ctx3, &deliveriesv1.UpdateDeliveryRequest{
		Scope: testScope, Id: d.GetId(), UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"nope"}}})
	if connect.CodeOf(err) != connect.CodeInvalidArgument || dtCode(info) != "validation_failed" {
		t.Fatalf("bad mask: %v / %q", err, dtCode(info))
	}

	ctx4, info := connect.NewClientContext(ctx)
	_, err = c.AssignCourier(ctx4, &deliveriesv1.AssignCourierRequest{Scope: testScope, Id: d.GetId(), CourierId: "cou_missing"})
	if connect.CodeOf(err) != connect.CodeInvalidArgument || dtCode(info) != "courier_not_found" {
		t.Fatalf("assign missing: %v / %q", err, dtCode(info))
	}
	uc, err := c.UpsertCourier(ctx, &deliveriesv1.UpsertCourierRequest{
		Scope: testScope, ExternalId: "c-1", DisplayName: "Bia", Vehicle: "e_bike", PhotoUrl: ptr("https://example.com/b.png")})
	if err != nil || !uc.GetCreated() || !strings.HasPrefix(uc.GetCourier().GetId(), "cou_") {
		t.Fatalf("upsert: %v %v", uc, err)
	}
	uc2, err := c.UpsertCourier(ctx, &deliveriesv1.UpsertCourierRequest{
		Scope: testScope, ExternalId: "c-1", DisplayName: "Bia B", Vehicle: "car"})
	if err != nil || uc2.GetCreated() || uc2.GetCourier().GetId() != uc.GetCourier().GetId() {
		t.Fatalf("upsert again: %v %v", uc2, err)
	}
	a, err := c.AssignCourier(ctx, &deliveriesv1.AssignCourierRequest{Scope: testScope, Id: d.GetId(), CourierId: uc.GetCourier().GetId()})
	if err != nil || a.GetDelivery().GetCourier().GetExternalId() != "c-1" {
		t.Fatalf("assign: %v %v", a, err)
	}

	r, err := c.ReportEvent(ctx, &deliveriesv1.ReportEventRequest{
		Scope: testScope, Id: d.GetId(), EventId: "e-pick", Type: "pickup_confirmed",
		OccurredAt: timestamppb.Now(), By: "store"})
	if err != nil || r.GetOutcome() != "applied" || len(r.GetDelivery().GetPickupConfirmations()) != 1 {
		t.Fatalf("report: %v %v", r, err)
	}

	ctx5, info := connect.NewClientContext(ctx)
	_, err = c.ErasePersonalData(ctx5, &deliveriesv1.ErasePersonalDataRequest{Scope: testScope, Id: d.GetId()})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || dtCode(info) != "delivery_active" {
		t.Fatalf("erase active: %v / %q", err, dtCode(info))
	}
	x, err := c.CancelDelivery(ctx, &deliveriesv1.CancelDeliveryRequest{Scope: testScope, Id: d.GetId(), Reason: "no"})
	if err == nil {
		t.Fatalf("cancel after pickup should fail, got %v", x)
	}
	// ext-2 is still confirmed: cancel then erase.
	l, _ = c.ListDeliveries(ctx, &deliveriesv1.ListDeliveriesRequest{Scope: testScope, ExternalId: "ext-2"})
	id2 := l.GetData()[0].GetId()
	x, err = c.CancelDelivery(ctx, &deliveriesv1.CancelDeliveryRequest{Scope: testScope, Id: id2, Reason: "changed mind"})
	if err != nil || x.GetDelivery().GetStatus() != "cancelled" {
		t.Fatalf("cancel: %v %v", x, err)
	}
	e, err := c.ErasePersonalData(ctx, &deliveriesv1.ErasePersonalDataRequest{Scope: testScope, Id: id2})
	if err != nil || !e.GetDelivery().GetPersonalDataErased() || e.GetDelivery().GetCustomer().FirstName != nil {
		t.Fatalf("erase: %v %v", e, err)
	}

	for _, secret := range []string{"Ana", "SECRET-LABEL", "ring twice", "Door 3"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("logs contain personal data %q:\n%s", secret, logs)
		}
	}
}

func TestReportEventCourierActor(t *testing.T) {
	t.Parallel()
	srv, _ := realServer(t)
	c := authed(srv)
	ctx := context.Background()
	cr, err := c.CreateDelivery(ctx, createReq("ext-courier"))
	if err != nil {
		t.Fatal(err)
	}
	id := cr.GetDelivery().GetId()

	// A courier reports at_pickup: allowed by the courier column in direct.
	res, err := c.ReportEvent(ctx, &deliveriesv1.ReportEventRequest{
		Scope: testScope, Id: id, EventId: "c-1", Type: "at_pickup", Actor: "courier"})
	if err != nil || res.GetOutcome() != "applied" || res.GetDelivery().GetStatus() != "at_pickup" {
		t.Fatalf("courier at_pickup: %v %v", res, err)
	}
	// A courier cannot confirm as the store: by is forced to courier.
	res, err = c.ReportEvent(ctx, &deliveriesv1.ReportEventRequest{
		Scope: testScope, Id: id, EventId: "c-2", Type: "pickup_confirmed", By: "store", Actor: "courier"})
	if err != nil || res.GetDelivery().GetStatus() != "picked_up" {
		t.Fatalf("courier pickup_confirmed: %v %v", res, err)
	}
	pcs := res.GetDelivery().GetPickupConfirmations()
	if len(pcs) != 1 || pcs[0].GetBy() != "courier" {
		t.Fatalf("pickup confirmations = %v", pcs)
	}

	// Without an actor the report is the partner's, so by "store" is kept.
	cr, err = c.CreateDelivery(ctx, createReq("ext-partner"))
	if err != nil {
		t.Fatal(err)
	}
	res, err = c.ReportEvent(ctx, &deliveriesv1.ReportEventRequest{
		Scope: testScope, Id: cr.GetDelivery().GetId(), EventId: "p-1", Type: "pickup_confirmed", By: "store"})
	if err != nil || res.GetDelivery().GetPickupConfirmations()[0].GetBy() != "store" {
		t.Fatalf("partner pickup_confirmed: %v %v", res, err)
	}

	// Unknown actors are refused.
	ctx2, info := connect.NewClientContext(ctx)
	_, err = c.ReportEvent(ctx2, &deliveriesv1.ReportEventRequest{
		Scope: testScope, Id: id, EventId: "c-3", Type: "delivered", Actor: "robot"})
	if connect.CodeOf(err) != connect.CodeInvalidArgument || dtCode(info) != "validation_failed" {
		t.Fatalf("robot actor: %v / %q", err, dtCode(info))
	}
}
