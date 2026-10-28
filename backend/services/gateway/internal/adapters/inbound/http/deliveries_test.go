package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/connect/v2/connectproto"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/gorillamux"
	"google.golang.org/protobuf/types/known/timestamppb"

	deliveriesv1 "github.com/raduolivas/dt/backend/gen/dt/deliveries/v1"
	"github.com/raduolivas/dt/backend/gen/dt/deliveries/v1/deliveriesv1connect"
	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/apikey"
	"github.com/raduolivas/dt/backend/pkg/logx"
	"github.com/raduolivas/dt/backend/pkg/rpcauth"
	"github.com/raduolivas/dt/backend/services/gateway/api"
	httpapi "github.com/raduolivas/dt/backend/services/gateway/internal/adapters/inbound/http"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

const (
	delID  = "del_01j9z3k8m2q4r6s8t0v2w4x6y8"
	couID  = "cou_01j9z3k8m2q4r6s8t0v2w4x6y8"
	actor  = "ak_01j9z3k8m2q4r6s8t0v2w4x6y8"
	stamp  = "2026-10-22T14:00:00Z"
	create = `{"external_id":"order-1042","pickup":{"location":{"lat":-23.5614,"lng":-46.6559},"name":"Pizzaria Central"},` +
		`"dropoff":{"location":{"lat":-23.5505,"lng":-46.6333},"label":"Apt 4"},"customer":{"first_name":"Ana"},` +
		`"items_summary":"2 pizzas","item_count":2,"notes":"ring twice","metadata":{"table":"12"}}`
)

// fakeDeliveries is a DeliveriesServiceClient that records the last request
// of each call and answers from the configured fields.
type fakeDeliveries struct {
	mu    sync.Mutex
	calls []string
	req   any

	del     *deliveriesv1.Delivery
	list    *deliveriesv1.ListDeliveriesResponse
	outcome string
	courier *deliveriesv1.Courier
	created bool
	// fail, when set, makes every call fail like the service would.
	fail func(ctx context.Context) error
}

func newFake() *fakeDeliveries {
	ts := timestamppb.New(time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC))
	return &fakeDeliveries{
		outcome: "applied",
		del: &deliveriesv1.Delivery{
			Id: delID, ExternalId: "order-1042", Environment: eventsv1.Environment_ENVIRONMENT_TEST,
			DeliveryModel: "direct", Status: "confirmed",
			Pickup:   &deliveriesv1.Pickup{Location: &eventsv1.LatLng{Lat: -23.5614, Lng: -46.6559}, Name: "Pizzaria Central"},
			Dropoff:  &deliveriesv1.Dropoff{Location: &eventsv1.LatLng{Lat: -23.5505, Lng: -46.6333}},
			Customer: &deliveriesv1.Customer{}, Metadata: map[string]string{},
			PickupPolicy: "any", CreatedAt: ts, UpdatedAt: ts,
		},
		courier: &deliveriesv1.Courier{
			Id: couID, ExternalId: "driver-77", DisplayName: "Carlos", Vehicle: "e_bike", CreatedAt: ts, UpdatedAt: ts,
		},
	}
}

func (f *fakeDeliveries) rec(ctx context.Context, name string, req any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
	f.req = req
	if f.fail != nil {
		return f.fail(ctx)
	}
	return nil
}

func (f *fakeDeliveries) last() any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.req
}

func (f *fakeDeliveries) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeDeliveries) CreateDelivery(ctx context.Context, r *deliveriesv1.CreateDeliveryRequest) (*deliveriesv1.CreateDeliveryResponse, error) {
	if err := f.rec(ctx, "create", r); err != nil {
		return nil, err
	}
	return &deliveriesv1.CreateDeliveryResponse{Delivery: f.del}, nil
}

func (f *fakeDeliveries) GetDelivery(ctx context.Context, r *deliveriesv1.GetDeliveryRequest) (*deliveriesv1.GetDeliveryResponse, error) {
	if err := f.rec(ctx, "get", r); err != nil {
		return nil, err
	}
	return &deliveriesv1.GetDeliveryResponse{Delivery: f.del}, nil
}

func (f *fakeDeliveries) ListDeliveries(ctx context.Context, r *deliveriesv1.ListDeliveriesRequest) (*deliveriesv1.ListDeliveriesResponse, error) {
	if err := f.rec(ctx, "list", r); err != nil {
		return nil, err
	}
	if f.list != nil {
		return f.list, nil
	}
	return &deliveriesv1.ListDeliveriesResponse{Data: []*deliveriesv1.Delivery{f.del}}, nil
}

func (f *fakeDeliveries) UpdateDelivery(ctx context.Context, r *deliveriesv1.UpdateDeliveryRequest) (*deliveriesv1.UpdateDeliveryResponse, error) {
	if err := f.rec(ctx, "update", r); err != nil {
		return nil, err
	}
	return &deliveriesv1.UpdateDeliveryResponse{Delivery: f.del}, nil
}

