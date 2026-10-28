package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	trackingv1 "github.com/raduolivas/dt/backend/gen/dt/tracking/v1"
	"github.com/raduolivas/dt/backend/gen/dt/tracking/v1/trackingv1connect"
	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/services/gateway/api"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

// trackingAPI serves the partner's tracking routes and the courier-location
// relay over the tracking internal RPC. As for deliveries, the scope of every
// call comes from the authenticated principal, never from the request, and
// tracking answers NotFound for anything outside it.
type trackingAPI struct {
	c   trackingv1connect.TrackingServiceClient // nil: tracking_unavailable
	log *slog.Logger
}

func (a *trackingAPI) scope(w http.ResponseWriter, r *http.Request) (*trackingv1.Scope, bool) {
	p, ok := PrincipalFrom(r.Context())
	if !ok {
		internalError(w, r) // Mount always authenticates first
		return nil, false
	}
	if a.c == nil {
		trackingUnavailable(w, r)
		return nil, false
	}
	env := eventsv1.Environment_ENVIRONMENT_TEST
	if p.Env == domain.EnvLive {
		env = eventsv1.Environment_ENVIRONMENT_LIVE
	}
	return &trackingv1.Scope{TenantId: p.TenantID, Environment: env, ActorId: p.CredentialID}, true
}

func rfc3339(t *timestamppb.Timestamp) time.Time { return t.AsTime().UTC() }

func optTime(t *timestamppb.Timestamp) *time.Time {
	if t == nil {
		return nil
	}
	v := rfc3339(t)
	return &v
}

func optLatLng(l *eventsv1.LatLng) *api.LatLng {
	if l == nil {
		return nil
	}
	return &api.LatLng{Lat: l.GetLat(), Lng: l.GetLng()}
}

// pairs are `[lat, lng]` route points; the result is never nil, so it
// encodes as [] even for no points (callers leave a field nil for null).
func pairs(pts []*eventsv1.LatLng) [][2]float64 {
	out := make([][2]float64, len(pts))
	for i, p := range pts {
		out[i] = [2]float64{p.GetLat(), p.GetLng()}
	}
	return out
}

// partnerCourierJSON is `PartnerCourier`.
type partnerCourierJSON struct {
	ID          string      `json:"id"`
	ExternalID  string      `json:"external_id"`
	DisplayName string      `json:"display_name"`
	Vehicle     string      `json:"vehicle"`
	PhotoURL    *string     `json:"photo_url"`
	Position    *api.LatLng `json:"position"`
	HeadingDeg  *float64    `json:"heading_deg"`
	SpeedKmh    *float64    `json:"speed_kmh"`
	PositionAt  *time.Time  `json:"position_at"`
	Signal      string      `json:"signal"`
}

type partnerViewJSON struct {
	DeliveryID    string              `json:"delivery_id"`
	DeliveryModel string              `json:"delivery_model"`
	Status        string              `json:"status"`
	Courier       *partnerCourierJSON `json:"courier"`
	Pickup        struct {
		Location api.LatLng `json:"location"`
		Name     string     `json:"name"`
	} `json:"pickup"`
	Dropoff struct {
		Location api.LatLng `json:"location"`
		Label    *string    `json:"label"`
	} `json:"dropoff"`
	Route struct {
		ToPickup  [][2]float64 `json:"to_pickup"`
		ToDropoff [][2]float64 `json:"to_dropoff"`
	} `json:"route"`
	EtaSeconds *int32 `json:"eta_seconds"`
	EtaWindow  *struct {
		MinSeconds int32 `json:"min_seconds"`
		MaxSeconds int32 `json:"max_seconds"`
	} `json:"eta_window"`
	EtaState           string   `json:"eta_state"`
	EtaQuality         string   `json:"eta_quality"`
	DistanceRemainingM *float64 `json:"distance_remaining_m"`
	Progress           *float64 `json:"progress"`
	Timeline           []struct {
		Status string    `json:"status"`
		At     time.Time `json:"at"`
	} `json:"timeline"`
	UpdatedAt time.Time `json:"updated_at"`
	Version   int64     `json:"version"`
}

