package sim

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testKey      = "dt_sk_test_" + "0123456789abcdefghijklmnopqrstuvwxyz0123456789abcdefg"
	courierToken = "courier-access-token-1"
)

type locReq struct {
	auth  string
	fixes int
	at    time.Time
}

// gwSnap is a copy of what the fake gateway has seen.
type gwSnap struct {
	partnerAuths []string // Authorization of every partner call
	courierAuths []string // Authorization of every courier call
	couriers     []string // PUT /v1/couriers/{external_id}
	assignments  []string // courier ids
	events       []string // event types
	viewerOpened int
	viewerAuth   string
}

// fakeGateway stands in for DT's gateway and edge: the partner API, the courier API and the viewer API.
type fakeGateway struct {
	t   *testing.T
	srv *httptest.Server

	mu         sync.Mutex
	locs       []locReq
	s          gwSnap
	deliveries int
}

func newFakeGateway(t *testing.T) *fakeGateway {
	f := &fakeGateway{t: t}
	mux := http.NewServeMux()
	partner := func(r *http.Request) {
		// DT's partner API rejects a write without an Idempotency-Key (400)
		if r.Method != http.MethodGet && r.Header.Get("Idempotency-Key") == "" {
			f.mu.Lock()
			f.s.partnerAuths = append(f.s.partnerAuths, "MISSING-IDEMPOTENCY-KEY")
			f.mu.Unlock()
			return
		}
		f.mu.Lock()
		f.s.partnerAuths = append(f.s.partnerAuths, r.Header.Get("Authorization"))
		f.mu.Unlock()
	}
	reply := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("PUT /v1/couriers/{ext}", func(w http.ResponseWriter, r *http.Request) {
		partner(r)
		f.mu.Lock()
		f.s.couriers = append(f.s.couriers, r.PathValue("ext"))
		f.mu.Unlock()
		reply(w, 201, map[string]string{"id": "cou_" + r.PathValue("ext")})
	})
	mux.HandleFunc("POST /v1/deliveries", func(w http.ResponseWriter, r *http.Request) {
		partner(r)
		var in struct {
			ExternalID string `json:"external_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		f.deliveries++
		n := f.deliveries
		f.mu.Unlock()
		reply(w, 201, map[string]any{"id": "del_" + string(rune('a'+n)), "external_id": in.ExternalID,
			"tracking": map[string]string{"url": f.srv.URL + "/t/TRACKTOK"}})
	})
	mux.HandleFunc("POST /v1/deliveries/{id}/assignment", func(w http.ResponseWriter, r *http.Request) {
		partner(r)
		var in struct {
			CourierID string `json:"courier_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		f.s.assignments = append(f.s.assignments, in.CourierID)
		f.mu.Unlock()
		reply(w, 200, map[string]string{"id": r.PathValue("id")})
	})
	mux.HandleFunc("POST /v1/courier-sessions", func(w http.ResponseWriter, r *http.Request) {
		partner(r)
		reply(w, 201, map[string]any{"session": map[string]string{"id": "cs_1"}, "link_url": "http://courier.localhost/s/LINKCODE"})
	})
	mux.HandleFunc("POST /v1/courier/token", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			GrantType string `json:"grant_type"`
			Code      string `json:"code"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.GrantType == "link_code" && in.Code != "LINKCODE" {
			reply(w, 401, map[string]string{"code": "link_invalid"})
			return
		}
		reply(w, 200, map[string]any{"access_token": courierToken, "expires_in": 900, "refresh_token": "refresh-token-1"})
	})
	mux.HandleFunc("POST /v1/courier/locations", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Fixes []json.RawMessage `json:"fixes"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		auth := r.Header.Get("Authorization")
		f.mu.Lock()
		f.s.courierAuths = append(f.s.courierAuths, auth)
		f.locs = append(f.locs, locReq{auth: auth, fixes: len(in.Fixes), at: time.Now()})
		f.mu.Unlock()
		if auth != "Bearer "+courierToken {
			reply(w, 401, map[string]string{"code": "unauthorized"})
			return
		}
		reply(w, 200, map[string]int{"accepted": len(in.Fixes), "dropped": 0})
	})
	mux.HandleFunc("POST /v1/courier/deliveries/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Type string `json:"type"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		f.s.courierAuths = append(f.s.courierAuths, r.Header.Get("Authorization"))
		f.s.events = append(f.s.events, in.Type)
		f.mu.Unlock()
		reply(w, 200, map[string]string{"outcome": "applied"})
	})
	mux.HandleFunc("POST /v1/tracking/session", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), "TRACKTOK") {
			reply(w, 401, map[string]string{"code": "link_invalid"})
			return
		}
		reply(w, 200, map[string]any{"viewer_token": "viewer-token-1", "expires_at": time.Now().Add(time.Hour)})
	})
	mux.HandleFunc("GET /v1/tracking/stream/sse", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.s.viewerOpened++
		f.s.viewerAuth = r.Header.Get("Authorization")
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("event: snapshot\ndata: {}\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGateway) locCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.locs)
}

func (f *fakeGateway) locsSince(n int) []locReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]locReq(nil), f.locs[n:]...)
}

func (f *fakeGateway) snapshot() gwSnap {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.s
	s.partnerAuths = append([]string(nil), s.partnerAuths...)
	s.courierAuths = append([]string(nil), s.courierAuths...)
	s.couriers = append([]string(nil), s.couriers...)
	s.assignments = append([]string(nil), s.assignments...)
	s.events = append([]string(nil), s.events...)
	return s
}
