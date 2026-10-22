package httpapi

import (
	"context"
	"net/http"
	"time"

	deliveriesv1 "github.com/raduolivas/dt/backend/gen/dt/deliveries/v1"
)

type courierJSON struct {
	ID          string    `json:"id"`
	ExternalID  string    `json:"external_id"`
	DisplayName string    `json:"display_name"`
	Vehicle     string    `json:"vehicle"`
	PhotoURL    *string   `json:"photo_url"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// upsertCourier serves PUT /v1/couriers/{external_id}: 201 when the courier
// was created, 200 when it already existed. The audit target is the path's
// external id (the default).
func (a *deliveriesAPI) upsertCourier(w http.ResponseWriter, r *http.Request) {
	sc, ok := scopeFor(w, r)
	if !ok {
		return
	}
	var in struct {
		DisplayName string  `json:"display_name"`
		Vehicle     string  `json:"vehicle"`
		PhotoURL    *string `json:"photo_url"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	res, err := callRPC(r.Context(), func(ctx context.Context) (*deliveriesv1.UpsertCourierResponse, error) {
		return a.c.UpsertCourier(ctx, &deliveriesv1.UpsertCourierRequest{
			Scope: sc, ExternalId: r.PathValue("external_id"),
			DisplayName: in.DisplayName, Vehicle: in.Vehicle, PhotoUrl: in.PhotoURL,
		})
	})
	if err != nil {
		writeRPCError(w, r, a.log, err)
		return
	}
	c := res.GetCourier()
	status := http.StatusOK
	if res.GetCreated() {
		status = http.StatusCreated
	}
	writeJSON(w, status, courierJSON{
		ID: c.GetId(), ExternalID: c.GetExternalId(), DisplayName: c.GetDisplayName(), Vehicle: c.GetVehicle(),
		PhotoURL: c.PhotoUrl, CreatedAt: c.GetCreatedAt().AsTime(), UpdatedAt: c.GetUpdatedAt().AsTime(),
	})
}
