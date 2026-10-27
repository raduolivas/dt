package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	trackingv1 "github.com/raduolivas/dt/backend/gen/dt/tracking/v1"
	"github.com/raduolivas/dt/backend/gen/dt/tracking/v1/trackingv1connect"
	"github.com/raduolivas/dt/backend/pkg/apikey"
	httpapi "github.com/raduolivas/dt/backend/services/gateway/internal/adapters/inbound/http"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

// fakeTracking is a TrackingServiceClient that records the last request of
// each call and answers from the configured fields.
type fakeTracking struct {
	trackingv1connect.TrackingServiceClient
	mu    sync.Mutex
	calls []string
	req   any

	view    *trackingv1.PartnerView
	trail   *trackingv1.TrailPage
	proofs  *trackingv1.ProofPoints
	ingest  *trackingv1.IngestResponse
	failing func(ctx context.Context) error
}

func (f *fakeTracking) rec(ctx context.Context, name string, req any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
	f.req = req
	if f.failing != nil {
		return f.failing(ctx)
	}
	return nil
}

func (f *fakeTracking) last() any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.req
}

func (f *fakeTracking) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeTracking) GetPartnerView(ctx context.Context, r *trackingv1.GetPartnerViewRequest) (*trackingv1.PartnerView, error) {
	if err := f.rec(ctx, "view", r); err != nil {
		return nil, err
	}
	return f.view, nil
}

func (f *fakeTracking) ListTrail(ctx context.Context, r *trackingv1.ListTrailRequest) (*trackingv1.TrailPage, error) {
	if err := f.rec(ctx, "trail", r); err != nil {
		return nil, err
	}
	return f.trail, nil
}

func (f *fakeTracking) ListProofPoints(ctx context.Context, r *trackingv1.ListProofPointsRequest) (*trackingv1.ProofPoints, error) {
	if err := f.rec(ctx, "proofs", r); err != nil {
		return nil, err
	}
	return f.proofs, nil
}

func (f *fakeTracking) IngestLocations(ctx context.Context, r *trackingv1.IngestRequest) (*trackingv1.IngestResponse, error) {
	if err := f.rec(ctx, "ingest", r); err != nil {
		return nil, err
	}
	return f.ingest, nil
}

func ll(lat, lng float64) *eventsv1.LatLng { return &eventsv1.LatLng{Lat: lat, Lng: lng} }

func ts(s string) *timestamppb.Timestamp {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return timestamppb.New(t)
}

func newFakeTracking() *fakeTracking {
	speed, heading, eta, dist, prog, acc := 17.5, 84.0, int32(420), 1350.0, 0.42, 6.5
	return &fakeTracking{
		ingest: &trackingv1.IngestResponse{Accepted: 2, Dropped: 1},
		view: &trackingv1.PartnerView{
			DeliveryId: delID, DeliveryModel: "direct", Status: "confirmed",
			Courier: &trackingv1.PartnerCourier{
				Id: couID, ExternalId: "driver-77", DisplayName: "Carlos", Vehicle: "e_bike",
				Position: ll(-23.5561, -46.6492), HeadingDeg: &heading, SpeedKmh: &speed,
				PositionAt: ts("2026-10-22T14:20:03Z"), Signal: "live",
			},
			Pickup: ll(-23.5614, -46.6559), PickupName: "Pizzaria Central", Dropoff: ll(-23.5505, -46.6333),
			Route:      &trackingv1.RouteLegs{ToDropoff: []*eventsv1.LatLng{ll(-23.5561, -46.6492), ll(-23.5505, -46.6333)}},
			EtaSeconds: &eta, EtaState: "live", EtaQuality: "osrm", DistanceRemainingM: &dist, Progress: &prog,
			Timeline:  []*trackingv1.TimelineEntry{{Status: "confirmed", At: ts("2026-10-22T14:00:00Z")}},
			UpdatedAt: ts("2026-10-22T14:20:03.410Z"), Version: 37,
		},
		trail: &trackingv1.TrailPage{
			RawAvailable: true, HasMore: true, NextCursor: "cursor-2",
			Fixes: []*trackingv1.Fix{
				{T: ts("2026-10-22T14:15:03Z"), Lat: -23.5612, Lng: -46.6557, AccuracyM: acc, SpeedMps: &speed, HeadingDeg: &heading, Source: "relay"},
				{T: ts("2026-10-22T14:15:06Z"), Lat: -23.5611, Lng: -46.6551, AccuracyM: 7, Source: "relay"},
			},
		},
		proofs: &trackingv1.ProofPoints{Points: []*trackingv1.ProofPoint{
			{Status: "at_pickup", At: ts("2026-10-22T14:12:00Z"), Actor: "courier", Position: ll(-23.5613, -46.6558),
				PositionAt: ts("2026-10-22T14:11:57Z"), AccuracyM: &acc},
			{Status: "delivered", At: ts("2026-10-22T14:40:00Z"), Actor: "courier"},
		}},
	}
}

