package httpapi

import (
	"context"
	"net/http"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	trackingv1 "github.com/raduolivas/dt/backend/gen/dt/tracking/v1"
)

// reportLocations serves POST /v1/couriers/{id}/locations: it relays the
// fixes to tracking, which applies the purpose limitation (only a courier
// with a non-final delivery), the plausibility rules and the fan-out, and
// answers how many fixes it accepted and dropped. The size (16 KB), the shape
// and the rate limit were checked before this point.
func (a *trackingAPI) reportLocations(w http.ResponseWriter, r *http.Request) {
	sc, ok := a.scope(w, r)
	if !ok {
		return
	}
	var in struct {
		Fixes []struct {
			T          time.Time `json:"t"`
			Lat        float64   `json:"lat"`
			Lng        float64   `json:"lng"`
			AccuracyM  float64   `json:"accuracy_m"`
			SpeedMps   *float64  `json:"speed_mps"`
			HeadingDeg *float64  `json:"heading_deg"`
		} `json:"fixes"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	fixes := make([]*trackingv1.Fix, len(in.Fixes))
	for i, f := range in.Fixes {
		fixes[i] = &trackingv1.Fix{T: timestamppb.New(f.T), Lat: f.Lat, Lng: f.Lng, AccuracyM: f.AccuracyM,
			SpeedMps: f.SpeedMps, HeadingDeg: f.HeadingDeg}
	}
	res, err := callRPC(r.Context(), func(ctx context.Context) (*trackingv1.IngestResponse, error) {
		return a.c.IngestLocations(ctx, &trackingv1.IngestRequest{Scope: sc, CourierId: r.PathValue("id"),
			Fixes: fixes, Source: "relay"})
	})
	if err != nil {
		writeRPCError(w, r, a.log, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Accepted int32 `json:"accepted"`
		Dropped  int32 `json:"dropped"`
	}{res.GetAccepted(), res.GetDropped()})
}
