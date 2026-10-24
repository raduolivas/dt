package app

import (
	"context"
	"log/slog"
	"time"
)

const (
	defaultSweepInterval = 30 * time.Second
	sweepBatch           = 100
)

// PickupSweeper warns about pickups still waiting for confirmation. The
// repository decides what is due and emits the event, at most once per
// delivery.
type PickupSweeper struct {
	Repo     Repo
	Clock    Clock
	Log      *slog.Logger
	Interval time.Duration // default 30s
}

// Run sweeps immediately and then every Interval until ctx is done, which
// is the returned error.
func (p *PickupSweeper) Run(ctx context.Context) error {
	iv := p.Interval
	if iv <= 0 {
		iv = defaultSweepInterval
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		p.Sweep(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Sweep warns about every watch that is due now. A failure on one watch is
// logged and does not stop the others.
func (p *PickupSweeper) Sweep(ctx context.Context) {
	log := p.Log
	if log == nil {
		log = slog.Default()
	}
	now := p.Clock.Now()
	ws, err := p.Repo.DuePickupWatches(ctx, now, sweepBatch)
	if err != nil {
		log.ErrorContext(ctx, "pickup sweep: list due watches", "err", err)
		return
	}
	for _, w := range ws {
		if err := p.Repo.WarnPickupPending(ctx, w, now); err != nil {
			log.ErrorContext(ctx, "pickup sweep: warn", "delivery_id", w.DeliveryID, "err", err)
		}
	}
}
