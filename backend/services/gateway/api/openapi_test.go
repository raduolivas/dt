package api_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/raduolivas/dt/backend/services/gateway/api"
)

func loadSpec(t *testing.T) *openapi3.T {
	t.Helper()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(api.Spec)
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	if err := doc.Validate(loader.Context); err != nil {
		t.Fatalf("validate spec: %v", err)
	}
	return doc
}

// TestOpenAPIExamplesValidate checks that every request and response example
// in the contract validates against its own schema.
func TestOpenAPIExamplesValidate(t *testing.T) {
	doc := loadSpec(t)
	checked := 0
	check := func(where string, content openapi3.Content) {
		for mediaType, mt := range content {
			if mt.Schema == nil {
				continue
			}
			for name, ex := range mt.Examples {
				checked++
				err := mt.Schema.Value.VisitJSON(ex.Value.Value, openapi3.EnableFormatValidation(), openapi3.EnableJSONSchema2020())
				if err != nil {
					t.Errorf("%s %s example %q: %v", where, mediaType, name, err)
				}
			}
		}
	}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			where := fmt.Sprintf("%s %s", method, path)
			if op.RequestBody != nil {
				check(where+" request", op.RequestBody.Value.Content)
			}
			for status, resp := range op.Responses.Map() {
				check(where+" "+status, resp.Value.Content)
			}
		}
	}
	// The delivery/courier operations and the M3 tracking operations carry
	// examples; guard against a silent no-op.
	if checked < 35 {
		t.Fatalf("only %d examples checked", checked)
	}
	// Every M3 operation must carry an example on each JSON request and
	// response body, so none can be added without one.
	for _, id := range m3Operations {
		op := operationByID(doc, id)
		if op == nil {
			t.Errorf("operation %s missing", id)
			continue
		}
		if op.RequestBody != nil {
			for mt, c := range op.RequestBody.Value.Content {
				if len(c.Examples) == 0 {
					t.Errorf("%s request %s has no example", id, mt)
				}
			}
		}
		for status, resp := range op.Responses.Map() {
			if !strings.HasPrefix(status, "2") {
				continue
			}
			for mt, c := range resp.Value.Content {
				if len(c.Examples) == 0 {
					t.Errorf("%s %s %s has no example", id, status, mt)
				}
			}
		}
	}
}

// m3Operations are the operation ids M3 adds.
var m3Operations = []string{
	"rotateTrackingLink", "getDeliveryTracking", "listDeliveryTrail", "listDeliveryProofPoints",
	"reportCourierLocations", "simulateDelivery", "createTrackingSession", "refreshTrackingSession",
	"getTrackingSnapshot",
}

func operationByID(doc *openapi3.T, id string) *openapi3.Operation {
	for _, item := range doc.Paths.Map() {
		for _, op := range item.Operations() {
			if op.OperationID == id {
				return op
			}
		}
	}
	return nil
}

