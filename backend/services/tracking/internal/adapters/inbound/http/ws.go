package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// WebSocket limits and close codes.
const (
	// maxClientMessage bounds a client message ({type:"auth",token} is a
	// few hundred bytes).
	maxClientMessage = 4 << 10
	// writeTimeout bounds one write; a client that cannot take a message
	// within it is dropped.
	writeTimeout = 10 * time.Second
	// finalWriteTimeout bounds the last message before a close.
	finalWriteTimeout = 2 * time.Second

	closeUnauthorized websocket.StatusCode = 4401
)

// sink writes server messages to one transport.
type sink interface {
	send(ctx context.Context, typ string, body []byte) error
}

// prober checks that the peer is still there (a WebSocket ping/pong). A
// written {type:"ping"} alone succeeds into the kernel buffer of a
// half-open connection, so it proves nothing.
type prober interface {
	probe(ctx context.Context) error
}

// pump writes s's messages and the pings to out until s ends.
func (h *Hub) pump(ctx context.Context, s *stream, out sink) {
	ping := time.NewTicker(h.d.PingInterval)
	defer ping.Stop()
	var probing atomic.Bool
	for {
		select {
		case <-s.done:
			return
		case <-ctx.Done():
			s.end(endGone)
			return
		case <-ping.C:
			if err := out.send(ctx, "ping", pingBody); err != nil {
				s.end(endGone)
				return
			}
			if p, ok := out.(prober); ok && probing.CompareAndSwap(false, true) {
				// In the background, so snapshots keep flowing meanwhile.
				go func() {
					defer probing.Store(false)
					pctx, cancel := context.WithTimeout(ctx, h.d.PongTimeout)
					defer cancel()
					if err := p.probe(pctx); err != nil {
						s.end(endGone) // releases the stream's slots
					}
				}()
			}
		case <-s.wake:
			for _, m := range s.take() {
				select {
				case <-s.done: // nothing more after a revocation
					return
				default:
				}
				if err := out.send(ctx, m.typ, m.body); err != nil {
					s.end(endGone)
					return
				}
			}
		}
	}
}

type wsSink struct{ c *websocket.Conn }

// probe sends a protocol ping and waits for the pong; readLoop is the
// concurrent reader that receives it.
func (w wsSink) probe(ctx context.Context) error { return w.c.Ping(ctx) }

func (w wsSink) send(ctx context.Context, _ string, body []byte) error {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return w.c.Write(ctx, websocket.MessageText, body)
}

// websocket serves GET /v1/tracking/stream. The token arrives in the first
// message, never in the URL. Any origin may connect: the token, not a
// cookie, is the credential, so cross-site WebSocket hijacking gains nothing.
func (h *Hub) websocket(w http.ResponseWriter, r *http.Request) {
	release, slotErr := h.acquire()
	// The server's read and write timeouts must not cut a long-lived stream.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil { // Accept answered the request
		if release != nil {
			release()
		}
		return
	}
	defer func() { _ = c.CloseNow() }()
	if slotErr != nil {
		if errors.Is(slotErr, errShutdown) {
			_ = c.Close(websocket.StatusServiceRestart, "service restart")
		} else {
			_ = c.Close(websocket.StatusTryAgainLater, "too many streams")
		}
		return
	}
	defer release()
	c.SetReadLimit(maxClientMessage)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	token, ok := h.readAuth(ctx, c)
	if !ok {
		return
	}
	claims, err := h.verify(ctx, token)
	switch {
	case errors.Is(err, errUnavailable):
		_ = c.Close(websocket.StatusTryAgainLater, "try again later")
		return
	case err != nil:
		_ = c.Close(closeUnauthorized, "unauthorized")
		return
	}
	s := newStream(claims)
	defer h.leave(s)
	if err := h.join(ctx, s); err != nil {
		switch {
		case errors.Is(err, errShutdown):
			_ = c.Close(websocket.StatusServiceRestart, "service restart")
		case errors.Is(err, errLinkFull):
			_ = c.Close(websocket.StatusTryAgainLater, "too many streams")
		default:
			h.log.WarnContext(ctx, "viewer_subscribe_failed", "error", err)
			_ = c.Close(websocket.StatusTryAgainLater, "try again later")
		}
		return
	}
	h.open(ctx, s)

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		h.readLoop(ctx, c, s)
	}()
	h.pump(ctx, s, wsSink{c})
	h.finishWS(ctx, c, s)
	_ = c.CloseNow()
	<-readDone
}

