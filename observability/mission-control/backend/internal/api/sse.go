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
	replayPage       = store.MaxTimelineLimit
	// writeTimeout bounds one SSE write, so a stalled client releases its
	// goroutine and subscription.
	writeTimeout = 10 * time.Second
)

// Sub is one SSE client's wake-up channel.
type Sub struct{ Wake chan struct{} }

// Broadcaster tells SSE clients that new events were stored. OnEvent is the
// ingest OnInserted hook: it never blocks. It carries no payload on purpose:
// writers call the hook after their own commit, so two writers can announce
// out of order. Clients therefore read what is new from the store
// (id > high-water mark); InsertEvents serialises inserts so id order equals
// commit order and that read never leaves a hole (R24). A slow client just
// wakes later and reads a bigger page.
type Broadcaster struct {
	mu   sync.RWMutex
	subs map[*Sub]struct{}
}

// NewBroadcaster returns an empty Broadcaster.
func NewBroadcaster() *Broadcaster { return &Broadcaster{subs: map[*Sub]struct{}{}} }

// Subscribe registers a client.
func (b *Broadcaster) Subscribe() *Sub {
	s := &Sub{Wake: make(chan struct{}, 1)}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

// Len is the number of connected clients.
func (b *Broadcaster) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

// Unsubscribe removes a client.
func (b *Broadcaster) Unsubscribe(s *Sub) {
	b.mu.Lock()
	delete(b.subs, s)
	b.mu.Unlock()
}

// OnEvent wakes every client without blocking.
func (b *Broadcaster) OnEvent(_ context.Context, _ store.Event) error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		select {
		case s.Wake <- struct{}{}:
		default:
		}
	}
	return nil
}

type bundleKey struct{ wire, outcome string }

// client is the state of one SSE connection.
type client struct {
	w        http.ResponseWriter
	fl       http.Flusher
	rc       *http.ResponseController
	st       *store.Store
	delivery string
	rate     int

	last    int64 // high-water mark: highest event id handled
	count   int   // live events sent in this window
	bundles map[bundleKey]int
	order   []bundleKey
}

func (c *client) deadline() { _ = c.rc.SetWriteDeadline(time.Now().Add(writeTimeout)) }

func (c *client) write(id int64, event string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.deadline()
	if _, err := fmt.Fprintf(c.w, "id: %d\nevent: %s\ndata: %s\n\n", id, event, b); err != nil {
		return err
	}
	c.fl.Flush()
	return nil
}

func wireOf(e store.Event) string {
	if e.Target == "" {
		return e.Source
	}
	return e.Source + ">" + e.Target
}

// deliver sends e unless it was handled already. Rows come from the store
// already filtered (a delivery stream reads Timeline: the delivery's rows plus
// its trace siblings), so there is no second filter here: one that learned
// trace ids as it went would drop a sibling stored before the row carrying its
// trace. Live
// events past the per-window rate are bundled, except detector findings and
// health changes, which always go out individually (R25). Replayed history is
// never bundled.
func (c *client) deliver(e store.Event, live bool) error {
	if e.ID <= c.last {
		return nil
	}
	c.last = e.ID
	important := e.Kind == "detector" || e.Kind == "health"
	if live && !important {
		if c.count >= c.rate {
			k := bundleKey{wireOf(e), e.Outcome}
			if _, ok := c.bundles[k]; !ok {
				c.order = append(c.order, k)
			}
			c.bundles[k]++
			return nil
		}
		c.count++
	}
	name := "event"
	if e.Kind == "detector" {
		name = "detector"
	}
	return c.write(e.ID, name, toEventDTO(e))
}

// endWindow resets the rate counter and emits the pending bundles. They carry
// the high-water mark as id, so Last-Event-ID never moves backwards.
func (c *client) endWindow() error {
	c.count = 0
	for _, k := range c.order {
		if err := c.write(c.last, "bundle", map[string]any{"wire": k.wire, "count": c.bundles[k], "outcome": k.outcome}); err != nil {
			return err
		}
	}
	c.bundles = map[bundleKey]int{}
	c.order = nil
	return nil
}

// catchUp sends stored events after the high-water mark, oldest first.
func (c *client) catchUp(ctx context.Context, live bool) error {
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
			if err := c.deliver(e, live); err != nil {
				return err
			}
		}
		if next == 0 {
			return nil
		}
		after = next
	}
}

// resume handles Last-Event-ID: a reset when the id is ahead of everything
// stored (the database was recreated and ids restarted, I1), a gap marker
// when it is older than what is stored, then the replay.
func (c *client) resume(ctx context.Context, last int64) error {
	c.last = last
	if c.st == nil {
		return nil
	}
	max, err := c.st.MaxEventID(ctx)
	if err != nil {
		return err
	}
	if last > max {
		// Without this every new id (<= last) would be dropped silently. The
		// client clears what it holds and refetches; the stream goes on live.
		if err := c.write(max, "reset", map[string]int64{"max": max}); err != nil {
			return err
		}
		c.last = max
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
	return c.catchUp(ctx, false)
}

func (a *API) stream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	ctx := r.Context()
	c := &client{w: w, fl: fl, rc: http.NewResponseController(w), st: a.st, delivery: r.URL.Query().Get("delivery_id"),
		rate: a.rate, bundles: map[bundleKey]int{}}
	sub := a.bc.Subscribe() // before reading the start point, so no wake is missed
	defer a.bc.Unsubscribe(sub)

	resumed := false
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && id >= 0 {
			resumed = true
			c.last = id
		}
	}
	if c.st != nil {
		if !resumed { // live only: start at the newest stored event
			max, err := c.st.MaxEventID(ctx)
			if err != nil {
				a.log.Warn("sse: start point failed", "error", err.Error())
				writeErr(w, http.StatusInternalServerError, "internal error")
				return
			}
			c.last = max
		}
	}
	// Headers go out only after the start point is fixed: a client that sees
	// the response can rely on receiving everything stored after it.
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	c.deadline()
	_, _ = fmt.Fprint(w, ": connected\n\n")
	fl.Flush()

	if resumed && c.st != nil {
		if err := c.resume(ctx, c.last); err != nil {
			a.log.Warn("sse: resume failed", "error", err.Error())
			return
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
		case <-sub.Wake:
			err = c.catchUp(ctx, true)
		case <-win.C:
			err = c.endWindow()
		case <-keep.C:
			c.deadline()
			_, err = fmt.Fprint(w, ": keep-alive\n\n")
			fl.Flush()
		}
		if err != nil {
			if ctx.Err() == nil {
				a.log.Warn("sse: client stream ended", "error", err.Error())
			}
			return
		}
	}
}
