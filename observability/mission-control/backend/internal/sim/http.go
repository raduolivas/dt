package sim

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/api"
)

// Mount registers the /api/sim routes. A nil manager (simulation off) registers nothing, so the paths fall through
// to the API's plain 404.
//
//	GET  /api/sim/status               {enabled, runs:[{delivery_id, order_ref, scenario}]}
//	POST /api/sim/runs[?pickup=hold]   -> 201 {delivery_id, order_ref, tracking_url}; hold = no pickup until scenario 4 is healed
//	POST /api/sim/runs/{id}/break      {scenario: 1..4} -> 204
//	POST /api/sim/runs/{id}/heal       -> 204
func Mount(mux *http.ServeMux, m *Manager) {
	if m == nil {
		return
	}
	h := &handlers{m: m}
	mux.Handle("GET /api/sim/status", api.AuthSeam(http.HandlerFunc(h.status)))
	mux.Handle("POST /api/sim/runs", api.AuthSeam(h.sameOrigin(h.start)))
	mux.Handle("POST /api/sim/runs/{id}/break", api.AuthSeam(h.sameOrigin(h.breakRun)))
	mux.Handle("POST /api/sim/runs/{id}/heal", api.AuthSeam(h.sameOrigin(h.healRun)))
}

type handlers struct{ m *Manager }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// sameOrigin refuses a browser request from another origin: the routes write to DT, and there is no login.
func (h *handlers) sameOrigin(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" {
			u, err := url.Parse(o)
			if err != nil || u.Host != r.Host {
				writeErr(w, http.StatusForbidden, "cross-origin request refused")
				return
			}
		}
		next(w, r)
	})
}

func (h *handlers) status(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "runs": h.m.Status()})
}

func (h *handlers) start(w http.ResponseWriter, r *http.Request) {
	start := h.m.Start
	if r.URL.Query().Get("pickup") == "hold" {
		start = h.m.StartHeld
	}
	run, err := start(r.Context())
	if err != nil {
		code := http.StatusBadGateway
		if errors.Is(err, ErrTooManyRuns) {
			code = http.StatusTooManyRequests
		}
		h.m.log.Error("sim: start failed", "error", err.Error())
		writeErr(w, code, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, run)
}

func (h *handlers) breakRun(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Scenario int `json:"scenario"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be {\"scenario\": 1..4}")
		return
	}
	h.finish(w, h.m.Break(r.PathValue("id"), in.Scenario))
}

func (h *handlers) healRun(w http.ResponseWriter, r *http.Request) {
	h.finish(w, h.m.Heal(r.PathValue("id")))
}

func (h *handlers) finish(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ErrUnknownRun):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrTooLate):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrBadScenario):
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		h.m.log.Error("sim: scenario change failed", "error", err.Error())
		writeErr(w, http.StatusBadGateway, err.Error())
	}
}