func (f *fakeDeliveries) AssignCourier(ctx context.Context, r *deliveriesv1.AssignCourierRequest) (*deliveriesv1.AssignCourierResponse, error) {
	if err := f.rec(ctx, "assign", r); err != nil {
		return nil, err
	}
	return &deliveriesv1.AssignCourierResponse{Delivery: f.del}, nil
}

func (f *fakeDeliveries) ReportEvent(ctx context.Context, r *deliveriesv1.ReportEventRequest) (*deliveriesv1.ReportEventResponse, error) {
	if err := f.rec(ctx, "event", r); err != nil {
		return nil, err
	}
	return &deliveriesv1.ReportEventResponse{Delivery: f.del, Outcome: f.outcome}, nil
}

func (f *fakeDeliveries) CancelDelivery(ctx context.Context, r *deliveriesv1.CancelDeliveryRequest) (*deliveriesv1.CancelDeliveryResponse, error) {
	if err := f.rec(ctx, "cancel", r); err != nil {
		return nil, err
	}
	return &deliveriesv1.CancelDeliveryResponse{Delivery: f.del}, nil
}

func (f *fakeDeliveries) ErasePersonalData(ctx context.Context, r *deliveriesv1.ErasePersonalDataRequest) (*deliveriesv1.ErasePersonalDataResponse, error) {
	if err := f.rec(ctx, "erase", r); err != nil {
		return nil, err
	}
	return &deliveriesv1.ErasePersonalDataResponse{Delivery: f.del}, nil
}

func (f *fakeDeliveries) UpsertCourier(ctx context.Context, r *deliveriesv1.UpsertCourierRequest) (*deliveriesv1.UpsertCourierResponse, error) {
	if err := f.rec(ctx, "courier", r); err != nil {
		return nil, err
	}
	return &deliveriesv1.UpsertCourierResponse{Courier: f.courier, Created: f.created}, nil
}

// rpcFail is what the real service returns: a Connect error plus a dt-code
// trailer on the call info, and one FieldViolation detail each.
func rpcFail(code connect.Code, dtCode string, violations ...*deliveriesv1.FieldViolation) func(context.Context) error {
	return func(ctx context.Context) error {
		if info, ok := connect.CallInfoForClientContext(ctx); ok && dtCode != "" {
			info.ResponseTrailer().Set(rpcauth.CodeKey, dtCode)
		}
		e := connect.NewError(code, "secret internal detail")
		for _, v := range violations {
			d, err := connectproto.NewErrorDetail(v)
			if err != nil {
				panic(err)
			}
			e = e.WithDetail(d)
		}
		return e
	}
}

// gw is a gateway edge with the production routes over a fake client.
type gw struct {
	*edge
	fake  *fakeDeliveries
	key   testKey
	audit *memAudit
	rpc   *bytes.Buffer // log of the deliveries calls
}

func newGW(t *testing.T, scopes ...domain.Scope) *gw {
	t.Helper()
	if len(scopes) == 0 {
		scopes = domain.AllScopes
	}
	k := mint(t, apikey.Test, actor, scopes...)
	f := newFake()
	au := &memAudit{}
	rpcLogs := &bytes.Buffer{}
	e := newEdge(t, []testKey{k},
		withRoutes(httpapi.Routes(httpapi.Deps{Deliveries: f, Log: logx.New(rpcLogs, slog.LevelDebug, "gateway")})...),
		withMiddleware(func(m *httpapi.Middleware) { m.Audit = httpapi.Audit(au, fixedClock{}, m.Log) }))
	return &gw{edge: e, fake: f, key: k, audit: au, rpc: rpcLogs}
}

var idemCounter atomic.Int64

func (g *gw) req(method, target, body string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Authorization", "Bearer "+g.key.raw)
	if method == http.MethodPost {
		r.Header.Set("Idempotency-Key", "k-"+strconv.FormatInt(idemCounter.Add(1), 10))
	}
	return r
}

// checkResponse validates rec against the embedded OpenAPI spec.
func checkResponse(t *testing.T, req *http.Request, rec *httptest.ResponseRecorder) {
	t.Helper()
	doc, err := httpapi.LoadSpec(api.Spec)
	if err != nil {
		t.Fatal(err)
	}
	doc.Servers = nil
	router, err := gorillamux.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	specReq := req.Clone(context.Background())
	specReq.URL.Path = strings.TrimPrefix(req.URL.Path, "/v1")
	route, params, err := router.FindRoute(specReq)
	if err != nil {
		t.Fatal(err)
	}
	if responses := route.Operation.Responses; responses.Status(rec.Code) == nil && responses.Default() == nil {
		// Generic infrastructure answers (500, 503) and the 409/422 problems
		// of other operations are not declared per operation.
		var p api.Problem
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Code == "" || p.Status != rec.Code {
			t.Fatalf("undeclared status %d without a problem body: %s", rec.Code, rec.Body.String())
		}
		return
	}
	err = openapi3filter.ValidateResponse(context.Background(), &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{
			Request: specReq, PathParams: params, Route: route,
			Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
		},
		Status:  rec.Code,
		Header:  rec.Header(),
		Body:    io.NopCloser(bytes.NewReader(rec.Body.Bytes())),
		Options: &openapi3filter.Options{IncludeResponseStatus: true},
	})
	if err != nil {
		t.Fatalf("%s %s: response (%d) does not match the contract: %v\n%s", req.Method, req.URL.Path, rec.Code, err, rec.Body.String())
	}
}

