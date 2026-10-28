package osrm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

// A trimmed response from osrm-routed for Dam Square to Amsterdam Centraal.
const routeBody = `{"code":"Ok","routes":[{"distance":1011.2,"duration":201.7,"weight_name":"routability","weight":201.7,
"geometry":{"type":"LineString","coordinates":[[4.89348,52.37311],[4.89476,52.37466],[4.90036,52.3772],[4.90027,52.37817]]},
"legs":[{"distance":1011.2,"duration":201.7,"summary":"","steps":[]}]}],
"waypoints":[{"hint":"x","distance":3.1,"name":"Damrak","location":[4.89348,52.37311]},{"hint":"y","distance":5.2,"name":"","location":[4.90027,52.37817]}]}`

const matchBody = `{"code":"Ok","matchings":[
{"confidence":0.9,"duration":10,"distance":100,"weight":10,"weight_name":"routability","geometry":{"type":"LineString","coordinates":[[4.9,52.37],[4.901,52.371]]}},
{"confidence":0.8,"duration":10,"distance":100,"weight":10,"weight_name":"routability","geometry":{"type":"LineString","coordinates":[[4.91,52.38],[4.911,52.381]]}}],
"tracepoints":[null,{"matchings_index":0,"waypoint_index":0}]}`

func TestOSRMClientParsesRoute(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		_, _ = w.Write([]byte(routeBody))
	}))
	defer srv.Close()
	c := New(map[string]string{"car": srv.URL}, 800*time.Millisecond)
	path, sec, err := c.Route(context.Background(), "car",
		domain.LatLng{Lat: 52.37311, Lng: 4.89348}, domain.LatLng{Lat: 52.37817, Lng: 4.90027})
	if err != nil {
		t.Fatal(err)
	}
	if sec != 202 || len(path) != 4 || path[0] != (domain.LatLng{Lat: 52.37311, Lng: 4.89348}) {
		t.Fatalf("path = %v, sec = %d", path, sec)
	}
	// OSRM takes lng,lat pairs joined by ';'.
	if !strings.HasSuffix(gotPath, "/route/v1/driving/4.893480,52.373110;4.900270,52.378170") || !strings.Contains(gotQuery, "geometries=geojson") {
		t.Fatalf("request = %s ? %s", gotPath, gotQuery)
	}
}

func TestOSRMClientErrors(t *testing.T) {
	status, body := 200, `{"code":"NoRoute","message":"no route"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := New(map[string]string{"car": srv.URL}, 800*time.Millisecond)
	a, b := domain.LatLng{Lat: 52.37, Lng: 4.89}, domain.LatLng{Lat: 52.38, Lng: 4.9}
	if _, _, err := c.Route(context.Background(), "car", a, b); err == nil {
		t.Fatal("NoRoute must be an error")
	}
	status, body = 503, "busy"
	if _, _, err := c.Route(context.Background(), "car", a, b); err == nil {
		t.Fatal("HTTP 503 must be an error")
	}
	if _, _, err := c.Route(context.Background(), "bike", a, b); err == nil {
		t.Fatal("a profile without a URL must be an error")
	}
	if _, _, err := c.Route(context.Background(), "car", a); err == nil {
		t.Fatal("one point is not a route")
	}
}

func TestOSRMClientTimesOut(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	c := New(map[string]string{"car": srv.URL}, 50*time.Millisecond)
	start := time.Now()
	_, _, err := c.Route(context.Background(), "car", domain.LatLng{Lat: 52.37, Lng: 4.89}, domain.LatLng{Lat: 52.38, Lng: 4.9})
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
}

func TestOSRMClientParsesMatch(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(matchBody))
	}))
	defer srv.Close()
	c := New(map[string]string{"bike": srv.URL}, 800*time.Millisecond)
	fixes := []domain.Fix{
		{T: time.Unix(1000, 0), Pos: domain.LatLng{Lat: 52.37, Lng: 4.9}, AccuracyM: 8},
		{T: time.Unix(1005, 0), Pos: domain.LatLng{Lat: 52.371, Lng: 4.901}, AccuracyM: 12},
	}
	path, err := c.Match(context.Background(), "bike", fixes)
	if err != nil {
		t.Fatal(err)
	}
	if len(path) != 4 || path[2] != (domain.LatLng{Lat: 52.38, Lng: 4.91}) {
		t.Fatalf("path = %v", path)
	}
	if !strings.Contains(gotQuery, "timestamps=1000;1005") || !strings.Contains(gotQuery, "radiuses=8;12") {
		t.Fatalf("query = %s", gotQuery)
	}
}

// TestOSRMAmsterdamRoute needs a running osrm-car (make osrm-data && make dev).
func TestOSRMAmsterdamRoute(t *testing.T) {
	url := os.Getenv("OSRM_CAR_URL")
	if url == "" {
		t.Skip("OSRM_CAR_URL is not set")
	}
	c := New(map[string]string{"car": url}, 5*time.Second)
	dam := domain.LatLng{Lat: 52.37311, Lng: 4.89348}
	centraal := domain.LatLng{Lat: 52.37817, Lng: 4.90027}
	path, sec, err := c.Route(context.Background(), "car", dam, centraal)
	if err != nil {
		t.Fatal(err)
	}
	if len(path) < 2 || sec < 60 || sec > 600 {
		t.Fatalf("path of %d points in %d s, want 60-600 s", len(path), sec)
	}
	if d := domain.DistanceM(path[0], dam); d > 100 {
		t.Fatalf("route starts %v m from Dam Square", d)
	}
	m, err := c.Match(context.Background(), "car", []domain.Fix{
		{T: time.Unix(1000, 0), Pos: path[0], AccuracyM: 10}, {T: time.Unix(1120, 0), Pos: path[len(path)-1], AccuracyM: 10}})
	if err != nil || len(m) < 2 {
		t.Fatalf("match = %v, %v", m, err)
	}
}

func TestOSRMClientNearest(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		_, _ = w.Write([]byte(`{"code":"Ok","waypoints":[{"hint":"x","distance":12.5,"name":"Damrak","location":[4.89348,52.37311]}]}`))
	}))
	defer srv.Close()
	c := New(map[string]string{"bike": srv.URL}, 800*time.Millisecond)
	p, err := c.Nearest(context.Background(), "bike", domain.LatLng{Lat: 52.3732, Lng: 4.8936})
	if err != nil {
		t.Fatal(err)
	}
	if p != (domain.LatLng{Lat: 52.37311, Lng: 4.89348}) {
		t.Fatalf("nearest = %v", p)
	}
	if !strings.HasSuffix(gotPath, "/nearest/v1/cycling/4.893600,52.373200") || !strings.Contains(gotQuery, "number=1") {
		t.Fatalf("request = %s ? %s", gotPath, gotQuery)
	}
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":"Ok","waypoints":[]}`))
	}))
	defer empty.Close()
	if _, err := New(map[string]string{"bike": empty.URL}, time.Second).Nearest(context.Background(), "bike", p); err == nil {
		t.Fatal("no waypoint must be an error")
	}
}
