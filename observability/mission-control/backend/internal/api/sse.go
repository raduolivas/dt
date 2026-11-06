package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
)

const (
	defaultRate      = 200
	defaultWindow    = time.Second
	defaultKeepAlive = 15 * time.Second
	subBuffer        = 1024
	replayPage       = store.MaxTimelineLimit
)

// Sub is one SSE client's feed from the Broadcaster.
type Sub struct {
	C chan store.Event
	// Overflow is signalled (never blocks) when C was full and events were
	// dropped for this client; it then re-reads from the store.
	Overflow chan struct{}
}

// Broadcaster fans newly inserted events out to SSE clients. OnEvent is the
// ingest OnInserted hook: it never blocks. A client that cannot keep up loses
// events from its buffer and catches up from the store (the same path as
// Last-Event-ID resume), so nothing is lost, only delayed.
type Broadcaster struct {
	mu   sync.RWMutex
	subs map[*Sub]struct{}
}

// NewBroadcaster returns an empty Broadcaster.
func NewBroadcaster() *Broadcaster { return &Broadcaster{subs: map[*Sub]struct{}{}} }

// Subscribe registers a client.
func (b *Broadcaster) Subscribe() *Sub {
	s := &Sub{C: make(chan store.Event, subBuffer), Overflow: make(chan struct{}, 1)}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

// Unsubscribe removes a client.
func (b *Broadcaster) Unsubscribe(s *Sub) {
	b.mu.Lock()
	delete(b.subs, s)
	b.mu.Unlock()
}

// OnEvent delivers a stored event (ID set) to every client without blocking.
func (b *Broadcaster) OnEvent(_ context.Context, e store.Event) error {
	if e.ID == 0 {
		return nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		select {
		case s.C <- e:
		default:
			select {
			case s.Overflow <- struct{}{}:
			default:
			}
		}
	}
	return nil
}

type bundleKey struct{ wire, outcome string }

type bundle struct {
	count  int
	lastID int64
}

// client is the state of one SSE connection.
type client struct {
	w        http.ResponseWriter
	fl       http.Flusher
	st       *store.Store
	delivery string
	traces   map[string]bool // trace ids seen for the delivery (spans carry no delivery id)
	rate     int

	last    int64 // highest event id handled; -1 = none yet
	count   int   // live events sent in this window
	bundles map[bundleKey]*bundle
	order   []bundleKey
}

func (c *client) write(id int64, event string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(c.w, "id: %d\nevent: %s\ndata: %s\n\n", id, event, b); err != nil {
		return err
	}
	c.fl.Flush()
	return nil
}

func (c *client) match(e store.Event) bool {
	if c.delivery == "" {
		return true
	}
	if e.DeliveryID == c.delivery {
		if e.TraceID != "" {
			c.traces[e.TraceID] = true
		}
		return true
	}
	return e.TraceID != "" && c.traces[e.TraceID]
}

func wireOf(e store.Event) string {
	if e.Target == "" {
		return e.Source
	}
	return e.Source + ">" + e.Target
}

// deliver sends e unless it was handled already or is filtered out. Live
// events past the per-window rate are bundled; replayed history never is.
func (c *client) deliver(e store.Event, live bool) error {
	if e.ID <= c.last {
		return nil
	}
	c.last = e.ID
	if !c.match(e) {
		return nil
	}
	if live && c.count >= c.rate {
		k := bundleKey{wireOf(e), e.Outcome}
		b := c.bundles[k]
		if b == nil {
			b = &bundle{}
			c.bundles[k] = b
			c.order = append(c.order, k)
		}
		b.count++
		b.lastID = e.ID
		return nil
	}
	if live {
		c.count++
	}
	name := "event"
	if e.Kind == "detector" {
		name = "detector"
	}
	return c.write(e.ID, name, toEventDTO(e))
}

// endWindow resets the rate counter and emits the pending bundles.
func (c *client) endWindow() error {
	c.count = 0
	for _, k := range c.order {
		b := c.bundles[k]
		if err := c.write(b.lastID, "bundle", map[string]any{"wire": k.wire, "count": b.count, "outcome": k.outcome}); err != nil {
			return err
		}
	}
	c.bundles = map[bundleKey]*bundle{}
	c.order = nil
	return nil
}

// replay sends stored events after c.last, oldest first.
func (c *client) replay(ctx context.Context) error {
	if c.st == nil {
		return nil
	}
	after := c.last
	for {
		var evs []store.Event
		var next int64
		var err error
		if c.delivery != "" {
			evs, next, err = c.st.Timeline(ctx, c.delivery, after, replayPage)
		} else {
			evs, err = c.st.EventsAfter(ctx, after, replayPage)
			if len(evs) == replayPage {
				next = evs[len(evs)-1].ID
			}
		}
		if err != nil {
			return err
		}
		for _, e := range evs {
			if err := c.deliver(e, false); err != nil {
				return err
			}
		}
		if next == 0 {
			return nil
		}
		after = next
	}
}

// resume handles Last-Event-ID: a gap marker when the id is older than what
// is stored, then the replay.
func (c *client) resume(ctx context.Context, last int64) error {
	c.last = last
	if c.st == nil {
		return nil
	}
	min, err := c.st.MinEventID(ctx)
	if err != nil {
		return err
	}
	if min > 0 && last+1 < min {
		if err := c.write(min-1, "gap", map[string]int64{"from": last + 1, "to": min - 1}); err != nil {
			return err
		}
		c.last = min - 1
	}
	return c.replay(ctx)
}

func (a *API) stream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	ctx := r.Context()
	c := &client{w: w, fl: fl, st: a.st, delivery: r.URL.Query().Get("delivery_id"), traces: map[string]bool{},
		rate: a.rate, last: -1, bundles: map[bundleKey]*bundle{}}
	sub := a.bc.Subscribe() // before the replay, so no event falls between replay and live
	defer a.bc.Unsubscribe(sub)

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, ": connected\n\n")
	fl.Flush()

	if c.delivery != "" && c.st != nil {
		evs, _, err := c.st.Timeline(ctx, c.delivery, 0, replayPage)
		if err != nil {
			a.log.Warn("sse: seed traces failed", "error", err.Error())
		}
		for _, e := range evs {
			if e.TraceID != "" {
				c.traces[e.TraceID] = true
			}
		}
	}
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && id >= 0 {
			if err := c.resume(ctx, id); err != nil {
				a.log.Warn("sse: resume failed", "error", err.Error())
				return
			}
		}
	}

	keep := time.NewTicker(a.keepAlive)
	defer keep.Stop()
	win := time.NewTicker(a.window)
	defer win.Stop()
	for {
		var err error
		select {
		case <-ctx.Done():
			return
		case e := <-sub.C:
			if c.last < 0 {
				c.last = e.ID - 1
			}
			err = c.deliver(e, true)
		case <-sub.Overflow:
			for len(sub.C) > 0 { // everything dropped or queued is in the store
				<-sub.C
			}
			if c.last >= 0 {
				err = c.replay(ctx)
			}
		case <-win.C:
			err = c.endWindow()
		case <-keep.C:
			_, err = fmt.Fprint(w, ": keep-alive\n\n")
			fl.Flush()
		}
		if err != nil {
			return
		}
	}
}