// do serves req and checks the status and the response contract.
func (g *gw) do(t *testing.T, req *http.Request, want int) *httptest.ResponseRecorder {
	t.Helper()
	rec := g.edge.do(req)
	if rec.Code != want {
		t.Fatalf("%s %s = %d, want %d: %s", req.Method, req.URL.Path, rec.Code, want, rec.Body.String())
	}
	checkResponse(t, req, rec)
	return rec
}

func TestCreateDeliveryMapsRequestAndResponse(t *testing.T) {
	t.Parallel()
	g := newGW(t)
	rec := g.do(t, g.req("POST", "/v1/deliveries", create), http.StatusCreated)

	got, ok := g.fake.last().(*deliveriesv1.CreateDeliveryRequest)
	if !ok {
		t.Fatalf("last request = %T", g.fake.last())
	}
	sc := got.GetScope()
	if sc.GetTenantId() != tenantID || sc.GetEnvironment() != eventsv1.Environment_ENVIRONMENT_TEST || sc.GetActorId() != actor {
		t.Fatalf("scope = %v, want the principal's", sc)
	}
	if got.GetExternalId() != "order-1042" || got.GetPickup().GetName() != "Pizzaria Central" ||
		got.GetPickup().GetLocation().GetLat() != -23.5614 || got.GetDropoff().GetLocation().GetLng() != -46.6333 ||
		got.GetDropoff().GetLabel() != "Apt 4" || got.GetCustomer().GetFirstName() != "Ana" ||
		got.GetItemsSummary() != "2 pizzas" || got.GetItemCount() != 2 || got.GetNotes() != "ring twice" ||
		got.GetMetadata()["table"] != "12" || got.GetDeliveryModel() != "" {
		t.Fatalf("mapped request = %v", got)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["id"] != delID || body["status"] != "confirmed" || body["environment"] != "test" || body["created_at"] != stamp {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// Coordinates keep their precision (no float32 round trip).
	if !strings.Contains(rec.Body.String(), `"lat":-23.5614`) {
		t.Fatalf("coordinates lost precision: %s", rec.Body.String())
	}
	for _, k := range []string{"notes", "items_summary", "item_count", "courier"} {
		if v, present := body[k]; !present || v != nil {
			t.Errorf("%s = %v (present %t), want null", k, v, present)
		}
	}
	if m, ok := body["metadata"].(map[string]any); !ok || len(m) != 0 {
		t.Errorf("metadata = %v, want {}", body["metadata"])
	}
}

func TestCreateIgnoresScopeInBody(t *testing.T) {
	t.Parallel()
	g := newGW(t)
	body := strings.Replace(create, `{"external_id"`, `{"tenant_id":"ten_evil","scope":{"tenant_id":"ten_evil"},"external_id"`, 1)
	rec := g.edge.do(g.req("POST", "/v1/deliveries", body))
	if rec.Code == http.StatusCreated {
		got := g.fake.last().(*deliveriesv1.CreateDeliveryRequest)
		if got.GetScope().GetTenantId() != tenantID {
			t.Fatalf("scope taken from body: %v", got.GetScope())
		}
		return
	}
	wantProblem(t, rec, http.StatusBadRequest, "validation_failed")
	if g.fake.count() != 0 {
		t.Fatal("the service was called with a body carrying scope")
	}
}

func TestRPCErrorToProblem(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		fail   func(context.Context) error
		status int
		code   string
	}{
		{"not_found", rpcFail(connect.CodeNotFound, "not_found"), 404, "not_found"},
		{"not_found by connect code only", rpcFail(connect.CodeNotFound, ""), 404, "not_found"},
		{"validation_failed", rpcFail(connect.CodeInvalidArgument, "validation_failed"), 400, "validation_failed"},
		{"invalid_transition", rpcFail(connect.CodeFailedPrecondition, "invalid_transition"), 409, "invalid_transition"},
		{"external_id_conflict", rpcFail(connect.CodeAlreadyExists, "external_id_conflict"), 409, "external_id_conflict"},
		{"dropoff_locked", rpcFail(connect.CodeFailedPrecondition, "dropoff_locked"), 409, "dropoff_locked"},
		{"delivery_active", rpcFail(connect.CodeFailedPrecondition, "delivery_active"), 409, "delivery_active"},
		{"model_not_allowed", rpcFail(connect.CodeFailedPrecondition, "model_not_allowed"), 422, "model_not_allowed"},
		{"courier_not_found", rpcFail(connect.CodeInvalidArgument, "courier_not_found"), 422, "courier_not_found"},
		{"unavailable", rpcFail(connect.CodeUnavailable, ""), 503, "service_unavailable"},
		{"deadline", rpcFail(connect.CodeDeadlineExceeded, ""), 503, "service_unavailable"},
		{"transport error", func(context.Context) error { return errors.New("dial tcp: connection refused") }, 503, "service_unavailable"},
		{"internal", rpcFail(connect.CodeInternal, ""), 500, "internal_error"},
		{"bad token is our bug, not the partner's", rpcFail(connect.CodeUnauthenticated, ""), 500, "internal_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := newGW(t)
			g.fake.fail = tc.fail
			rec := g.do(t, g.req("GET", "/v1/deliveries/"+delID, ""), tc.status)
			wantProblem(t, rec, tc.status, tc.code)
			if strings.Contains(rec.Body.String(), "secret internal detail") || strings.Contains(rec.Body.String(), "refused") {
				t.Fatalf("service error leaked: %s", rec.Body.String())
			}
		})
	}

	t.Run("field violations become errors[]", func(t *testing.T) {
		t.Parallel()
		g := newGW(t)
		g.fake.fail = rpcFail(connect.CodeInvalidArgument, "validation_failed",
			&deliveriesv1.FieldViolation{Field: "dropoff.location", Code: "too_far", Message: "more than 100 km from the pickup"},
			&deliveriesv1.FieldViolation{Field: "notes", Code: "too_long", Message: "too long"})
		rec := g.do(t, g.req("POST", "/v1/deliveries", create), http.StatusBadRequest)
		p := wantProblem(t, rec, 400, "validation_failed")
		if len(p.Errors) != 2 || p.Errors[0].Field != "dropoff.location" || p.Errors[0].Code != "too_far" ||
			p.Errors[1].Field != "notes" || p.Errors[1].Message != "too long" {
			t.Fatalf("errors = %+v", p.Errors)
		}
	})
}

