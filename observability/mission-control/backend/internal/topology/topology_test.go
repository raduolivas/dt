package topology

import (
	"strings"
	"testing"
)

func TestEveryWireEndsExist(t *testing.T) {
	tp := Default()
	ids := map[string]bool{}
	for _, c := range tp.Components {
		if ids[c.ID] {
			t.Errorf("duplicate component %s", c.ID)
		}
		ids[c.ID] = true
		if c.Parent != "" && !ids[c.Parent] {
			t.Errorf("%s: parent %s missing or declared later", c.ID, c.Parent)
		}
	}
	wires := map[string]bool{}
	for _, w := range tp.Wires {
		if !ids[w.From] || !ids[w.To] {
			t.Errorf("wire %s: unknown end", w.ID)
		}
		if wires[w.ID] {
			t.Errorf("duplicate wire %s", w.ID)
		}
		wires[w.ID] = true
	}
}

func TestResolveTrackingSpans(t *testing.T) {
	tp := Default()
	cases := []struct {
		svc, name string
		attrs     map[string]string
		want      string
	}{
		{"tracking", "tracking.ingest", nil, "tracking.ingest"},
		{"tracking", "tracking.viewer_fanout", nil, "tracking.hub"},
		{"tracking", "publish tracking.courier.location_reported", nil, "tracking.outbox"},
		{"tracking", "consume deliveries.delivery.status_changed", map[string]string{"messaging.consumer.group.name": "tracking-deliveries"}, "tracking.consumers"},
		{"tracking", "hget", nil, "redis"},
		{"tracking", "GET /v1/tracking/stream", nil, "tracking.hub"},
		{"gateway", "POST /v1/courier/locations", nil, "gateway.courier_api"},
		{"gateway", "POST /v1/tracking/session", nil, "gateway.viewer_api"},
		{"gateway", "GET /v1/deliveries/{id}", nil, "gateway.partner_api"},
		{"deliveries", "/dt.deliveries.v1.DeliveriesService/CreateDelivery", nil, "deliveries.rpc"},
		{"deliveries", "deliveries.assign", nil, "deliveries.state_machine"},
		{"accounts", "publish accounts.apikey.created", nil, "accounts"},
		{"nobody", "whatever", nil, ""},
	}
	for _, c := range cases {
		if got := tp.Resolve(c.svc, c.name, c.attrs); got != c.want {
			t.Errorf("Resolve(%s,%q) = %q, want %q", c.svc, c.name, got, c.want)
		}
	}
}

func TestGatewayWires(t *testing.T) {
	var limiterRedis bool
	for _, w := range Default().Wires {
		if w.From == "gateway.rate_limiter" && w.To == "redis" && w.Channel == ChanRedis {
			limiterRedis = true
		}
		if w.To == "accounts" && w.Channel == ChanRPC && strings.HasPrefix(w.From, "gateway") {
			t.Errorf("gateway has no accounts rpc client: %s", w.ID)
		}
	}
	if !limiterRedis {
		t.Error("missing gateway.rate_limiter -> redis")
	}
}

func TestSimulator(t *testing.T) {
	tp := Default()
	found := false
	for _, w := range tp.Wires {
		if w.From == "tracking.simulator" && w.To == "deliveries.rpc" && w.Channel == ChanRPC {
			found = true
		}
	}
	if !found {
		t.Error("missing tracking.simulator -> deliveries.rpc")
	}
	if got := tp.Resolve("tracking", "/dt.tracking.v1.TrackingService/Simulate", nil); got != "tracking.simulator" {
		t.Errorf("Simulate resolved to %q", got)
	}
}

// Partner, couriers, viewers, the Postgres databases and OSRM are not traced by
// DT, so their cubes are not instrumented (they light only when seen in other spans).
func TestUninstrumentedComponents(t *testing.T) {
	want := map[string]bool{"partner": false, "courier": false, "viewer": false, "osrm": false,
		"pg.deliveries": false, "pg.tracking": false, "pg.accounts": false, "pg.gateway": false,
		"gateway": true, "deliveries": true, "tracking": true, "accounts": true, "nats": true, "redis": true}
	seen := 0
	for _, c := range Default().Components {
		if w, ok := want[c.ID]; ok {
			seen++
			if c.Instrumented != w {
				t.Errorf("%s: instrumented %v, want %v", c.ID, c.Instrumented, w)
			}
		}
	}
	if seen != len(want) {
		t.Fatalf("found %d of %d components", seen, len(want))
	}
}

func TestWebhooksNotBuilt(t *testing.T) {
	for _, c := range Default().Components {
		if c.ID == "webhooks" {
			if c.Status != "not_built" {
				t.Fatalf("status %q", c.Status)
			}
			return
		}
	}
	t.Fatal("webhooks missing")
}