// TestOpenAPIOperations pins the operation ids and the problem+json error
// shape of every non-2xx response.
func TestOpenAPIOperations(t *testing.T) {
	doc := loadSpec(t)
	want := map[string]string{
		"createDelivery": "POST /deliveries", "listDeliveries": "GET /deliveries",
		"getDelivery": "GET /deliveries/{id}", "updateDelivery": "PATCH /deliveries/{id}",
		"assignCourier": "POST /deliveries/{id}/assignment", "reportDeliveryEvent": "POST /deliveries/{id}/events",
		"cancelDelivery": "POST /deliveries/{id}/cancel", "erasePersonalData": "DELETE /deliveries/{id}/personal-data",
		"upsertCourier": "PUT /couriers/{external_id}",
		// M3
		"rotateTrackingLink": "POST /deliveries/{id}/tracking-link/rotate", "getDeliveryTracking": "GET /deliveries/{id}/tracking",
		"listDeliveryTrail": "GET /deliveries/{id}/trail", "listDeliveryProofPoints": "GET /deliveries/{id}/proof-points",
		"reportCourierLocations": "POST /couriers/{id}/locations", "simulateDelivery": "POST /test/deliveries/{id}/simulate",
		"createTrackingSession": "POST /tracking/session", "refreshTrackingSession": "POST /tracking/session/refresh",
		"getTrackingSnapshot": "GET /tracking/snapshot",
	}
	got := map[string]string{}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			got[op.OperationID] = method + " " + path
			for status, resp := range op.Responses.Map() {
				if strings.HasPrefix(status, "2") {
					continue
				}
				if resp.Value.Content.Get("application/problem+json") == nil {
					t.Errorf("%s %s: %s is not problem+json", method, path, status)
				}
			}
		}
	}
	for id, route := range want {
		if got[id] != route {
			t.Errorf("operation %s = %q, want %q", id, got[id], route)
		}
	}
	if _, ok := doc.Components.SecuritySchemes["CourierToken"]; ok {
		t.Error("security scheme CourierToken should be removed until M5")
	}
	if _, ok := doc.Components.SecuritySchemes["ViewerToken"]; !ok {
		t.Error("security scheme ViewerToken is missing")
	}
	// Only the session exchange is unauthenticated; the other viewer routes use the viewer token.
	if sec := operationByID(doc, "createTrackingSession").Security; sec == nil || len(*sec) != 0 {
		t.Error("createTrackingSession must declare security: []")
	}
	for _, id := range []string{"refreshTrackingSession", "getTrackingSnapshot"} {
		sec := operationByID(doc, id).Security
		if sec == nil || len(*sec) != 1 || (*sec)[0]["ViewerToken"] == nil {
			t.Errorf("%s must use ViewerToken", id)
		}
	}
}