// trackGW is a gateway edge with the production routes over fake clients,
// with the rate limiter, idempotency and audit hooks mounted.
type trackGW struct {
	*edge
	track *fakeTracking
	dels  *fakeDeliveries
	key   testKey
	audit *memAudit
}

func newTrackGW(t *testing.T, lim app.Limiter, scopes ...domain.Scope) *trackGW {
	t.Helper()
	if len(scopes) == 0 {
		scopes = domain.AllScopes
	}
	k := mint(t, apikey.Test, actor, scopes...)
	ft, fd, au := newFakeTracking(), newFake(), &memAudit{}
	e := newEdge(t, []testKey{k},
		withRoutes(httpapi.Routes(httpapi.Deps{Deliveries: fd, Tracking: ft})...),
		withMiddleware(func(m *httpapi.Middleware) {
			if lim != nil {
				m.RateLimit = httpapi.PartnerRateLimit(lim, m.Log)
			}
			m.Idempotency = httpapi.Idempotency(&matrixStore{seen: map[app.IdempotencyScope][]byte{}}, fixedClock{}, m.Log)
			m.Audit = httpapi.Audit(au, fixedClock{}, m.Log)
		}))
	return &trackGW{edge: e, track: ft, dels: fd, key: k, audit: au}
}

func (g *trackGW) req(method, target, body string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Authorization", "Bearer "+g.key.raw)
	if method == http.MethodPost {
		r.Header.Set("Idempotency-Key", "t-"+strconv.FormatInt(idemCounter.Add(1), 10))
	}
	return r
}

