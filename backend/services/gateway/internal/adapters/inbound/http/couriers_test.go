package httpapi_test

import (
	"net/http"
	"strings"
	"testing"

	deliveriesv1 "github.com/raduolivas/dt/backend/gen/dt/deliveries/v1"
)

func TestUpsertCourierCreatedVs200(t *testing.T) {
	t.Parallel()
	g := newGW(t)
	body := `{"display_name":"Carlos","vehicle":"e_bike","photo_url":"https://cdn.example.com/c/77.jpg"}`

	g.fake.created = true
	rec := g.do(t, g.req("PUT", "/v1/couriers/driver-77", body), http.StatusCreated)
	got := g.fake.last().(*deliveriesv1.UpsertCourierRequest)
	if got.GetExternalId() != "driver-77" || got.GetDisplayName() != "Carlos" || got.GetVehicle() != "e_bike" ||
		got.GetPhotoUrl() != "https://cdn.example.com/c/77.jpg" || got.GetScope().GetTenantId() != tenantID {
		t.Fatalf("request = %v", got)
	}
	if !strings.Contains(rec.Body.String(), `"id":"`+couID+`"`) || !strings.Contains(rec.Body.String(), `"photo_url":null`) {
		t.Fatalf("body = %s", rec.Body.String())
	}

	g.fake.created = false
	g.do(t, g.req("PUT", "/v1/couriers/driver-77", `{"display_name":"Carlos","vehicle":"car"}`), http.StatusOK)
	if got := g.fake.last().(*deliveriesv1.UpsertCourierRequest); got.PhotoUrl != nil {
		t.Fatalf("absent photo_url became %q", got.GetPhotoUrl())
	}
}
