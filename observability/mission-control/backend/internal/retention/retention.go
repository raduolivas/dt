// Package retention deletes Mission Control data older than the retention window.
package retention

import (
	"context"
	"log/slog"
	"time"
)

// Pruner is the part of the store the job needs.
type Pruner interface {
	DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
	DeleteOrdersOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
	DeleteDowntimeOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
}

// Run prunes once immediately, then every `every`, until ctx ends. Failures
// are logged and retried at the next tick.
func Run(ctx context.Context, st Pruner, every, keep time.Duration) {
	prune(ctx, st, keep)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			prune(ctx, st, keep)
		}
	}
}

func prune(ctx context.Context, st Pruner, keep time.Duration) {
	cutoff := time.Now().Add(-keep)
	steps := []struct {
		name string
		f    func(context.Context, time.Time) (int64, error)
	}{
		{"events", st.DeleteOlderThan}, {"orders", st.DeleteOrdersOlderThan}, {"downtime", st.DeleteDowntimeOlderThan},
	}
	for _, s := range steps {
		n, err := s.f(ctx, cutoff)
		switch {
		case err != nil:
			if ctx.Err() == nil {
				slog.Warn("retention prune failed", "table", s.name, "error", err.Error())
			}
		case n > 0:
			slog.Info("retention prune", "table", s.name, "deleted", n)
		}
	}
}
