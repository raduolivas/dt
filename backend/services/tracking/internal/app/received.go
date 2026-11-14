package app

import (
	"context"
	"time"
)

type fixReceivedKey struct{}

type fixReceived struct {
	at     time.Time
	source string
}

// WithFixReceived returns ctx carrying when the gateway received a fix and
// the fix's source. The pair travels with the snapshot it causes to the viewer
// hub, which measures the fix-to-viewer latency from it.
func WithFixReceived(ctx context.Context, at time.Time, source string) context.Context {
	return context.WithValue(ctx, fixReceivedKey{}, fixReceived{at: at, source: source})
}

// FixReceived returns what WithFixReceived stored; ok is false when ctx has no
// received time (or a zero one).
func FixReceived(ctx context.Context) (at time.Time, source string, ok bool) {
	v, found := ctx.Value(fixReceivedKey{}).(fixReceived)
	if !found || v.at.IsZero() {
		return time.Time{}, "", false
	}
	return v.at, v.source, true
}
