package ingest

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/raduolivas/dt/backend/pkg/testkit"
)

// A jetstream.New failure must close the connection and retry, not end ingest.
func TestRunNATSLoopRetriesWhenJetStreamInitFails(t *testing.T) {
	url := testkit.StartNATS(t)
	var calls atomic.Int32
	orig := newJetStream
	newJetStream = func(nc *nats.Conn) (jetstream.JetStream, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("boom")
		}
		return jetstream.New(nc)
	}
	t.Cleanup(func() { newJetStream = orig })

	var oks atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunNATSLoop(ctx, url, nil, nil, NATSOptions{RetryEvery: 50 * time.Millisecond},
			func(outcome, _ string) {
				if outcome == "ok" {
					oks.Add(1)
				}
			})
	}()
	require.Eventually(t, func() bool { return calls.Load() >= 2 && oks.Load() >= 1 }, 10*time.Second, 20*time.Millisecond)
	cancel()
	<-done
}
