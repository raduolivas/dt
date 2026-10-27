package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// sseRetryMs is the reconnect hint sent when the instance shuts down.
const sseRetryMs = "1000"

type sseSink struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func (s sseSink) send(_ context.Context, typ string, body []byte) error {
	_ = s.rc.SetWriteDeadline(time.Now().Add(writeTimeout))
	if _, err := s.w.Write([]byte("event: " + typ + "\ndata: ")); err != nil {
		return err
	}
	if _, err := s.w.Write(body); err != nil {
		return err
	}
	if _, err := s.w.Write([]byte("\n\n")); err != nil {
		return err
	}
	return s.rc.Flush()
}

func (s sseSink) comment(text string) error {
	_ = s.rc.SetWriteDeadline(time.Now().Add(writeTimeout))
	if _, err := s.w.Write([]byte(text)); err != nil {
		return err
	}
	return s.rc.Flush()
}

// sse serves GET /v1/tracking/stream/sse: the WebSocket's messages as
// events (event: <type>, data: the same JSON), flushed one by one.
func (h *Hub) sse(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	token, ok := bearerToken(r)
	if !ok {
		unauthorized(w, r)
		return
	}
	release, err := h.acquire()
	if err != nil {
		if errors.Is(err, errShutdown) {
			unavailable(w, r)
		} else {
			tooManyStreams(w, r, http.StatusServiceUnavailable)
		}
		return
	}
	defer release()
	ctx := r.Context()
	claims, err := h.verify(ctx, token)
	switch {
	case errors.Is(err, errUnavailable):
		unavailable(w, r)
		return
	case err != nil:
		unauthorized(w, r)
		return
	}
	s := newStream(claims)
	defer h.leave(s)
	if err := h.join(ctx, s); err != nil {
		switch {
		case errors.Is(err, errLinkFull):
			tooManyStreams(w, r, http.StatusTooManyRequests)
		default:
			if !errors.Is(err, errShutdown) {
				h.log.WarnContext(ctx, "viewer_subscribe_failed", "error", err)
			}
			unavailable(w, r)
		}
		return
	}
	h.open(ctx, s)
	select {
	case <-s.done: // refused before the stream started
		switch s.endReason() {
		case endRevoked, endUnauthorized:
			unauthorized(w, r)
		default:
			unavailable(w, r)
		}
		return
	default:
	}

	rc := http.NewResponseController(w)
	// The server's read timeout would otherwise cancel the request.
	_ = rc.SetReadDeadline(time.Time{})
	hdr := w.Header()
	hdr.Set("Content-Type", "text/event-stream")
	hdr.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	out := sseSink{w: w, rc: rc}
	if err := out.comment(": stream\n\n"); err != nil {
		return
	}
	h.pump(ctx, s, out)
	switch s.endReason() {
	case endRevoked:
		_ = out.send(ctx, "revoked", revokedBody)
	case endShutdown:
		_ = out.comment("retry: " + sseRetryMs + "\n\n")
	}
}