func toPartnerViewJSON(v *trackingv1.PartnerView) partnerViewJSON {
	out := partnerViewJSON{
		DeliveryID: v.GetDeliveryId(), DeliveryModel: v.GetDeliveryModel(), Status: v.GetStatus(),
		EtaSeconds: v.EtaSeconds, EtaState: v.GetEtaState(), EtaQuality: v.GetEtaQuality(),
		DistanceRemainingM: v.DistanceRemainingM, Progress: v.Progress,
		UpdatedAt: rfc3339(v.GetUpdatedAt()), Version: v.GetVersion(),
	}
	out.Pickup.Location, out.Pickup.Name = api.LatLng{Lat: v.GetPickup().GetLat(), Lng: v.GetPickup().GetLng()}, v.GetPickupName()
	out.Dropoff.Location = api.LatLng{Lat: v.GetDropoff().GetLat(), Lng: v.GetDropoff().GetLng()}
	out.Dropoff.Label = v.DropoffLabel
	out.Route.ToDropoff = pairs(v.GetRoute().GetToDropoff())
	if v.GetRoute().GetHasToPickup() {
		out.Route.ToPickup = pairs(v.GetRoute().GetToPickup())
	}
	if w := v.GetEtaWindow(); w != nil {
		out.EtaWindow = &struct {
			MinSeconds int32 `json:"min_seconds"`
			MaxSeconds int32 `json:"max_seconds"`
		}{w.GetMinSeconds(), w.GetMaxSeconds()}
	}
	out.Timeline = make([]struct {
		Status string    `json:"status"`
		At     time.Time `json:"at"`
	}, len(v.GetTimeline()))
	for i, e := range v.GetTimeline() {
		out.Timeline[i].Status, out.Timeline[i].At = e.GetStatus(), rfc3339(e.GetAt())
	}
	if c := v.GetCourier(); c != nil {
		out.Courier = &partnerCourierJSON{ID: c.GetId(), ExternalID: c.GetExternalId(), DisplayName: c.GetDisplayName(),
			Vehicle: c.GetVehicle(), PhotoURL: c.PhotoUrl, Position: optLatLng(c.GetPosition()),
			HeadingDeg: c.HeadingDeg, SpeedKmh: c.SpeedKmh, PositionAt: optTime(c.GetPositionAt()), Signal: c.GetSignal()}
	}
	return out
}

// getTracking serves GET /v1/deliveries/{id}/tracking.
func (a *trackingAPI) getTracking(w http.ResponseWriter, r *http.Request) {
	sc, ok := a.scope(w, r)
	if !ok {
		return
	}
	v, err := callRPC(r.Context(), func(ctx context.Context) (*trackingv1.PartnerView, error) {
		return a.c.GetPartnerView(ctx, &trackingv1.GetPartnerViewRequest{Scope: sc, DeliveryId: r.PathValue("id")})
	})
	if err != nil {
		writeRPCError(w, r, a.log, err)
		return
	}
	writeJSON(w, http.StatusOK, toPartnerViewJSON(v))
}

// trailFixJSON is `TrailFix`.
type trailFixJSON struct {
	T          time.Time `json:"t"`
	Lat        float64   `json:"lat"`
	Lng        float64   `json:"lng"`
	AccuracyM  float64   `json:"accuracy_m"`
	SpeedMps   *float64  `json:"speed_mps"`
	HeadingDeg *float64  `json:"heading_deg"`
	Source     string    `json:"source"`
}

func toTrailFixJSON(f *trackingv1.Fix) trailFixJSON {
	return trailFixJSON{T: rfc3339(f.GetT()), Lat: f.GetLat(), Lng: f.GetLng(), AccuracyM: f.GetAccuracyM(),
		SpeedMps: f.SpeedMps, HeadingDeg: f.HeadingDeg, Source: f.GetSource()}
}

// listTrail serves GET /v1/deliveries/{id}/trail (json or geojson). The OpenAPI
// validation has checked format, limit and starting_after.
func (a *trackingAPI) listTrail(w http.ResponseWriter, r *http.Request) {
	sc, ok := a.scope(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	limit := 0 // absent: the service default (500); validation fills it in anyway
	if s := q.Get("limit"); s != "" {
		n, err := strconv.ParseInt(s, 10, 32)
		if err != nil {
			httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusBadRequest, Code: "validation_failed",
				Detail: "The request does not match the API contract."})
			return
		}
		limit = int(n)
	}
	pg, err := callRPC(r.Context(), func(ctx context.Context) (*trackingv1.TrailPage, error) {
		return a.c.ListTrail(ctx, &trackingv1.ListTrailRequest{Scope: sc, DeliveryId: r.PathValue("id"),
			StartingAfter: q.Get("starting_after"), Limit: int32(limit)}) //nolint:gosec // 1..1000, validated
	})
	if err != nil {
		writeRPCError(w, r, a.log, err)
		return
	}
	var next *string
	if c := pg.GetNextCursor(); pg.GetHasMore() && c != "" {
		next = &c
	}
	if q.Get("format") == "geojson" {
		w.Header().Set("Content-Type", "application/geo+json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(trailGeoJSON(pg, next))
		return
	}
	fixes := make([]trailFixJSON, len(pg.GetFixes()))
	for i, f := range pg.GetFixes() {
		fixes[i] = toTrailFixJSON(f)
	}
	var simplified [][2]float64
	if !pg.GetRawAvailable() && len(pg.GetSimplified()) > 0 {
		simplified = pairs(pg.GetSimplified())
	}
	writeJSON(w, http.StatusOK, struct {
		Data         []trailFixJSON `json:"data"`
		HasMore      bool           `json:"has_more"`
		NextCursor   *string        `json:"next_cursor"`
		RawAvailable bool           `json:"raw_available"`
		Simplified   [][2]float64   `json:"simplified"`
	}{fixes, pg.GetHasMore(), next, pg.GetRawAvailable(), simplified})
}