// TestOpenAPIRequestLimits proves the input limits are enforced by the schema
// keywords, not only documented. It uses the JSON Schema 2020-12 validator
// because the legacy validator in kin-openapi ignores propertyNames.
func TestOpenAPIRequestLimits(t *testing.T) {
	doc := loadSpec(t)
	schema := func(name string) *openapi3.Schema { return doc.Components.Schemas[name].Value }
	base := func() map[string]any {
		return map[string]any{
			"external_id": "order-1",
			"pickup":      map[string]any{"location": map[string]any{"lat": 1.0, "lng": 2.0}, "name": "P"},
			"dropoff":     map[string]any{"location": map[string]any{"lat": 1.0, "lng": 2.0}},
		}
	}
	meta := func(n int) map[string]any {
		m := map[string]any{}
		for i := range n {
			m[fmt.Sprintf("k%d", i)] = "v"
		}
		return m
	}
	with := func(k string, v any) map[string]any { b := base(); b[k] = v; return b }

	cases := []struct {
		name   string
		schema string
		body   any
		valid  bool
	}{
		{"base", "CreateDeliveryRequest", base(), true},
		{"unknown field", "CreateDeliveryRequest", with("bogus", 1), false},
		{"empty external_id", "CreateDeliveryRequest", with("external_id", ""), false},
		{"non-ascii external_id", "CreateDeliveryRequest", with("external_id", "pedido-é"), false},
		{"external_id 256", "CreateDeliveryRequest", with("external_id", strings.Repeat("a", 256)), false},
		{"external_id 255", "CreateDeliveryRequest", with("external_id", strings.Repeat("a", 255)), true},
		{"items_summary 201", "CreateDeliveryRequest", with("items_summary", strings.Repeat("a", 201)), false},
		{"notes 501", "CreateDeliveryRequest", with("notes", strings.Repeat("a", 501)), false},
		{"item_count 1000", "CreateDeliveryRequest", with("item_count", 1000), false},
		{"item_count -1", "CreateDeliveryRequest", with("item_count", -1), false},
		{"item_count 999", "CreateDeliveryRequest", with("item_count", 999), true},
		{"first_name 51", "CreateDeliveryRequest", with("customer", map[string]any{"first_name": strings.Repeat("a", 51)}), false},
		{"model unknown", "CreateDeliveryRequest", with("delivery_model", "drone"), false},
		{"lat out of range", "CreateDeliveryRequest", with("dropoff", map[string]any{"location": map[string]any{"lat": 91.0, "lng": 0.0}}), false},
		{"metadata 50 keys", "CreateDeliveryRequest", with("metadata", meta(50)), true},
		{"metadata 51 keys", "CreateDeliveryRequest", with("metadata", meta(51)), false},
		{"metadata long value", "CreateDeliveryRequest", with("metadata", map[string]any{"a": strings.Repeat("a", 501)}), false},
		{"metadata bad key", "CreateDeliveryRequest", with("metadata", map[string]any{"a-b": "v"}), false},
		{"metadata empty key", "CreateDeliveryRequest", with("metadata", map[string]any{"": "v"}), false},
		{"metadata key 41", "CreateDeliveryRequest", with("metadata", map[string]any{strings.Repeat("a", 41): "v"}), false},
		{"metadata key 40", "CreateDeliveryRequest", with("metadata", map[string]any{strings.Repeat("a", 40): "v"}), true},
		{"metadata non-string", "CreateDeliveryRequest", with("metadata", map[string]any{"a": 1}), false},
		{"patch clear", "UpdateDeliveryRequest", map[string]any{"notes": nil, "dropoff_label": nil, "metadata": nil}, true},
		{"patch null dropoff", "UpdateDeliveryRequest", map[string]any{"dropoff": nil}, false},
		{"patch empty", "UpdateDeliveryRequest", map[string]any{}, false},
		{"event ok", "ReportDeliveryEventRequest", map[string]any{"event_id": "e1", "type": "at_pickup"}, true},
		{"event bad type", "ReportDeliveryEventRequest", map[string]any{"event_id": "e1", "type": "picked_up"}, false},
		{"event missing id", "ReportDeliveryEventRequest", map[string]any{"type": "failed"}, false},
		{"event bad by", "ReportDeliveryEventRequest", map[string]any{"event_id": "e1", "type": "pickup_confirmed", "by": "courier"}, false},
		{"event reason 201", "ReportDeliveryEventRequest", map[string]any{"event_id": "e1", "type": "failed", "reason": strings.Repeat("a", 201)}, false},
		{"event bad time", "ReportDeliveryEventRequest", map[string]any{"event_id": "e1", "type": "failed", "occurred_at": "yesterday"}, false},
		{"cancel empty", "CancelDeliveryRequest", map[string]any{}, true},
		{"cancel reason 201", "CancelDeliveryRequest", map[string]any{"reason": strings.Repeat("a", 201)}, false},
		{"assign bad id", "AssignCourierRequest", map[string]any{"courier_id": "del_01j9z3k8m2q4r6s8t0v2w4x6y8"}, false},
		{"courier ok", "UpsertCourierRequest", map[string]any{"display_name": "C", "vehicle": "car"}, true},
		{"courier empty name", "UpsertCourierRequest", map[string]any{"display_name": "", "vehicle": "car"}, false},
		{"courier name 51", "UpsertCourierRequest", map[string]any{"display_name": strings.Repeat("a", 51), "vehicle": "car"}, false},
		{"courier vehicle", "UpsertCourierRequest", map[string]any{"display_name": "C", "vehicle": "boat"}, false},
		{"courier http photo", "UpsertCourierRequest", map[string]any{"display_name": "C", "vehicle": "car", "photo_url": "http://x.example/a.jpg"}, false},
		{"courier https photo", "UpsertCourierRequest", map[string]any{"display_name": "C", "vehicle": "car", "photo_url": "https://x.example/a.jpg"}, true},
		{"courier photo 2049", "UpsertCourierRequest", map[string]any{"display_name": "C", "vehicle": "car", "photo_url": "https://x.example/" + strings.Repeat("a", 2040)}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := schema(c.schema).VisitJSON(c.body, openapi3.EnableFormatValidation(), openapi3.EnableJSONSchema2020())
			if c.valid && err != nil {
				t.Fatalf("want valid, got %v", err)
			}
			if !c.valid && err == nil {
				t.Fatal("want invalid, got valid")
			}
		})
	}
}
