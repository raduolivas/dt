// Package httpapi serves the customer viewer endpoints on tracking's public
// port (MVP spec §5.4; M3 design §3.2): the snapshot, the WebSocket stream
// and the SSE stream. A viewer token (Ed25519 JWT from the gateway) is the
// only credential; it opens the stream of its own delivery and nothing else.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/raduolivas/dt/backend/pkg/viewerjwt"
	"github.com/raduolivas/dt/backend/services/tracking/internal/app"
	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

// Defaults (MVP spec §5.4, contract).
const (
	// DefaultMaxPerLink is the most streams one tracking link may hold on
	// an instance.
	DefaultMaxPerLink = 5
	// DefaultMaxStreams is the most streams (authenticated or not) an
	// instance holds.
	DefaultMaxStreams = 10_000
	// DefaultPingInterval is how often a stream gets {type:"ping"}.
	DefaultPingInterval = 25 * time.Second
	// DefaultAuthTimeout is how long a WebSocket may wait before its first
	// message, which must be auth.
	DefaultAuthTimeout = 5 * time.Second
	// DefaultSwapInterval is the least time between two token swaps on a
	// stream (each one verifies a token and reads Redis).
	DefaultSwapInterval = time.Second
	// queueSize is a stream's backlog; past it the oldest snapshot is
	// dropped (the client only needs the newest).
	queueSize = 8
	// shutdownWait bounds how long shutdown waits for streams to close.
	shutdownWait = 5 * time.Second
	// checkTimeout bounds the Redis reads of one authentication.
	checkTimeout = 3 * time.Second
)

// Verifier checks viewer tokens (*viewerjwt.Verifier).
type Verifier interface {
	Verify(ctx context.Context, token string, now time.Time) (viewerjwt.Claims, error)
}

// Snapshots reads a delivery's viewer snapshot (*app.Service).
type Snapshots interface {
	ViewerSnapshot(ctx context.Context, sc app.Scope, deliveryID string) (domain.TrackingSnapshot, error)
}

// Deps configures the hub.
type Deps struct {
	Verifier  Verifier
	Snapshots Snapshots
	Links     app.ViewerLinks
	Feed      app.ViewerFeed
	// Log receives failures; never a token. Nil discards.
	Log *slog.Logger
	// Zero values take the defaults above.
	MaxPerLink, MaxStreams                  int
	PingInterval, AuthTimeout, SwapInterval time.Duration
}

// Hub holds the instance's open streams: it fans snapshots out to them,
// closes them on revocation, expiry and shutdown, and enforces the caps.
type Hub struct {
	d   Deps
	log *slog.Logger

	mu         sync.Mutex
	total      int // streams holding an instance slot
	closing    bool
	byLink     map[string]map[*stream]struct{}
	byDelivery map[string]map[*stream]struct{}
	handlers   sync.WaitGroup
}

var _ app.FeedHandler = (*Hub)(nil)

// NewHub returns a hub; call Mount, then Run.
func NewHub(d Deps) *Hub {
	if d.MaxPerLink <= 0 {
		d.MaxPerLink = DefaultMaxPerLink
	}
	if d.MaxStreams <= 0 {
		d.MaxStreams = DefaultMaxStreams
	}
	if d.PingInterval <= 0 {
		d.PingInterval = DefaultPingInterval
	}
	if d.AuthTimeout <= 0 {
		d.AuthTimeout = DefaultAuthTimeout
	}
	if d.SwapInterval <= 0 {
		d.SwapInterval = DefaultSwapInterval
	}
	log := d.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Hub{d: d, log: log, byLink: map[string]map[*stream]struct{}{}, byDelivery: map[string]map[*stream]struct{}{}}
}

// Mount registers the viewer routes on mux:
//
//	GET /v1/tracking/snapshot     bearer viewer token → TrackingSnapshot
//	GET /v1/tracking/stream       WebSocket, first message {type:"auth",token}
//	GET /v1/tracking/stream/sse   bearer viewer token → text/event-stream
//	OPTIONS on each               CORS preflight (204)
func (h *Hub) Mount(mux *http.ServeMux) {
	mux.Handle("GET /v1/tracking/snapshot", viewerCORS(http.HandlerFunc(h.snapshot)))
	mux.Handle("GET /v1/tracking/stream", viewerCORS(http.HandlerFunc(h.websocket)))
	mux.Handle("GET /v1/tracking/stream/sse", viewerCORS(http.HandlerFunc(h.sse)))
	for _, p := range []string{"/v1/tracking/snapshot", "/v1/tracking/stream", "/v1/tracking/stream/sse"} {
		mux.Handle("OPTIONS "+p, http.HandlerFunc(viewerPreflight))
	}
}

