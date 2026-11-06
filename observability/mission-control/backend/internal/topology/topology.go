// Package topology is the static map of DT's components, their working parts
// and the wires between them (design section 5). It is verified against the
// code: every entry exists in backend/ or is marked not_built.
package topology

import "strings"

// Zones.
const (
	ZoneOutside   = "outside"
	ZoneEdge      = "edge"
	ZoneServices  = "services"
	ZoneMessaging = "messaging"
	ZoneData      = "data"
)

// Channels.
const (
	ChanRPC   = "rpc"
	ChanNATS  = "nats"
	ChanRedis = "redis"
	ChanHTTP  = "http"
	ChanWSSSE = "ws_sse"
	ChanSQL   = "sql"
	ChanOSRM  = "osrm"
)

// Component is a cube (Parent == "") or a working part of one.
type Component struct {
	ID           string `json:"id"`
	Label        string `json:"label"`
	Zone         string `json:"zone"`
	Parent       string `json:"parent"`
	Instrumented bool   `json:"instrumented"`
	Status       string `json:"status"` // "" or "not_built"
}

// Wire joins two components over one channel.
type Wire struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	To      string `json:"to"`
	Channel string `json:"channel"`
}

// Topology is the whole static map.
type Topology struct {
	Components []Component
	Wires      []Wire
}

func (t Topology) has(id string) bool {
	for _, c := range t.Components {
		if c.ID == id {
			return true
		}
	}
	return false
}

// Default returns DT's topology.
func Default() Topology {
	var cs []Component
	add := func(id, label, zone, parent string, instr bool, status string) {
		cs = append(cs, Component{ID: id, Label: label, Zone: zone, Parent: parent, Instrumented: instr, Status: status})
	}
	parts := func(parent, zone string, names ...string) {
		for _, n := range names {
			add(parent+"."+n, n, zone, parent, true, "")
		}
	}

	add("partner", "Partner", ZoneOutside, "", true, "")
	add("courier", "Courier", ZoneOutside, "", false, "")
	add("viewer", "Viewer", ZoneOutside, "", false, "")

	add("gateway", "Gateway", ZoneEdge, "", true, "")
	// outbox (publishes to GATEWAY) and consumers (accounts/deliveries
	// read models) exist in gateway main.go.
	parts("gateway", ZoneEdge, "partner_api", "courier_api", "viewer_api", "rate_limiter", "outbox", "consumers")

	add("deliveries", "Deliveries", ZoneServices, "", true, "")
	parts("deliveries", ZoneServices, "rpc", "state_machine", "outbox", "consumers")
	add("tracking", "Tracking", ZoneServices, "", true, "")
	parts("tracking", ZoneServices, "ingest", "consumers", "state", "hub", "sweeper", "outbox", "simulator")
	add("accounts", "Accounts", ZoneServices, "", true, "")
	add("webhooks", "Webhooks", ZoneServices, "", false, "not_built")

	add("nats", "NATS", ZoneMessaging, "", true, "")
	for _, s := range []string{"ACCOUNTS", "DELIVERIES", "TRACKING", "GATEWAY"} {
		add("nats."+s, s, ZoneMessaging, "nats", true, "")
	}

	add("redis", "Redis", ZoneData, "", true, "")
	add("pg.deliveries", "Postgres deliveries", ZoneData, "", true, "")
	add("pg.tracking", "Postgres tracking", ZoneData, "", true, "")
	add("pg.accounts", "Postgres accounts", ZoneData, "", true, "")
	add("pg.gateway", "Postgres gateway", ZoneData, "", true, "")
	add("osrm", "OSRM", ZoneData, "", false, "")

	var ws []Wire
	w := func(from, to, ch string) { ws = append(ws, Wire{ID: from + ">" + to, From: from, To: to, Channel: ch}) }
	w("partner", "gateway.partner_api", ChanHTTP)
	w("courier", "gateway.courier_api", ChanHTTP)
	w("viewer", "gateway.viewer_api", ChanHTTP)
	w("viewer", "tracking.hub", ChanWSSSE)
	w("gateway.rate_limiter", "redis", ChanRedis)
	w("gateway.partner_api", "deliveries.rpc", ChanRPC)
	w("gateway.partner_api", "tracking.ingest", ChanRPC)
	w("gateway.courier_api", "deliveries.rpc", ChanRPC)
	w("gateway.courier_api", "tracking.ingest", ChanRPC)
	w("gateway.courier_api", "gateway.outbox", ChanSQL)
	w("gateway.outbox", "pg.gateway", ChanSQL)
	w("gateway.outbox", "nats.GATEWAY", ChanNATS)
	w("nats.ACCOUNTS", "gateway.consumers", ChanNATS)
	w("nats.DELIVERIES", "gateway.consumers", ChanNATS)
	w("gateway.consumers", "pg.gateway", ChanSQL)

	w("deliveries.rpc", "deliveries.state_machine", ChanRPC)
	w("deliveries.state_machine", "pg.deliveries", ChanSQL)
	w("deliveries.state_machine", "deliveries.outbox", ChanSQL)
	w("deliveries.outbox", "nats.DELIVERIES", ChanNATS)
	w("nats.TRACKING", "deliveries.consumers", ChanNATS)
	w("nats.ACCOUNTS", "deliveries.consumers", ChanNATS)
	w("deliveries.consumers", "deliveries.state_machine", ChanSQL)

	// DT's built-in sandbox simulator path (tracking/internal/app/simulator.go):
	// it calls the deliveries RPCs (Get, UpsertCourier, Assign, Report).
	w("tracking.simulator", "deliveries.rpc", ChanRPC)
	w("tracking.ingest", "tracking.state", ChanRedis)
	w("tracking.ingest", "osrm", ChanOSRM)
	w("tracking.ingest", "tracking.outbox", ChanSQL)
	w("tracking.outbox", "pg.tracking", ChanSQL)
	w("tracking.outbox", "nats.TRACKING", ChanNATS)
	w("tracking.state", "redis", ChanRedis)
	w("redis", "tracking.hub", ChanRedis)
	w("tracking.sweeper", "tracking.state", ChanRedis)
	w("nats.DELIVERIES", "tracking.consumers", ChanNATS)
	w("nats.ACCOUNTS", "tracking.consumers", ChanNATS)
	w("tracking.consumers", "tracking.state", ChanRedis)
	w("tracking.consumers", "pg.tracking", ChanSQL)

	w("accounts", "pg.accounts", ChanSQL)
	w("accounts", "nats.ACCOUNTS", ChanNATS)
	return Topology{Components: cs, Wires: ws}
}

