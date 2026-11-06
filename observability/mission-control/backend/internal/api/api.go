// Package api serves Mission Control's read-only JSON API under /api and the
// SSE live stream (spec §4.5, §6). Every JSON string is data: nothing here
// builds HTML.
package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/topology"
)

// WireTraffic is aggregate traffic on one wire (Task 7 fills it in).
type WireTraffic struct {
	Wire   string `json:"wire"`
	Count  int64  `json:"count"`
	Failed int64  `json:"failed"`
	// Rate is events per second, ErrorRate the failed fraction (0..1), P95Ms the 95th percentile duration.
	Rate      float64 `json:"rate"`
	ErrorRate float64 `json:"errorRate"`
	P95Ms     float64 `json:"p95Ms"`
}

// Link is an "Open in..." deep link (Task 7).
type Link struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// TrafficSource supplies aggregate traffic per wire (ruling R13). Task 7 plugs
// in metrics.Source here.
type TrafficSource interface {
	Traffic(ctx context.Context) ([]WireTraffic, error)
}

// LinkSource supplies deep links for a component and an optional trace id
// (Task 7).
type LinkSource interface {
	Links(componentID, traceID string) []Link
}

type noTraffic struct{}

func (noTraffic) Traffic(context.Context) ([]WireTraffic, error) { return nil, nil }

type noLinks struct{}

func (noLinks) Links(string, string) []Link { return nil }

// Options configures New.
type Options struct {
	Store       *store.Store
	Topology    topology.Topology
	Broadcaster *Broadcaster
	Traffic     TrafficSource // nil = empty
	Links       LinkSource    // nil = none
	Log         *slog.Logger
	// RatePerSec caps events per second per SSE client (default 200).
	RatePerSec int
	// Window is the rate window (default 1s).
	Window time.Duration
	// KeepAlive is the SSE comment interval (default 15s).
	KeepAlive time.Duration
}

// API holds the handlers' dependencies.
type API struct {
	st        *store.Store
	topo      topology.Topology
	bc        *Broadcaster
	traffic   TrafficSource
	links     LinkSource
	log       *slog.Logger
	rate      int
	window    time.Duration
	keepAlive time.Duration
}

// New returns the handler for everything under /api, wrapped in AuthSeam and
// the CSP header.
func New(o Options) http.Handler {
	a := &API{st: o.Store, topo: o.Topology, bc: o.Broadcaster, traffic: o.Traffic, links: o.Links, log: o.Log,
		rate: o.RatePerSec, window: o.Window, keepAlive: o.KeepAlive}
	if a.bc == nil {
		a.bc = NewBroadcaster()
	}
	if a.traffic == nil {
		a.traffic = noTraffic{}
	}
	if a.links == nil {
		a.links = noLinks{}
	}
	if a.log == nil {
		a.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if a.rate <= 0 {
		a.rate = defaultRate
	}
	if a.window <= 0 {
		a.window = defaultWindow
	}
	if a.keepAlive <= 0 {
		a.keepAlive = defaultKeepAlive
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/topology", a.getTopology)
	mux.HandleFunc("GET /api/orders", a.searchOrders)
	mux.HandleFunc("GET /api/orders/{delivery_id}", a.getOrder)
	mux.HandleFunc("GET /api/orders/{delivery_id}/timeline", a.getTimeline)
	mux.HandleFunc("GET /api/components/{id}", a.getComponent)
	mux.HandleFunc("GET /api/components/{id}/logs", a.getComponentLogs)
	mux.HandleFunc("GET /api/events/{id}", a.getEvent)
	mux.HandleFunc("GET /api/system", a.getSystem)
	mux.HandleFunc("GET /api/self", a.getSelf)
	mux.HandleFunc("GET /api/stream", a.stream)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "not found")
	})
	return CSP(AuthSeam(mux))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// fail logs an internal error and answers 500 without its text.
func (a *API) fail(w http.ResponseWriter, op string, err error) {
	a.log.Error("api: "+op+" failed", "error", err.Error())
	writeErr(w, http.StatusInternalServerError, "internal error")
}
