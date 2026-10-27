package redis

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/raduolivas/dt/backend/pkg/viewerjwt"
	"github.com/raduolivas/dt/backend/services/tracking/internal/app"
)

// Feed timing.
const (
	// feedHeartbeat is how often the feed pings its connection.
	feedHeartbeat = 10 * time.Second
	// feedReceiveTimeout ends a receive that saw nothing (not even a
	// heartbeat reply) for this long: the connection is presumed dead and
	// replaced.
	feedReceiveTimeout = 30 * time.Second
	// subscribeTimeout bounds the wait for a subscription to be active.
	subscribeTimeout = 3 * time.Second
	feedBackoffMin   = 200 * time.Millisecond
	feedBackoffMax   = 5 * time.Second
	heartbeatPayload = "hb"
)

var errFeedReset = errors.New("viewer feed: connection reset")

// Feed is the app.ViewerFeed on one Redis Pub/Sub connection: the link
// revocation and expiry channels, plus dt:snap:<del_id> for each delivery
// with at least one local stream (reference counted). On any receive error
// the connection is replaced (subscribing every channel again) and the
// handler is told to resync.
type Feed struct {
	rdb *goredis.Client
	log *slog.Logger

	mu      sync.Mutex
	ps      *goredis.PubSub
	refs    map[string]int
	waiters map[string]chan error
	seq     uint64
}

var _ app.ViewerFeed = (*Feed)(nil)

// NewFeed returns a feed over rdb; log may be nil.
func NewFeed(rdb *goredis.Client, log *slog.Logger) *Feed {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Feed{rdb: rdb, log: log, refs: map[string]int{}, waiters: map[string]chan error{}}
}

// pubsubLocked returns the connection, opening one subscribed to every
// channel in use when there is none.
func (f *Feed) pubsubLocked(ctx context.Context) *goredis.PubSub {
	if f.ps == nil {
		chans := []string{viewerjwt.RevocationsChannel, viewerjwt.ExpiriesChannel}
		for id := range f.refs {
			chans = append(chans, snapChannel(id))
		}
		// A failed first SUBSCRIBE is retried by the next receive, which
		// reconnects and subscribes every channel recorded here.
		f.ps = f.rdb.Subscribe(ctx, chans...)
	}
	return f.ps
}

// resetLocked drops ps (if it is still the current connection) and fails
// every pending Subscribe.
func (f *Feed) resetLocked(ps *goredis.PubSub) {
	if ps == nil || f.ps != ps {
		return
	}
	f.ps = nil
	_ = ps.Close()
	for k, w := range f.waiters {
		w <- errFeedReset
		delete(f.waiters, k)
	}
}

// Subscribe implements app.ViewerFeed. It sends SUBSCRIBE (for the first
// local user) and then a PING with a unique payload on the same
// connection; Redis answers in order, so the PONG proves the subscription
// is active.
func (f *Feed) Subscribe(ctx context.Context, deliveryID string) error {
	f.mu.Lock()
	ps := f.pubsubLocked(ctx)
	f.refs[deliveryID]++
	if f.refs[deliveryID] == 1 {
		if err := ps.Subscribe(ctx, snapChannel(deliveryID)); err != nil {
			f.derefLocked(ctx, deliveryID)
			f.resetLocked(ps)
			f.mu.Unlock()
			return err
		}
	}
	f.seq++
	token := "s" + strconv.FormatUint(f.seq, 10)
	w := make(chan error, 1)
	f.waiters[token] = w
	if err := ps.Ping(ctx, token); err != nil {
		delete(f.waiters, token)
		f.derefLocked(ctx, deliveryID)
		f.resetLocked(ps)
		f.mu.Unlock()
		return err
	}
	f.mu.Unlock()

	timer := time.NewTimer(subscribeTimeout)
	defer timer.Stop()
	var err error
	select {
	case err = <-w:
	case <-ctx.Done():
		err = ctx.Err()
	case <-timer.C:
		err = errors.New("viewer feed: subscribe timed out")
	}
	if err != nil {
		f.mu.Lock()
		delete(f.waiters, token)
		f.derefLocked(ctx, deliveryID)
		f.mu.Unlock()
	}
	return err
}

// Unsubscribe implements app.ViewerFeed.
func (f *Feed) Unsubscribe(ctx context.Context, deliveryID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.derefLocked(ctx, deliveryID)
}

func (f *Feed) derefLocked(ctx context.Context, deliveryID string) {
	n, ok := f.refs[deliveryID]
	if !ok {
		return
	}
	if n > 1 {
		f.refs[deliveryID] = n - 1
		return
	}
	delete(f.refs, deliveryID)
	if f.ps != nil {
		// A failed UNSUBSCRIBE only leaves messages nobody listens to.
		_ = f.ps.Unsubscribe(context.WithoutCancel(ctx), snapChannel(deliveryID))
	}
}

// Run implements app.ViewerFeed.
func (f *Feed) Run(ctx context.Context, h app.FeedHandler) error {
	defer func() {
		f.mu.Lock()
		f.resetLocked(f.ps)
		f.mu.Unlock()
	}()
	go f.heartbeat(ctx)
	backoff := feedBackoffMin
	resync := false
	for ctx.Err() == nil {
		f.mu.Lock()
		ps := f.pubsubLocked(ctx)
		f.mu.Unlock()
		received, err := f.receive(ctx, ps, h, &resync)
		if ctx.Err() != nil {
			return nil
		}
		f.log.WarnContext(ctx, "viewer_feed_reconnecting", "error", err)
		f.mu.Lock()
		f.resetLocked(ps)
		f.mu.Unlock()
		resync = true
		if received {
			backoff = feedBackoffMin
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, feedBackoffMax)
	}
	return nil
}

// receive reads ps until it fails; it reports whether anything arrived.
func (f *Feed) receive(ctx context.Context, ps *goredis.PubSub, h app.FeedHandler, resync *bool) (bool, error) {
	received := false
	for {
		msg, err := ps.ReceiveTimeout(ctx, feedReceiveTimeout)
		if err != nil {
			return received, err
		}
		received = true
		if *resync {
			*resync = false
			h.Resync()
		}
		switch m := msg.(type) {
		case *goredis.Message:
			f.dispatch(ctx, m, h)
		case *goredis.Pong:
			if m.Payload == heartbeatPayload {
				continue
			}
			f.mu.Lock()
			if w, ok := f.waiters[m.Payload]; ok {
				delete(f.waiters, m.Payload)
				w <- nil
			}
			f.mu.Unlock()
		}
	}
}

func (f *Feed) dispatch(ctx context.Context, m *goredis.Message, h app.FeedHandler) {
	switch {
	case m.Channel == viewerjwt.RevocationsChannel:
		if m.Payload != "" {
			h.Revoked(m.Payload)
		}
	case m.Channel == viewerjwt.ExpiriesChannel:
		id, at, err := viewerjwt.DecodeExpiry(m.Payload)
		if err != nil {
			f.log.WarnContext(ctx, "viewer_feed_bad_expiry")
			return
		}
		h.Expiry(id, at)
	case strings.HasPrefix(m.Channel, snapPrefix):
		h.Snapshot(strings.TrimPrefix(m.Channel, snapPrefix), []byte(m.Payload))
	}
}

func (f *Feed) heartbeat(ctx context.Context) {
	t := time.NewTicker(feedHeartbeat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			f.mu.Lock()
			ps := f.ps
			f.mu.Unlock()
			if ps != nil {
				_ = ps.Ping(ctx, heartbeatPayload)
			}
		}
	}
}