// readAuth reads the first message, which must be {type:"auth",token}
// within the auth timeout; otherwise it closes the connection 4401.
func (h *Hub) readAuth(ctx context.Context, c *websocket.Conn) (string, bool) {
	timedOut := make(chan struct{})
	timer := time.AfterFunc(h.d.AuthTimeout, func() {
		close(timedOut)
		_ = c.Close(closeUnauthorized, "auth timeout")
	})
	m, err := readMessage(ctx, c)
	if !timer.Stop() {
		<-timedOut
		return "", false
	}
	if err != nil {
		if !errors.Is(err, errReadFailed) {
			_ = c.Close(closeUnauthorized, "auth required")
		}
		return "", false
	}
	if m.Type != "auth" {
		_ = c.Close(closeUnauthorized, "auth required")
		return "", false
	}
	return m.Token, true
}

var (
	errReadFailed = errors.New("read failed")
	errBadMessage = errors.New("bad message")
)

type clientMessage struct {
	Type  string `json:"type"`
	Token string `json:"token"`
}

// readMessage reads one client message: errReadFailed when the connection
// failed (the library already closed it, e.g. 1009 for an oversized
// message), errBadMessage when it is not a JSON text message.
func readMessage(ctx context.Context, c *websocket.Conn) (clientMessage, error) {
	typ, b, err := c.Read(ctx)
	if err != nil {
		return clientMessage{}, errReadFailed
	}
	var m clientMessage
	if typ != websocket.MessageText || json.Unmarshal(b, &m) != nil {
		return clientMessage{}, errBadMessage
	}
	return m, nil
}

// readLoop handles client messages after authentication: a new auth swaps
// in a refreshed token for the same link and delivery. Any other type,
// anything that is not JSON text, or swaps faster than the swap
// interval, ends the stream with 1008.
func (h *Hub) readLoop(ctx context.Context, c *websocket.Conn, s *stream) {
	last := time.Now()
	for {
		m, err := readMessage(ctx, c)
		switch {
		case errors.Is(err, errReadFailed):
			s.end(endGone)
			return
		case err != nil:
			s.end(endPolicy)
			return
		}
		if m.Type != "auth" {
			// The protocol defines no other client message.
			s.end(endPolicy)
			return
		}
		if time.Since(last) < h.d.SwapInterval {
			s.end(endPolicy)
			return
		}
		last = time.Now()
		h.swap(ctx, s, m.Token)
	}
}

// swap checks a refreshed token: it must verify and name the same link,
// delivery, tenant and environment, and the link must still be good. While
// the keys cannot be loaded the stream keeps its current authorization.
func (h *Hub) swap(ctx context.Context, s *stream, token string) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	c, err := h.verify(ctx, token)
	switch {
	case errors.Is(err, errUnavailable):
		return
	case err != nil, c != s.claims:
		s.end(endUnauthorized)
		return
	}
	h.checkLink(ctx, s)
}

// finishWS sends the last message for the end reason and closes.
func (h *Hub) finishWS(ctx context.Context, c *websocket.Conn, s *stream) {
	switch s.endReason() {
	case endRevoked:
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finalWriteTimeout)
		_ = c.Write(wctx, websocket.MessageText, revokedBody)
		cancel()
		_ = c.Close(closeUnauthorized, "revoked")
	case endUnauthorized:
		_ = c.Close(closeUnauthorized, "unauthorized")
	case endShutdown:
		_ = c.Close(websocket.StatusServiceRestart, "service restart")
	case endUnavailable:
		_ = c.Close(websocket.StatusTryAgainLater, "try again later")
	case endPolicy:
		_ = c.Close(websocket.StatusPolicyViolation, "policy violation")
	}
}