// Run receives the feed until ctx ends, then closes every stream with a
// reconnect hint and waits (bounded) for them to finish.
func (h *Hub) Run(ctx context.Context) error {
	err := h.d.Feed.Run(ctx, h)
	h.shutdown()
	return err
}

// Streams returns the number of streams holding an instance slot.
func (h *Hub) Streams() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.total
}

func (h *Hub) shutdown() {
	h.mu.Lock()
	h.closing = true
	var all []*stream
	for _, set := range h.byDelivery {
		for s := range set {
			all = append(all, s)
		}
	}
	h.mu.Unlock()
	for _, s := range all {
		s.end(endShutdown)
	}
	done := make(chan struct{})
	go func() { h.handlers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(shutdownWait):
		h.log.Warn("viewer_streams_shutdown_timeout")
	}
}

var (
	errShutdown = errors.New("shutting down")
	errFull     = errors.New("too many streams")
	errLinkFull = errors.New("too many streams for the link")
)

// acquire takes an instance slot for a connection, before it authenticates.
// The returned release is idempotent.
func (h *Hub) acquire() (func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing {
		return nil, errShutdown
	}
	if h.total >= h.d.MaxStreams {
		return nil, errFull
	}
	h.total++
	h.handlers.Add(1)
	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			h.total--
			h.mu.Unlock()
			h.handlers.Done()
		})
	}, nil
}

// join registers s under its link and delivery (the link cap applies), then
// subscribes to the delivery's snapshots. Registering first means a
// revocation or expiry announced from here on reaches s; the checks in open
// cover what was announced before. The caller must call leave.
func (h *Hub) join(ctx context.Context, s *stream) error {
	c := s.claims
	h.mu.Lock()
	if h.closing {
		h.mu.Unlock()
		return errShutdown
	}
	if len(h.byLink[c.LinkID]) >= h.d.MaxPerLink {
		h.mu.Unlock()
		return errLinkFull
	}
	add(h.byLink, c.LinkID, s)
	add(h.byDelivery, c.DeliveryID, s)
	s.joined = true
	h.mu.Unlock()
	if err := h.d.Feed.Subscribe(ctx, c.DeliveryID); err != nil {
		return err
	}
	s.subscribed = true
	return nil
}

// leave undoes join; it is safe to call more than once and without join.
func (h *Hub) leave(s *stream) {
	s.stopTimers()
	h.mu.Lock()
	if s.joined {
		s.joined = false
		remove(h.byLink, s.claims.LinkID, s)
		remove(h.byDelivery, s.claims.DeliveryID, s)
	}
	h.mu.Unlock()
	if s.subscribed {
		s.subscribed = false
		h.d.Feed.Unsubscribe(context.Background(), s.claims.DeliveryID)
	}
}

func add(m map[string]map[*stream]struct{}, k string, s *stream) {
	set := m[k]
	if set == nil {
		set = map[*stream]struct{}{}
		m[k] = set
	}
	set[s] = struct{}{}
}

func remove(m map[string]map[*stream]struct{}, k string, s *stream) {
	if set := m[k]; set != nil {
		delete(set, s)
		if len(set) == 0 {
			delete(m, k)
		}
	}
}

func (h *Hub) streamsOf(m map[string]map[*stream]struct{}, k string) []*stream {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*stream, 0, len(m[k]))
	for s := range m[k] {
		out = append(out, s)
	}
	return out
}

// open runs the checks of a newly joined stream: the link's deny-list and
// expiry, then the current snapshot. A failure ends s.
func (h *Hub) open(ctx context.Context, s *stream) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	if !h.checkLink(ctx, s) {
		return
	}
	h.refresh(ctx, s)
}

// checkLink ends s when its link is revoked or expired, and schedules the
// end at a known expiry. It reports whether s may go on.
func (h *Hub) checkLink(ctx context.Context, s *stream) bool {
	lid := s.claims.LinkID
	revoked, err := h.d.Links.Revoked(ctx, lid)
	if err != nil {
		h.log.WarnContext(ctx, "viewer_link_check_failed", "error", err)
		s.end(endUnavailable) // fail closed
		return false
	}
	if revoked {
		s.end(endRevoked)
		return false
	}
	at, ok, err := h.d.Links.Expiry(ctx, lid)
	if err != nil {
		h.log.WarnContext(ctx, "viewer_link_check_failed", "error", err)
		s.end(endUnavailable)
		return false
	}
	if ok {
		s.expireAt(at)
	}
	return true
}

