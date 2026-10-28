package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

// Signal sweeper timing (design §3.3: timers are evaluated by tracking, not
// only when a fix arrives).
const (
	// SweepInterval is how often the lease holder sweeps.
	SweepInterval = 5 * time.Second
	// LeaseTTL is the sweeper lease's lifetime; the holder renews it every
	// SweepInterval, and a standby takes over within LeaseTTL of the holder
	// stopping.
	LeaseTTL = 10 * time.Second
	// sweepTimeout bounds one sweep, so a slow one ends before its lease.
	sweepTimeout = 8 * time.Second
)

// SweepSignals re-evaluates the courier signal of every dt:active delivery
// whose signal can have changed (last fix, or visible-since, at least 30 s
// ago). A changed signal goes through Apply, which stores it, gives the
// snapshot a new version, publishes it and enqueues
// tracking.courier_signal_lost once per gap. Entries of deliveries that are
// final, hidden or gone are removed. One delivery's failure is logged and
// skipped.
func (s *Service) SweepSignals(ctx context.Context) error {
	now := s.Clock.Now()
	entries, err := s.States.ActiveSince(ctx, now.Add(-domain.SignalStaleAfter))
	if err != nil {
		return fmt.Errorf("list active deliveries: %w", err)
	}
	for _, e := range entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.sweepOne(ctx, e)
	}
	return nil
}

func (s *Service) sweepOne(ctx context.Context, e ActiveDelivery) {
	drop := false
	err := s.Apply(ctx, e.Scope, e.DeliveryID, func(st *domain.State) error {
		drop = false // Apply may run the mutation again
		if _, active := domain.SweepActive(*st); !active {
			drop = true
			return ErrNoChange
		}
		sig := domain.ViewerSignal(*st, s.Clock.Now())
		// A lost signal whose event could not be enqueued is retried.
		if sig == st.Signal && (sig != domain.SignalLost || st.SignalLostSent) {
			return ErrNoChange
		}
		return nil // Apply stores the new signal
	})
	switch {
	case errors.Is(err, ErrNotFound):
		drop = true
	case err != nil:
		s.log().WarnContext(ctx, "signal_sweep_failed", "delivery_id", e.DeliveryID, "error", err)
		return
	}
	if drop {
		if err := s.States.SetActive(ctx, e.Scope, e.DeliveryID, time.Time{}, false); err != nil {
			s.log().WarnContext(ctx, "active_set_update_failed", "delivery_id", e.DeliveryID, "error", err)
		}
	}
}

// Sweeper runs SweepSignals every SweepInterval on the one instance that
// holds the sweeper lease (dt:lease:sweeper).
type Sweeper struct {
	Svc   *Service
	Lease Lease
	// ID names this instance as the lease holder; unique per process.
	ID string
}

// Tick takes or renews the lease and, when this instance holds it, sweeps.
// It reports whether it swept.
func (w *Sweeper) Tick(ctx context.Context) (bool, error) {
	held, err := w.Lease.Hold(ctx, w.ID, LeaseTTL)
	if err != nil {
		return false, fmt.Errorf("sweeper lease: %w", err)
	}
	if !held {
		return false, nil
	}
	sctx, cancel := context.WithTimeout(ctx, sweepTimeout)
	defer cancel()
	return true, w.Svc.SweepSignals(sctx)
}

// Run ticks every SweepInterval until ctx ends, then releases the lease so
// a standby takes over at once.
func (w *Sweeper) Run(ctx context.Context) error {
	t := time.NewTicker(SweepInterval)
	defer t.Stop()
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_ = w.Lease.Release(rctx, w.ID)
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if _, err := w.Tick(ctx); err != nil && ctx.Err() == nil {
				w.Svc.log().WarnContext(ctx, "signal_sweep_failed", "error", err)
			}
		}
	}
}