var redisCommands = map[string]bool{
	"get": true, "set": true, "del": true, "hget": true, "hset": true, "hgetall": true, "hdel": true,
	"evalsha": true, "eval": true, "expire": true, "subscribe": true, "zadd": true,
	"zrange": true, "zrem": true, "incr": true, "setnx": true, "exists": true, "scan": true, "mget": true,
}

// Resolve maps a span to a component or working part id ("" when unknown).
// service is the resource attribute service.name; attrs are span attributes.
func (t Topology) Resolve(service, spanName string, attrs map[string]string) string {
	part := func(p string) string {
		if id := service + "." + p; t.has(id) {
			return id
		}
		if t.has(service) {
			return service
		}
		return ""
	}
	switch {
	case attrs["db.system"] == "redis" || attrs["db.system.name"] == "redis" ||
		strings.HasPrefix(spanName, "redis.") || redisCommands[strings.ToLower(spanName)]:
		return "redis"
	case attrs["db.system"] == "postgresql" || attrs["db.system.name"] == "postgresql":
		if id := "pg." + service; t.has(id) {
			return id
		}
	case strings.HasPrefix(spanName, "publish "):
		return part("outbox")
	case strings.HasPrefix(spanName, "consume "):
		return part("consumers")
	case service == "tracking" && spanName == "/dt.tracking.v1.TrackingService/Simulate":
		return "tracking.simulator" // DT's built-in sandbox simulator
	case strings.HasPrefix(spanName, "/dt."):
		// Connect span (client and server share the name): the callee's rpc part.
		switch strings.SplitN(strings.TrimPrefix(spanName, "/dt."), ".", 2)[0] {
		case "deliveries":
			return "deliveries.rpc"
		case "tracking":
			return "tracking.ingest"
		case "accounts":
			return "accounts"
		}
	case spanName == "tracking.ingest":
		return "tracking.ingest"
	case spanName == "tracking.apply":
		return "tracking.state"
	case spanName == "tracking.viewer_fanout":
		return "tracking.hub"
	case strings.HasPrefix(spanName, "deliveries."):
		return "deliveries.state_machine"
	}
	// HTTP server span: "<METHOD> <route pattern>".
	if i := strings.IndexByte(spanName, ' '); i > 0 && strings.HasPrefix(spanName[i+1:], "/") {
		route := spanName[i+1:]
		switch service {
		case "gateway":
			switch {
			case strings.HasPrefix(route, "/v1/courier/"):
				return "gateway.courier_api"
			case strings.HasPrefix(route, "/v1/tracking/"), strings.HasPrefix(route, "/t/"), strings.HasPrefix(route, "/logos/"):
				return "gateway.viewer_api"
			default:
				return "gateway.partner_api"
			}
		case "tracking":
			if strings.HasPrefix(route, "/v1/tracking/") {
				return "tracking.hub"
			}
			return "tracking.ingest"
		case "deliveries":
			return "deliveries.rpc"
		}
	}
	if t.has(service) {
		return service
	}
	return ""
}