// refresh reads s's delivery snapshot and offers it to s.
func (h *Hub) refresh(ctx context.Context, s *stream) {
	snap, err := h.d.Snapshots.ViewerSnapshot(ctx, scopeOf(s.claims), s.claims.DeliveryID)
	switch {
	case errors.Is(err, app.ErrNotFound):
		s.end(endUnauthorized) // a token for a delivery outside its scope
		return
	case err != nil:
		h.log.WarnContext(ctx, "viewer_snapshot_failed", "delivery_id", s.claims.DeliveryID, "error", err)
		s.end(endUnavailable)
		return
	}
	m, err := encodeSnapshot(snap)
	if err != nil {
		s.end(endUnavailable)
		return
	}
	s.offer(m)
}

func scopeOf(c viewerjwt.Claims) app.Scope { return app.Scope{TenantID: c.TenantID, Env: c.Env} }

// Snapshot implements app.FeedHandler: it fans a published snapshot out to
// the delivery's local streams. It never blocks.
func (h *Hub) Snapshot(deliveryID string, data []byte) {
	streams := h.streamsOf(h.byDelivery, deliveryID)
	if len(streams) == 0 {
		return
	}
	var snap domain.TrackingSnapshot
	if err := json.Unmarshal(data, &snap); err != nil || snap.DeliveryID != deliveryID {
		h.log.Warn("viewer_snapshot_malformed", "delivery_id", deliveryID)
		return
	}
	m, err := encodeSnapshot(snap)
	if err != nil {
		return
	}
	for _, s := range streams {
		s.offer(m)
	}
}

// Revoked implements app.FeedHandler: the link's streams get revoked and
// close.
func (h *Hub) Revoked(linkID string) {
	for _, s := range h.streamsOf(h.byLink, linkID) {
		s.end(endRevoked)
	}
}

// Expiry implements app.FeedHandler: the link's streams close at expiresAt.
func (h *Hub) Expiry(linkID string, expiresAt time.Time) {
	for _, s := range h.streamsOf(h.byLink, linkID) {
		s.expireAt(expiresAt)
	}
}

// Resync implements app.FeedHandler: after the feed reconnected, every
// stream re-checks its link and re-reads its snapshot, in the background.
func (h *Hub) Resync() {
	h.mu.Lock()
	var all []*stream
	for _, set := range h.byDelivery {
		for s := range set {
			all = append(all, s)
		}
	}
	h.mu.Unlock()
	go func() {
		for _, s := range all {
			select {
			case <-s.done:
				continue
			default:
			}
			h.open(context.Background(), s)
		}
	}()
}

// encodeSnapshot builds {type:"snapshot",data} (and the status it shows).
// As a last guard it sanitizes the snapshot, whatever was published (MVP
// spec §4.5).
func encodeSnapshot(snap domain.TrackingSnapshot) (outMsg, error) {
	snap = sanitize(snap)
	data, err := json.Marshal(snap)
	if err != nil {
		return outMsg{}, err
	}
	b, err := json.Marshal(wire{Type: "snapshot", Data: data})
	if err != nil {
		return outMsg{}, err
	}
	var at time.Time
	for _, e := range snap.Timeline {
		if e.Status == snap.Status {
			at = e.At
		}
	}
	return outMsg{typ: "snapshot", body: b, version: snap.Version, status: snap.Status, statusAt: at}, nil
}

// sanitize drops courier data the delivery's model and status do not
// allow: the courier unless visible, the route to the pickup outside
// End-to-end before pickup.
func sanitize(snap domain.TrackingSnapshot) domain.TrackingSnapshot {
	if !domain.CourierVisible(snap.DeliveryModel, snap.Status) {
		snap.Courier = nil
		snap.Route.ToPickup = nil
	}
	if snap.DeliveryModel != domain.ModelEndToEnd || !snap.Status.BeforePickup() {
		snap.Route.ToPickup = nil
	}
	return snap
}

// wire is a server message.
type wire struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

var (
	pingBody    = []byte(`{"type":"ping"}`)
	revokedBody = []byte(`{"type":"revoked"}`)
)

func statusBody(st domain.Status, at time.Time) []byte {
	data := struct {
		Status domain.Status `json:"status"`
		At     *time.Time    `json:"at,omitempty"`
	}{Status: st}
	if !at.IsZero() {
		data.At = &at
	}
	d, _ := json.Marshal(data)
	b, _ := json.Marshal(wire{Type: "status", Data: d})
	return b
}
