package httpapi

import (
	"sync"
	"time"

	"github.com/raduolivas/dt/backend/pkg/viewerjwt"
	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

// endReason says why a stream ends; the transport maps it to its last
// message and close code.
type endReason int

const (
	// endRevoked: the link was rotated or expired ({type:"revoked"}, 4401).
	endRevoked endReason = iota + 1
	// endUnauthorized: no, a bad or a mismatched token (4401).
	endUnauthorized
	// endShutdown: the instance is stopping (1012, reconnect).
	endShutdown
	// endUnavailable: a dependency failed (1013, try again later).
	endUnavailable
	// endPolicy: the client broke the protocol (1008).
	endPolicy
	// endGone: the client went away or could not keep up.
	endGone
)

// outMsg is a queued server message.
type outMsg struct {
	typ      string // snapshot | status
	body     []byte
	version  int64
	status   domain.Status
	statusAt time.Time
}

// stream is one viewer's WebSocket or SSE stream. The hub offers it
// messages without blocking; its transport goroutine drains them.
type stream struct {
	claims viewerjwt.Claims // fixed after authentication

	// Guarded by the hub's mutex.
	joined bool
	// Owned by the transport goroutine.
	subscribed bool

	mu          sync.Mutex
	queue       []outMsg
	lastVersion int64
	lastStatus  domain.Status
	expiry      *time.Timer
	expiryAt    time.Time

	wake    chan struct{}
	done    chan struct{}
	endOnce sync.Once
	reason  endReason
}

func newStream(c viewerjwt.Claims) *stream {
	return &stream{claims: c, wake: make(chan struct{}, 1), done: make(chan struct{})}
}

// end marks the stream finished for r; the first reason wins.
func (s *stream) end(r endReason) {
	s.endOnce.Do(func() {
		s.reason = r
		close(s.done)
	})
}

// endReason is valid once done is closed.
func (s *stream) endReason() endReason {
	<-s.done
	return s.reason
}

// offer queues a snapshot newer than any queued or sent, followed by a
// status message when the status changed. Past queueSize the oldest
// snapshot is dropped, so a slow client gets the newest state and the hub
// never waits.
func (s *stream) offer(m outMsg) {
	s.mu.Lock()
	if m.version <= s.lastVersion {
		s.mu.Unlock()
		return
	}
	s.lastVersion = m.version
	s.queue = append(s.queue, m)
	if s.lastStatus != "" && m.status != s.lastStatus {
		s.queue = append(s.queue, outMsg{typ: "status", body: statusBody(m.status, m.statusAt), status: m.status})
	}
	s.lastStatus = m.status
	for len(s.queue) > queueSize {
		drop := 0
		for i, q := range s.queue {
			if q.typ == "snapshot" {
				drop = i
				break
			}
		}
		s.queue = append(s.queue[:drop], s.queue[drop+1:]...)
	}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// take returns and clears the queue.
func (s *stream) take() []outMsg {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.queue
	s.queue = nil
	return q
}

// expireAt ends the stream (revoked) at the link's expiry; the earliest
// known expiry wins.
func (s *stream) expireAt(at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.expiryAt.IsZero() && !at.Before(s.expiryAt) {
		return
	}
	s.expiryAt = at
	if s.expiry != nil {
		s.expiry.Stop()
	}
	d := time.Until(at)
	if d <= 0 {
		s.end(endRevoked)
		return
	}
	s.expiry = time.AfterFunc(d, func() { s.end(endRevoked) })
}

func (s *stream) stopTimers() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.expiry != nil {
		s.expiry.Stop()
	}
}
