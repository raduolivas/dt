package sim

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// R38: after a restart the registry is empty; the stored order origin keeps later events simulated.
func TestRegistryFallsBackToStoredOrigin(t *testing.T) {
	calls := 0
	stored := map[string]string{"del_sim": "simulated", "del_live": "live"}
	reg := NewRegistry(func(_ context.Context, id string) (string, error) {
		calls++
		return stored[id], nil
	})
	assert.True(t, reg.IsSimulated("del_sim"), "a fresh registry asks the store")
	assert.True(t, reg.IsSimulated("del_sim"))
	assert.Equal(t, 1, calls, "a simulated answer is cached")
	assert.False(t, reg.IsSimulated("del_live"))
	assert.False(t, reg.IsSimulated("del_unknown"))

	// the order row may not exist yet when the first event arrives: a "no" is not remembered for long
	stored["del_late"] = ""
	assert.False(t, reg.IsSimulated("del_late"))
	stored["del_late"] = "simulated"
	reg.negTTL = 0
	assert.True(t, reg.IsSimulated("del_late"))

	// a failing store answers false
	bad := NewRegistry(func(context.Context, string) (string, error) { return "", assert.AnError })
	assert.False(t, bad.IsSimulated("del_x"))
}

func TestOrderRefHasSimPrefix(t *testing.T) {
	f := newFakeGateway(t)
	m := newManager(t, f)
	run, err := m.Start(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "mc-sim-", OrderRefPrefix)
	assert.Contains(t, run.OrderRef, OrderRefPrefix)
}

func TestRunCapIsAtomicAndReleasedOnFailure(t *testing.T) {
	f := newFakeGateway(t)
	m := newManager(t, f)
	var wg sync.WaitGroup
	var ok, tooMany atomic.Int32
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.Start(context.Background())
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, ErrTooManyRuns):
				tooMany.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(maxRuns), ok.Load(), "never more than the cap, never fewer")
	assert.Equal(t, int32(20-maxRuns), tooMany.Load())
	assert.Len(t, m.Status(), maxRuns)

	// a failed start gives its slot back
	f2 := newFakeGateway(t)
	f2.srv.Close()
	m2, err := New(Config{GatewayURL: f2.srv.URL, Key: testKey, FixInterval: 40 * time.Millisecond})
	require.NoError(t, err)
	t.Cleanup(m2.Close)
	for range maxRuns + 2 {
		_, err := m2.Start(context.Background())
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrTooManyRuns)
	}
}
