// Package contract checks the domain projections against the OpenAPI contract.
// It lives outside internal/domain because the domain is standard library only.
package contract

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

const specPath = "../../../../../contracts/openapi/dt.v1.yaml"

func schemaOf(t *testing.T, name string) *openapi3.Schema {
	t.Helper()
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(data)
	if err != nil {
		t.Fatal(err)
	}
	ref, ok := doc.Components.Schemas[name]
	if !ok {
		t.Fatalf("schema %s not found", name)
	}
	return ref.Value
}

func validate(t *testing.T, schema *openapi3.Schema, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var generic any
	if err := json.Unmarshal(b, &generic); err != nil {
		t.Fatal(err)
	}
	if err := schema.VisitJSON(generic); err != nil {
		t.Fatalf("schema violation: %v\n%s", err, b)
	}
}

func state(m domain.Model, s domain.Status) domain.State {
	t0 := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	speed := 5.0
	return domain.State{
		DeliveryID: "del_01j9z3k8m2q4r6s8t0v2w4x6y8", Model: m, Status: s,
		Pickup: domain.LatLng{Lat: 52.368, Lng: 4.903}, PickupName: "Food Hub", Dropoff: domain.LatLng{Lat: 52.374, Lng: 4.889},
		CreatedAt: t0, Version: 5, UpdatedAt: t0,
		Timeline: []domain.TimelineEntry{{Status: domain.StatusConfirmed, At: t0}},
		Courier:  &domain.Courier{ID: "cou_01j9z3k8m2q4r6s8t0v2w4x6y8", ExternalID: "ext-9", DisplayName: "Sam", Vehicle: "e_bike"},
		LastFix:  &domain.Fix{T: t0, Pos: domain.LatLng{Lat: 52.371, Lng: 4.897}, SpeedMps: &speed},
		Route: domain.Route{
			ToPickup:    []domain.LatLng{{Lat: 52.371, Lng: 4.897}, {Lat: 52.368, Lng: 4.903}},
			ToDropoff:   []domain.LatLng{{Lat: 52.368, Lng: 4.903}, {Lat: 52.374, Lng: 4.889}},
			ToPickupSec: 60, ToDropoffSec: 600,
		},
	}
}

func TestSnapshotAndPartnerViewMatchOpenAPI(t *testing.T) {
	snapSchema := schemaOf(t, "TrackingSnapshot")
	partnerSchema := schemaOf(t, "PartnerView")
	models := []domain.Model{domain.ModelDirect, domain.ModelEndToEnd}
	statuses := []domain.Status{domain.StatusConfirmed, domain.StatusCourierAssigned, domain.StatusAtPickup, domain.StatusPickedUp, domain.StatusArriving, domain.StatusDelivered, domain.StatusCancelled, domain.StatusFailed}
	for _, m := range models {
		for _, s := range statuses {
			for _, fix := range []bool{true, false} {
				for _, ready := range []bool{true, false} {
					st := state(m, s)
					vs := st.UpdatedAt
					st.CourierVisibleSince = &vs
					if !fix {
						st.LastFix = nil
					}
					if ready {
						r := st.UpdatedAt.Add(5 * time.Minute)
						st.ReadyAt = &r
					}
					now := st.UpdatedAt.Add(10 * time.Second)
					validate(t, snapSchema, domain.Snapshot(st, now))
					validate(t, partnerSchema, domain.PartnerView(st, now))
				}
			}
		}
	}
}