func TestDeliveriesUnavailable503(t *testing.T) {
	t.Parallel()
	g := newGW(t)
	g.fake.fail = rpcFail(connect.CodeUnavailable, "")
	rec := g.do(t, g.req("POST", "/v1/deliveries", create), http.StatusServiceUnavailable)
	wantProblem(t, rec, 503, "service_unavailable")
	if !strings.Contains(g.rpc.String(), "deliveries_rpc_unavailable") {
		t.Fatalf("503 not logged: %s", g.rpc.String())
	}
	if strings.Contains(g.rpc.String(), g.key.raw) {
		t.Fatal("log holds the key")
	}
}

func TestListQueryParams(t *testing.T) {
	t.Parallel()
	g := newGW(t)
	g.fake.list = &deliveriesv1.ListDeliveriesResponse{Data: []*deliveriesv1.Delivery{g.fake.del}, HasMore: true}
	q := "?status=confirmed&delivery_model=direct&external_id=order-1042&limit=5&starting_after=" + delID +
		"&created_gte=2026-10-01T00:00:00Z&created_lt=2026-10-23T00:00:00Z"
	rec := g.do(t, g.req("GET", "/v1/deliveries"+q, ""), http.StatusOK)
	got := g.fake.last().(*deliveriesv1.ListDeliveriesRequest)
	if got.GetStatus() != "confirmed" || got.GetDeliveryModel() != "direct" || got.GetExternalId() != "order-1042" ||
		got.GetLimit() != 5 || got.GetStartingAfter() != delID ||
		got.GetCreatedGte().AsTime() != time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) ||
		got.GetCreatedLt().AsTime() != time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC) ||
		got.GetScope().GetTenantId() != tenantID {
		t.Fatalf("request = %v", got)
	}
	var out struct {
		Data    []map[string]any `json:"data"`
		HasMore bool             `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Data) != 1 || !out.HasMore {
		t.Fatalf("body = %s (%v)", rec.Body.String(), err)
	}

	// No filters: zero values (the service applies its default of 20).
	g.do(t, g.req("GET", "/v1/deliveries", ""), http.StatusOK)
	if got := g.fake.last().(*deliveriesv1.ListDeliveriesRequest); (got.GetLimit() != 0 && got.GetLimit() != 20) || got.GetCreatedGte() != nil {
		t.Fatalf("defaults = %v", got)
	}

	// An empty page is `data: []`, not null.
	g.fake.list = &deliveriesv1.ListDeliveriesResponse{}
	rec = g.do(t, g.req("GET", "/v1/deliveries", ""), http.StatusOK)
	if !strings.Contains(rec.Body.String(), `"data":[]`) {
		t.Fatalf("empty page = %s", rec.Body.String())
	}

	for _, bad := range []string{"limit=0", "limit=101", "limit=abc", "status=nope", "starting_after=bad"} {
		before := g.fake.count()
		rec := g.edge.do(g.req("GET", "/v1/deliveries?"+bad, ""))
		wantProblem(t, rec, http.StatusBadRequest, "validation_failed")
		if g.fake.count() != before {
			t.Errorf("%s: the service was called", bad)
		}
	}
}

func TestCreateAuditTargetIsNewID(t *testing.T) {
	t.Parallel()
	g := newGW(t)
	g.do(t, g.req("POST", "/v1/deliveries", create), http.StatusCreated)
	g.do(t, g.req("POST", "/v1/deliveries/"+delID+"/cancel", ""), http.StatusOK)
	g.fake.created = true
	g.do(t, g.req("PUT", "/v1/couriers/driver-77", `{"display_name":"Carlos","vehicle":"car"}`), http.StatusCreated)

	want := map[string]string{"deliveries.create": delID, "deliveries.cancel": delID, "couriers.upsert": "driver-77"}
	got := map[string]string{}
	for _, e := range g.audit.all() {
		got[e.Action] = e.Target
	}
	for action, target := range want {
		if got[action] != target {
			t.Errorf("audit target of %s = %q, want %q (all: %v)", action, got[action], target, got)
		}
	}
}

func TestDeliveryRoutesMapRequests(t *testing.T) {
	t.Parallel()
	g := newGW(t)

	g.do(t, g.req("GET", "/v1/deliveries/"+delID, ""), http.StatusOK)
	if r := g.fake.last().(*deliveriesv1.GetDeliveryRequest); r.GetId() != delID || r.GetScope().GetActorId() != actor {
		t.Fatalf("get = %v", r)
	}

	g.do(t, g.req("POST", "/v1/deliveries/"+delID+"/assignment", `{"courier_id":"`+couID+`"}`), http.StatusOK)
	if r := g.fake.last().(*deliveriesv1.AssignCourierRequest); r.GetId() != delID || r.GetCourierId() != couID {
		t.Fatalf("assign = %v", r)
	}

	g.do(t, g.req("POST", "/v1/deliveries/"+delID+"/cancel", `{"reason":"customer called"}`), http.StatusOK)
	if r := g.fake.last().(*deliveriesv1.CancelDeliveryRequest); r.GetId() != delID || r.GetReason() != "customer called" {
		t.Fatalf("cancel = %v", r)
	}
	g.do(t, g.req("POST", "/v1/deliveries/"+delID+"/cancel", ""), http.StatusOK) // body is optional
	if r := g.fake.last().(*deliveriesv1.CancelDeliveryRequest); r.GetReason() != "" {
		t.Fatalf("cancel without body = %v", r)
	}

	g.do(t, g.req("DELETE", "/v1/deliveries/"+delID+"/personal-data", ""), http.StatusOK)
	if r := g.fake.last().(*deliveriesv1.ErasePersonalDataRequest); r.GetId() != delID {
		t.Fatalf("erase = %v", r)
	}

	// Events: every outcome is a 200 carrying {delivery, outcome}.
	for _, outcome := range []string{"applied", "duplicate", "stale", "conflict", "recorded"} {
		g.fake.outcome = outcome
		rec := g.do(t, g.req("POST", "/v1/deliveries/"+delID+"/events",
			`{"event_id":"e-1","type":"pickup_confirmed","by":"store","occurred_at":"2026-10-22T14:00:00Z","reason":"r"}`), http.StatusOK)
		if !strings.Contains(rec.Body.String(), `"outcome":"`+outcome+`"`) || !strings.Contains(rec.Body.String(), `"delivery":{`) {
			t.Fatalf("event body = %s", rec.Body.String())
		}
	}
	r := g.fake.last().(*deliveriesv1.ReportEventRequest)
	if r.GetId() != delID || r.GetEventId() != "e-1" || r.GetType() != "pickup_confirmed" || r.GetBy() != "store" ||
		r.GetReason() != "r" || r.GetActor() != "" || r.GetOccurredAt().AsTime() != time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC) {
		t.Fatalf("event = %v", r)
	}
	g.do(t, g.req("POST", "/v1/deliveries/"+delID+"/events", `{"event_id":"e-2","type":"arriving"}`), http.StatusOK)
	if r := g.fake.last().(*deliveriesv1.ReportEventRequest); r.GetOccurredAt() != nil || r.GetBy() != "" {
		t.Fatalf("optional event fields = %v", r)
	}
}

func TestDeliveryWithCourierAndConfirmations(t *testing.T) {
	t.Parallel()
	g := newGW(t)
	ts := timestamppb.New(time.Date(2026, 10, 22, 14, 5, 0, 0, time.UTC))
	notes, label, first, sum := "n", "l", "Ana", "2 pizzas"
	count := int32(2)
	g.fake.del.Courier = &deliveriesv1.CourierRef{Id: couID, ExternalId: "driver-77", DisplayName: "Carlos", Vehicle: "e_bike"}
	g.fake.del.PickupConfirmations = []*deliveriesv1.PickupConfirmation{{By: "courier", At: ts}}
	g.fake.del.Notes, g.fake.del.Dropoff.Label, g.fake.del.Customer.FirstName = &notes, &label, &first
	g.fake.del.ItemsSummary, g.fake.del.ItemCount = &sum, &count
	g.fake.del.Metadata = map[string]string{"table": "12"}
	g.fake.del.Environment = eventsv1.Environment_ENVIRONMENT_LIVE
	rec := g.do(t, g.req("GET", "/v1/deliveries/"+delID, ""), http.StatusOK)
	var d api.Delivery
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Courier == nil || d.Courier.Id != couID || len(d.PickupConfirmations) != 1 || d.PickupConfirmations[0].By != "courier" ||
		*d.Notes != "n" || *d.Dropoff.Label != "l" || *d.Customer.FirstName != "Ana" || *d.ItemCount != 2 ||
		d.Metadata["table"] != "12" || d.Environment != api.EnvironmentLive {
		t.Fatalf("delivery = %s", rec.Body.String())
	}
}

func TestPatchBuildsUpdateMask(t *testing.T) {
	t.Parallel()
	g := newGW(t)
	patch := func(body string) *deliveriesv1.UpdateDeliveryRequest {
		t.Helper()
		g.do(t, g.req("PATCH", "/v1/deliveries/"+delID, body), http.StatusOK)
		r := g.fake.last().(*deliveriesv1.UpdateDeliveryRequest)
		if r.GetId() != delID || r.GetScope().GetTenantId() != tenantID {
			t.Fatalf("scope or id wrong: %v", r)
		}
		return r
	}
	paths := func(r *deliveriesv1.UpdateDeliveryRequest) map[string]bool {
		m := map[string]bool{}
		for _, p := range r.GetUpdateMask().GetPaths() {
			m[p] = true
		}
		return m
	}

	for _, field := range []string{"dropoff_label", "notes"} {
		get := func(r *deliveriesv1.UpdateDeliveryRequest) (*string, bool) {
			if field == "notes" {
				return r.Notes, paths(r)["notes"]
			}
			return r.DropoffLabel, paths(r)["dropoff_label"]
		}
		t.Run(field+" null clears", func(t *testing.T) {
			v, masked := get(patch(`{"` + field + `":null}`))
			if !masked || v != nil {
				t.Fatalf("masked=%t value=%v, want masked and unset", masked, v)
			}
		})
		t.Run(field+" value sets", func(t *testing.T) {
			v, masked := get(patch(`{"` + field + `":"new"}`))
			if !masked || v == nil || *v != "new" {
				t.Fatalf("masked=%t value=%v, want masked and new", masked, v)
			}
		})
		t.Run(field+" empty string sets empty", func(t *testing.T) {
			v, masked := get(patch(`{"` + field + `":""}`))
			if !masked || v == nil || *v != "" {
				t.Fatalf("masked=%t value=%v, want masked and empty", masked, v)
			}
		})
		t.Run(field+" absent is untouched", func(t *testing.T) {
			other := "notes"
			if field == "notes" {
				other = "dropoff_label"
			}
			r := patch(`{"` + other + `":"x"}`)
			if _, masked := get(r); masked {
				t.Fatalf("%s masked although absent: %v", field, r.GetUpdateMask().GetPaths())
			}
		})
	}

	t.Run("metadata", func(t *testing.T) {
		r := patch(`{"metadata":null}`)
		if !paths(r)["metadata"] || len(r.GetMetadata()) != 0 {
			t.Fatalf("null: %v %v", r.GetUpdateMask().GetPaths(), r.GetMetadata())
		}
		r = patch(`{"metadata":{"a":"1","b":"2"}}`)
		if !paths(r)["metadata"] || len(r.GetMetadata()) != 2 || r.GetMetadata()["b"] != "2" {
			t.Fatalf("value: %v %v", r.GetUpdateMask().GetPaths(), r.GetMetadata())
		}
		r = patch(`{"notes":"x"}`)
		if paths(r)["metadata"] {
			t.Fatalf("absent metadata masked: %v", r.GetUpdateMask().GetPaths())
		}
	})

	t.Run("dropoff", func(t *testing.T) {
		r := patch(`{"dropoff":{"location":{"lat":-23.5,"lng":-46.6}},"notes":null}`)
		if !paths(r)["dropoff"] || !paths(r)["notes"] || len(paths(r)) != 2 ||
			r.GetDropoff().GetLat() != -23.5 || r.GetDropoff().GetLng() != -46.6 {
			t.Fatalf("mask %v dropoff %v", r.GetUpdateMask().GetPaths(), r.GetDropoff())
		}
	})

	t.Run("empty body is refused", func(t *testing.T) {
		before := g.fake.count()
		wantProblem(t, g.edge.do(g.req("PATCH", "/v1/deliveries/"+delID, `{}`)), http.StatusBadRequest, "validation_failed")
		if g.fake.count() != before {
			t.Fatal("the service was called")
		}
	})
}

func TestMetadataKeyRuleIsEnforced(t *testing.T) {
	t.Parallel()
	g := newGW(t)
	long := strings.Repeat("k", 41)
	for name, tc := range map[string]struct{ method, target, body string }{
		"create dash":     {"POST", "/v1/deliveries", strings.Replace(create, `"table"`, `"bad-key"`, 1)},
		"create too long": {"POST", "/v1/deliveries", strings.Replace(create, `"table"`, `"`+long+`"`, 1)},
		"create empty":    {"POST", "/v1/deliveries", strings.Replace(create, `"table"`, `""`, 1)},
		"patch dash":      {"PATCH", "/v1/deliveries/" + delID, `{"metadata":{"bad-key":"x"}}`},
	} {
		before := g.fake.count()
		rec := g.edge.do(g.req(tc.method, tc.target, tc.body))
		p := wantProblem(t, rec, http.StatusBadRequest, "validation_failed")
		if len(p.Errors) == 0 {
			t.Errorf("%s: no errors[]", name)
		}
		if g.fake.count() != before {
			t.Errorf("%s: the service was called", name)
		}
	}
	// A valid key still passes.
	g.do(t, g.req("POST", "/v1/deliveries", strings.Replace(create, `"table"`, `"Table_12"`, 1)), http.StatusCreated)
}

func TestRoutesDeclareScopesAndStatuses(t *testing.T) {
	t.Parallel()
	want := map[string]struct {
		method, pattern string
		scope           domain.Scope
		status          int
	}{
		"deliveries.create":              {"POST", "/v1/deliveries", domain.ScopeDeliveriesWrite, 201},
		"deliveries.list":                {"GET", "/v1/deliveries", domain.ScopeDeliveriesRead, 200},
		"deliveries.get":                 {"GET", "/v1/deliveries/{id}", domain.ScopeDeliveriesRead, 200},
		"deliveries.update":              {"PATCH", "/v1/deliveries/{id}", domain.ScopeDeliveriesWrite, 200},
		"deliveries.assign":              {"POST", "/v1/deliveries/{id}/assignment", domain.ScopeDeliveriesWrite, 200},
		"deliveries.report_event":        {"POST", "/v1/deliveries/{id}/events", domain.ScopeDeliveriesWrite, 200},
		"deliveries.cancel":              {"POST", "/v1/deliveries/{id}/cancel", domain.ScopeDeliveriesWrite, 200},
		"deliveries.erase_personal_data": {"DELETE", "/v1/deliveries/{id}/personal-data", domain.ScopeDeliveriesWrite, 200},
		"couriers.upsert":                {"PUT", "/v1/couriers/{external_id}", domain.ScopeCouriersWrite, 200},
	}
	seen := 0
	for _, rt := range httpapi.Routes(httpapi.Deps{}) {
		w, ok := want[rt.Name]
		if !ok {
			continue
		}
		seen++
		if rt.Method != w.method || rt.Pattern != w.pattern || rt.Scope != w.scope || rt.SuccessStatus != w.status ||
			rt.Env != httpapi.AnyEnv || len(rt.Credentials) != 1 || rt.Credentials[0] != domain.CredentialSecretKey {
			t.Errorf("%s = %+v", rt.Name, rt)
		}
	}
	if seen != len(want) {
		t.Fatalf("found %d of %d routes", seen, len(want))
	}
	if rt := httpapi.Routes(httpapi.Deps{})[0]; rt.Name != "me.get" || rt.SuccessStatus != 200 {
		t.Fatalf("first route = %+v", rt)
	}
}

func TestScopeIsEnforcedPerRoute(t *testing.T) {
	t.Parallel()
	g := newGW(t, domain.ScopeDeliveriesRead)
	wantProblem(t, g.edge.do(g.req("POST", "/v1/deliveries", create)), http.StatusForbidden, "insufficient_scope")
	wantProblem(t, g.edge.do(g.req("PUT", "/v1/couriers/x", `{"display_name":"C","vehicle":"car"}`)), http.StatusForbidden, "insufficient_scope")
	g.do(t, g.req("GET", "/v1/deliveries", ""), http.StatusOK)
	if n := g.fake.count(); n != 1 {
		t.Fatalf("service called %d times, want 1", n)
	}
}

func TestLiveKeyAccepted(t *testing.T) {
	t.Parallel()
	k := mint(t, apikey.Live, actor, domain.AllScopes...)
	f := newFake()
	e := newEdge(t, []testKey{k}, withRoutes(httpapi.Routes(httpapi.Deps{Deliveries: f})...))
	r := httptest.NewRequest("GET", "/v1/deliveries", nil)
	r.Header.Set("Authorization", "Bearer "+k.raw)
	if rec := e.do(r); rec.Code != http.StatusOK {
		t.Fatalf("live key = %d: %s", rec.Code, rec.Body.String())
	}
	if got := f.last().(*deliveriesv1.ListDeliveriesRequest).GetScope().GetEnvironment(); got != eventsv1.Environment_ENVIRONMENT_LIVE {
		t.Fatalf("environment = %v", got)
	}
}

// stubService is a real Connect handler: the dt-code trailer and the bearer
// token travel over HTTP exactly as they do in production.
type stubService struct {
	deliveriesv1connect.UnimplementedDeliveriesServiceHandler
	dtCode string
	code   connect.Code
}

func (s stubService) GetDelivery(ctx context.Context, _ *deliveriesv1.GetDeliveryRequest) (*deliveriesv1.GetDeliveryResponse, error) {
	rpcauth.SetCode(ctx, s.dtCode)
	return nil, connect.NewError(s.code, "internal detail")
}

func TestRealConnectTransportCarriesDtCode(t *testing.T) {
	t.Parallel()
	const token = "test-only-internal-rpc-token-0123456789"
	for _, tc := range []struct {
		dtCode string
		code   connect.Code
		status int
	}{
		{"invalid_transition", connect.CodeFailedPrecondition, 409},
		{"courier_not_found", connect.CodeInvalidArgument, 422},
		{"not_found", connect.CodeNotFound, 404},
	} {
		si, err := rpcauth.NewServerInterceptor(token)
		if err != nil {
			t.Fatal(err)
		}
		srv := connect.NewServer(si)
		deliveriesv1connect.RegisterDeliveriesServiceHandler(srv, stubService{dtCode: tc.dtCode, code: tc.code})
		mux := http.NewServeMux()
		connecthttp.Mount(mux, srv)
		ts := httptest.NewServer(mux)
		t.Cleanup(ts.Close)
		ci, err := rpcauth.NewClientInterceptor(token)
		if err != nil {
			t.Fatal(err)
		}
		client := deliveriesv1connect.NewDeliveriesServiceClient(connect.NewClient(connecthttp.NewTransport(ts.Client(), ts.URL), ci))

		k := mint(t, apikey.Test, actor, domain.AllScopes...)
		e := newEdge(t, []testKey{k}, withRoutes(httpapi.Routes(httpapi.Deps{Deliveries: client})...))
		r := httptest.NewRequest("GET", "/v1/deliveries/"+delID, nil)
		r.Header.Set("Authorization", "Bearer "+k.raw)
		wantProblem(t, e.do(r), tc.status, tc.dtCode)
	}
}

func TestCoordinatesRoundTripExactly(t *testing.T) {
	t.Parallel()
	const lat, lng = -23.56141234567, -46.65591234567
	g := newGW(t)
	g.fake.del.Pickup.Location = &eventsv1.LatLng{Lat: lat, Lng: lng}
	body := `{"external_id":"order-1","pickup":{"location":{"lat":-23.56141234567,"lng":-46.65591234567},"name":"P"},` +
		`"dropoff":{"location":{"lat":-23.5505,"lng":-46.6333}}}`
	rec := g.do(t, g.req("POST", "/v1/deliveries", body), http.StatusCreated)

	got := g.fake.last().(*deliveriesv1.CreateDeliveryRequest)
	if l := got.GetPickup().GetLocation(); l.GetLat() != lat || l.GetLng() != lng {
		t.Fatalf("rpc pickup = %v, want %v,%v", l, lat, lng)
	}
	var resp struct {
		Pickup struct {
			Location struct{ Lat, Lng float64 } `json:"location"`
		} `json:"pickup"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if l := resp.Pickup.Location; l.Lat != lat || l.Lng != lng {
		t.Fatalf("response pickup = %v, want %v,%v: %s", l, lat, lng, rec.Body.String())
	}
}

func TestCreateWithReadyAtRoundTrip(t *testing.T) {
	t.Parallel()
	g := newGW(t)
	ready := time.Date(2026, 10, 22, 14, 15, 0, 0, time.UTC)
	g.fake.del.ReadyAt = timestamppb.New(ready)
	body := strings.Replace(create, `"notes"`, `"ready_at":"2026-10-22T14:15:00Z","notes"`, 1)
	rec := g.do(t, g.req("POST", "/v1/deliveries", body), http.StatusCreated)

	got := g.fake.last().(*deliveriesv1.CreateDeliveryRequest)
	if !got.GetReadyAt().AsTime().Equal(ready) {
		t.Fatalf("ready_at = %v", got.GetReadyAt())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["ready_at"] != "2026-10-22T14:15:00Z" {
		t.Fatalf("body = %s", rec.Body.String())
	}

	// Without a ready_at the field is present and null.
	g.fake.del.ReadyAt = nil
	rec = g.do(t, g.req("POST", "/v1/deliveries", create), http.StatusCreated)
	if got := g.fake.last().(*deliveriesv1.CreateDeliveryRequest); got.ReadyAt != nil {
		t.Fatalf("ready_at = %v, want unset", got.ReadyAt)
	}
	out = nil
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if v, present := out["ready_at"]; !present || v != nil {
		t.Fatalf("ready_at = %v (present %t), want null", v, present)
	}
}

func TestPatchReadyAt(t *testing.T) {
	t.Parallel()
	g := newGW(t)
	patch := func(body string) *deliveriesv1.UpdateDeliveryRequest {
		t.Helper()
		g.do(t, g.req("PATCH", "/v1/deliveries/"+delID, body), http.StatusOK)
		return g.fake.last().(*deliveriesv1.UpdateDeliveryRequest)
	}
	r := patch(`{"ready_at":"2026-10-22T14:25:00Z"}`)
	if !slices.Contains(r.GetUpdateMask().GetPaths(), "ready_at") ||
		!r.GetReadyAt().AsTime().Equal(time.Date(2026, 10, 22, 14, 25, 0, 0, time.UTC)) {
		t.Fatalf("set: %v", r)
	}
	r = patch(`{"ready_at":null}`)
	if !slices.Contains(r.GetUpdateMask().GetPaths(), "ready_at") || r.ReadyAt != nil {
		t.Fatalf("clear: %v", r)
	}
	r = patch(`{"notes":"x"}`)
	if slices.Contains(r.GetUpdateMask().GetPaths(), "ready_at") {
		t.Fatalf("absent but masked: %v", r)
	}
}