type geoFeature struct {
	Type     string `json:"type"`
	Geometry struct {
		Type        string `json:"type"`
		Coordinates any    `json:"coordinates"`
	} `json:"geometry"`
	Properties map[string]any `json:"properties"`
}

// trailGeoJSON is the page as a FeatureCollection: one LineString (the
// page's fixes, or the simplified route once the raw fixes are gone; left
// out when it would have fewer than two positions) plus a Point per fix.
// Coordinates are [lng, lat]. has_more, next_cursor and raw_available are
// foreign members, which GeoJSON allows, so paging works in this format too.
func trailGeoJSON(pg *trackingv1.TrailPage, next *string) any {
	feature := func(geom string, coords any, props map[string]any) geoFeature {
		f := geoFeature{Type: "Feature", Properties: props}
		f.Geometry.Type, f.Geometry.Coordinates = geom, coords
		return f
	}
	features := []geoFeature{}
	var line [][2]float64
	for _, f := range pg.GetFixes() {
		line = append(line, [2]float64{f.GetLng(), f.GetLat()})
	}
	if !pg.GetRawAvailable() {
		for _, p := range pg.GetSimplified() {
			line = append(line, [2]float64{p.GetLng(), p.GetLat()})
		}
	}
	if len(line) >= 2 {
		features = append(features, feature("LineString", line, map[string]any{"kind": "trail"}))
	}
	for _, f := range pg.GetFixes() {
		fx := toTrailFixJSON(f)
		features = append(features, feature("Point", [2]float64{fx.Lng, fx.Lat}, map[string]any{
			"kind": "fix", "t": fx.T, "accuracy_m": fx.AccuracyM, "speed_mps": fx.SpeedMps,
			"heading_deg": fx.HeadingDeg, "source": fx.Source}))
	}
	return struct {
		Type         string       `json:"type"`
		Features     []geoFeature `json:"features"`
		HasMore      bool         `json:"has_more"`
		NextCursor   *string      `json:"next_cursor"`
		RawAvailable bool         `json:"raw_available"`
	}{"FeatureCollection", features, pg.GetHasMore(), next, pg.GetRawAvailable()}
}

// listProofPoints serves GET /v1/deliveries/{id}/proof-points.
func (a *trackingAPI) listProofPoints(w http.ResponseWriter, r *http.Request) {
	sc, ok := a.scope(w, r)
	if !ok {
		return
	}
	res, err := callRPC(r.Context(), func(ctx context.Context) (*trackingv1.ProofPoints, error) {
		return a.c.ListProofPoints(ctx, &trackingv1.ListProofPointsRequest{Scope: sc, DeliveryId: r.PathValue("id")})
	})
	if err != nil {
		writeRPCError(w, r, a.log, err)
		return
	}
	type point struct {
		Status     string      `json:"status"`
		At         time.Time   `json:"at"`
		Actor      string      `json:"actor"`
		Position   *api.LatLng `json:"position"`
		PositionAt *time.Time  `json:"position_at"`
		AccuracyM  *float64    `json:"accuracy_m"`
	}
	data := make([]point, len(res.GetPoints()))
	for i, p := range res.GetPoints() {
		data[i] = point{Status: p.GetStatus(), At: rfc3339(p.GetAt()), Actor: p.GetActor(),
			Position: optLatLng(p.GetPosition()), PositionAt: optTime(p.GetPositionAt()), AccuracyM: p.AccuracyM}
	}
	writeJSON(w, http.StatusOK, struct {
		Data []point `json:"data"`
	}{data})
}

// simulate serves POST /v1/test/deliveries/{id}/simulate: it starts a
// sandbox simulation in tracking (test keys only; the route is TestOnly).
// The body is optional; the OpenAPI validation has checked its ranges.
func (a *trackingAPI) simulate(w http.ResponseWriter, r *http.Request) {
	sc, ok := a.scope(w, r)
	if !ok {
		return
	}
	var in struct {
		SpeedFactor float64 `json:"speed_factor"`
		SignalGap   *struct {
			AfterS    int32 `json:"after_s"`
			DurationS int32 `json:"duration_s"`
		} `json:"signal_gap"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	req := &trackingv1.SimulateRequest{Scope: sc, DeliveryId: r.PathValue("id"), SpeedFactor: in.SpeedFactor}
	if g := in.SignalGap; g != nil {
		req.Gap = &trackingv1.SignalGap{AfterS: g.AfterS, DurationS: g.DurationS}
	}
	res, err := callRPC(r.Context(), func(ctx context.Context) (*trackingv1.SimulateResponse, error) {
		return a.c.Simulate(ctx, req)
	})
	if err != nil {
		writeRPCError(w, r, a.log, err)
		return
	}
	writeJSON(w, http.StatusAccepted, struct {
		SimulationID string `json:"simulation_id"`
	}{res.GetSimulationId()})
}