func TestTrackingView(t *testing.T) {
	t.Parallel()
	g := newTrackGW(t, nil)
	req := g.req(http.MethodGet, "/v1/deliveries/"+delID+"/tracking", "")
	rec := g.do(req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	checkResponse(t, req, rec)
	var v map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	c, _ := v["courier"].(map[string]any)
	pos, _ := c["position"].(map[string]any)
	if v["delivery_id"] != delID || v["status"] != "confirmed" || v["version"] != float64(37) ||
		c["id"] != couID || c["external_id"] != "driver-77" || c["signal"] != "live" || c["speed_kmh"] != 17.5 ||
		c["position_at"] != "2026-10-22T14:20:03Z" || pos["lat"] != -23.5561 || c["photo_url"] != nil {
		t.Fatalf("view = %s", rec.Body.String())
	}
	route, _ := v["route"].(map[string]any)
	if route["to_pickup"] != nil || len(route["to_dropoff"].([]any)) != 2 || v["eta_window"] != nil || v["eta_seconds"] != float64(420) {
		t.Fatalf("route = %v", route)
	}
	d, _ := v["dropoff"].(map[string]any)
	if d["label"] != nil {
		t.Fatalf("dropoff = %v", d)
	}
	rq := g.track.last().(*trackingv1.GetPartnerViewRequest)
	if rq.GetDeliveryId() != delID || rq.GetScope().GetTenantId() != tenantID || rq.GetScope().GetActorId() != actor {
		t.Fatalf("request = %v", rq)
	}
	// Not audited (a live view read all day), and the deliveries service is not asked.
	if n := len(g.audit.all()); n != 0 {
		t.Fatalf("audit rows = %d, want 0", n)
	}
	if g.dels.count() != 0 {
		t.Fatal("deliveries called")
	}
}

func TestPartnerViewShowsHiddenCourier(t *testing.T) {
	t.Parallel()
	// A Direct, confirmed delivery whose courier the customer cannot see yet:
	// the partner's view carries the courier with a position.
	g := newTrackGW(t, nil)
	if g.track.view.GetDeliveryModel() != "direct" || g.track.view.GetStatus() != "confirmed" {
		t.Fatal("fixture is not Direct/confirmed")
	}
	rec := g.do(g.req(http.MethodGet, "/v1/deliveries/"+delID+"/tracking", ""))
	var v struct {
		Courier *struct {
			ID       string `json:"id"`
			Position *struct {
				Lat float64 `json:"lat"`
			} `json:"position"`
		} `json:"courier"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || v.Courier == nil || v.Courier.ID != couID || v.Courier.Position == nil {
		t.Fatalf("view = %s (%v)", rec.Body.String(), err)
	}
	// No courier assigned: null.
	g.track.view.Courier = nil
	rec = g.do(g.req(http.MethodGet, "/v1/deliveries/"+delID+"/tracking", ""))
	if !strings.Contains(rec.Body.String(), `"courier":null`) {
		t.Fatalf("view = %s", rec.Body.String())
	}
	// A courier with no fix yet: null position, no position_at.
	g.track.view.Courier = &trackingv1.PartnerCourier{Id: couID, ExternalId: "d", DisplayName: "C", Vehicle: "car", Signal: "pending"}
	req := g.req(http.MethodGet, "/v1/deliveries/"+delID+"/tracking", "")
	rec = g.do(req)
	checkResponse(t, req, rec)
	if !strings.Contains(rec.Body.String(), `"position":null`) || !strings.Contains(rec.Body.String(), `"position_at":null`) {
		t.Fatalf("view = %s", rec.Body.String())
	}
}

func TestTrailJSONAndPaging(t *testing.T) {
	t.Parallel()
	g := newTrackGW(t, nil)
	req := g.req(http.MethodGet, "/v1/deliveries/"+delID+"/trail?limit=2&starting_after=cursor-1", "")
	rec := g.do(req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	checkResponse(t, req, rec)
	var p struct {
		Data []struct {
			T        string   `json:"t"`
			Speed    *float64 `json:"speed_mps"`
			Heading  *float64 `json:"heading_deg"`
			Source   string   `json:"source"`
			Accuracy float64  `json:"accuracy_m"`
		} `json:"data"`
		HasMore      bool        `json:"has_more"`
		NextCursor   *string     `json:"next_cursor"`
		RawAvailable bool        `json:"raw_available"`
		Simplified   [][]float64 `json:"simplified"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Data) != 2 || p.Data[0].T != "2026-10-22T14:15:03Z" || p.Data[0].Speed == nil || *p.Data[0].Speed != 17.5 ||
		p.Data[1].Speed != nil || p.Data[1].Heading != nil || p.Data[0].Source != "relay" ||
		!p.HasMore || p.NextCursor == nil || *p.NextCursor != "cursor-2" || !p.RawAvailable || p.Simplified != nil {
		t.Fatalf("page = %s", rec.Body.String())
	}
	rq := g.track.last().(*trackingv1.ListTrailRequest)
	if rq.GetDeliveryId() != delID || rq.GetLimit() != 2 || rq.GetStartingAfter() != "cursor-1" {
		t.Fatalf("request = %v", rq)
	}
	// Defaults: the contract's limit default (500), which request
	// validation fills in, and no cursor.
	g.do(g.req(http.MethodGet, "/v1/deliveries/"+delID+"/trail", ""))
	if rq := g.track.last().(*trackingv1.ListTrailRequest); rq.GetLimit() != 500 || rq.GetStartingAfter() != "" {
		t.Fatalf("default request = %v", rq)
	}
	for _, q := range []string{"limit=0", "limit=1001", "limit=x", "format=xml", "starting_after="} {
		wantProblem(t, g.do(g.req(http.MethodGet, "/v1/deliveries/"+delID+"/trail?"+q, "")), http.StatusBadRequest, "validation_failed")
	}
}

func TestTrailAfterRetentionIsSimplified(t *testing.T) {
	t.Parallel()
	g := newTrackGW(t, nil)
	g.track.trail = &trackingv1.TrailPage{RawAvailable: false,
		Simplified: []*eventsv1.LatLng{ll(-23.5612, -46.6557), ll(-23.5505, -46.6333)}}
	req := g.req(http.MethodGet, "/v1/deliveries/"+delID+"/trail", "")
	rec := g.do(req)
	checkResponse(t, req, rec)
	want := `{"data":[],"has_more":false,"next_cursor":null,"raw_available":false,"simplified":[[-23.5612,-46.6557],[-23.5505,-46.6333]]}`
	if strings.TrimSpace(rec.Body.String()) != want {
		t.Fatalf("body = %s\nwant   %s", rec.Body.String(), want)
	}
	// Erased and never simplified: raw_available false, simplified null.
	g.track.trail = &trackingv1.TrailPage{}
	rec = g.do(g.req(http.MethodGet, "/v1/deliveries/"+delID+"/trail", ""))
	if !strings.Contains(rec.Body.String(), `"raw_available":false`) || !strings.Contains(rec.Body.String(), `"simplified":null`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestTrailGeoJSON(t *testing.T) {
	t.Parallel()
	g := newTrackGW(t, nil)
	req := g.req(http.MethodGet, "/v1/deliveries/"+delID+"/trail?format=geojson", "")
	rec := g.do(req)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/geo+json" {
		t.Fatalf("status = %d, content-type %q: %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	checkResponse(t, req, rec)
	var fc struct {
		Type     string `json:"type"`
		Features []struct {
			Type     string `json:"type"`
			Geometry struct {
				Type        string `json:"type"`
				Coordinates any    `json:"coordinates"`
			} `json:"geometry"`
			Properties map[string]any `json:"properties"`
		} `json:"features"`
		HasMore    bool   `json:"has_more"`
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &fc); err != nil {
		t.Fatal(err)
	}
	// One LineString plus a Point per fix; coordinates are [lng, lat].
	if fc.Type != "FeatureCollection" || len(fc.Features) != 3 {
		t.Fatalf("collection = %s", rec.Body.String())
	}
	line := fc.Features[0]
	coords, _ := line.Geometry.Coordinates.([]any)
	first, _ := coords[0].([]any)
	if line.Type != "Feature" || line.Geometry.Type != "LineString" || len(coords) != 2 || first[0] != -46.6557 || first[1] != -23.5612 ||
		line.Properties["kind"] != "trail" {
		t.Fatalf("line = %+v", line)
	}
	pt := fc.Features[1]
	pc, _ := pt.Geometry.Coordinates.([]any)
	if pt.Geometry.Type != "Point" || pc[0] != -46.6557 || pc[1] != -23.5612 || pt.Properties["kind"] != "fix" ||
		pt.Properties["t"] != "2026-10-22T14:15:03Z" || pt.Properties["accuracy_m"] != 6.5 || pt.Properties["source"] != "relay" ||
		pt.Properties["speed_mps"] != 17.5 || pt.Properties["heading_deg"] != 84.0 {
		t.Fatalf("point = %+v", pt)
	}
	if p2 := fc.Features[2].Properties; p2["speed_mps"] != nil || p2["heading_deg"] != nil {
		t.Fatalf("second point = %v", p2)
	}
	// Paging survives in the foreign members.
	if !fc.HasMore || fc.NextCursor != "cursor-2" {
		t.Fatalf("paging = %v %q", fc.HasMore, fc.NextCursor)
	}

	// A single fix has no line (a LineString needs two positions).
	g.track.trail = &trackingv1.TrailPage{RawAvailable: true, Fixes: g.track.trail.Fixes[:1]}
	rec = g.do(g.req(http.MethodGet, "/v1/deliveries/"+delID+"/trail?format=geojson", ""))
	if strings.Contains(rec.Body.String(), "LineString") || !strings.Contains(rec.Body.String(), `"Point"`) {
		t.Fatalf("one fix: %s", rec.Body.String())
	}
	// After retention: the simplified route is the line.
	g.track.trail = &trackingv1.TrailPage{Simplified: []*eventsv1.LatLng{ll(-23.5612, -46.6557), ll(-23.5505, -46.6333)}}
	rec = g.do(g.req(http.MethodGet, "/v1/deliveries/"+delID+"/trail?format=geojson", ""))
	if !strings.Contains(rec.Body.String(), `"coordinates":[[-46.6557,-23.5612],[-46.6333,-23.5505]]`) || strings.Contains(rec.Body.String(), `"Point"`) {
		t.Fatalf("simplified: %s", rec.Body.String())
	}
}

func TestProofPoints(t *testing.T) {
	t.Parallel()
	g := newTrackGW(t, nil)
	req := g.req(http.MethodGet, "/v1/deliveries/"+delID+"/proof-points", "")
	rec := g.do(req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	checkResponse(t, req, rec)
	want := `{"data":[{"status":"at_pickup","at":"2026-10-22T14:12:00Z","actor":"courier","position":{"lat":-23.5613,"lng":-46.6558},` +
		`"position_at":"2026-10-22T14:11:57Z","accuracy_m":6.5},` +
		`{"status":"delivered","at":"2026-10-22T14:40:00Z","actor":"courier","position":null,"position_at":null,"accuracy_m":null}]}`
	if strings.TrimSpace(rec.Body.String()) != want {
		t.Fatalf("body = %s\nwant   %s", rec.Body.String(), want)
	}
	g.track.proofs = &trackingv1.ProofPoints{}
	rec = g.do(g.req(http.MethodGet, "/v1/deliveries/"+delID+"/proof-points", ""))
	if strings.TrimSpace(rec.Body.String()) != `{"data":[]}` {
		t.Fatalf("empty = %s", rec.Body.String())
	}
}

func TestTrailReadIsAudited(t *testing.T) {
	t.Parallel()
	g := newTrackGW(t, nil)
	for _, p := range []string{"trail", "trail?format=geojson", "proof-points"} {
		if rec := g.do(g.req(http.MethodGet, "/v1/deliveries/"+delID+"/"+p, "")); rec.Code != http.StatusOK {
			t.Fatalf("%s = %d", p, rec.Code)
		}
	}
	rows := g.audit.all()
	if len(rows) != 3 {
		t.Fatalf("audit rows = %d, want 3: %+v", len(rows), rows)
	}
	for i, action := range []string{"deliveries.list_trail", "deliveries.list_trail", "deliveries.list_proof_points"} {
		r := rows[i]
		if r.Action != action || r.Target != delID || r.Status != 200 || r.ActorID != actor || r.TenantID != tenantID {
			t.Fatalf("row %d = %+v", i, r)
		}
	}
	// A refused read (unknown delivery) is audited with its status too, and
	// the query string (cursors) never lands in the entry.
	g.track.failing = rpcFail(connect.CodeNotFound, "not_found")
	g.do(g.req(http.MethodGet, "/v1/deliveries/"+delID+"/trail?starting_after=secret-cursor", ""))
	rows = g.audit.all()
	if len(rows) != 4 || rows[3].Status != 404 || strings.Contains(strings.Join([]string{rows[3].Target, rows[3].Action}, " "), "secret-cursor") {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestTrackingReadIsNotAudited(t *testing.T) {
	t.Parallel()
	g := newTrackGW(t, nil)
	for range 3 {
		if rec := g.do(g.req(http.MethodGet, "/v1/deliveries/"+delID+"/tracking", "")); rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
	}
	if rows := g.audit.all(); len(rows) != 0 {
		t.Fatalf("audit rows = %+v, want none", rows)
	}
}

func TestTrackingReadErrors(t *testing.T) {
	t.Parallel()
	g := newTrackGW(t, nil, domain.ScopeCouriersWrite)
	for _, p := range []string{"tracking", "trail", "proof-points"} {
		wantProblem(t, g.do(g.req(http.MethodGet, "/v1/deliveries/"+delID+"/"+p, "")), http.StatusForbidden, "insufficient_scope")
	}
	g = newTrackGW(t, nil)
	for _, p := range []string{"tracking", "trail", "proof-points"} {
		g.track.failing = rpcFail(connect.CodeNotFound, "not_found")
		wantProblem(t, g.do(g.req(http.MethodGet, "/v1/deliveries/"+delID+"/"+p, "")), http.StatusNotFound, "not_found")
		g.track.failing = rpcFail(connect.CodeUnavailable, "tracking_unavailable")
		rec := g.do(g.req(http.MethodGet, "/v1/deliveries/"+delID+"/"+p, ""))
		wantProblem(t, rec, http.StatusServiceUnavailable, "tracking_unavailable")
		if strings.Contains(rec.Body.String(), "secret internal detail") {
			t.Fatal("service error message echoed")
		}
		g.track.failing = rpcFail(connect.CodeInvalidArgument, "validation_failed")
		wantProblem(t, g.do(g.req(http.MethodGet, "/v1/deliveries/"+delID+"/"+p, "")), http.StatusBadRequest, "validation_failed")
	}
}
