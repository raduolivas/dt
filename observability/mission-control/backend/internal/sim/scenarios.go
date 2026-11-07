package sim

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// Scenarios the backend can run. 5 (Redis down) and 6 (OSRM stopped) need Docker: scripts/mc-break.sh.
const (
	ScenarioPhoneStops   = 1
	ScenarioInvalidToken = 2
	ScenarioRateLimit    = 3
	ScenarioReassigned   = 4
)

// rateLimitInterval is the phone's spacing in scenario 3: 20 posts a second against the gateway's 1/s (burst 5).
const rateLimitInterval = 50 * time.Millisecond

// run is one simulated delivery with its phone.
type run struct {
	m                       *Manager
	tag, ref                string
	deliveryID, trackingURL string
	phone                   *phone
	courierA                string
	route                   []LatLng
	cancel                  context.CancelFunc
	hold                    bool // pickup waits for the first heal (scenario 4)
	released                atomic.Bool
	pickedUp                atomic.Bool

	scen atomic.Int32
	mu   sync.Mutex // serialises break/heal (they call DT)
}

func (r *run) scenario() int { return int(r.scen.Load()) }

func (r *run) breakIt(ctx context.Context, s int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.scenario() != 0 {
		if err := r.healLocked(ctx); err != nil {
			return err
		}
	}
	if s == ScenarioReassigned {
		if r.pickedUp.Load() {
			return ErrTooLate
		}
		// Only inside the sandbox tenant, and only for this run's own delivery and a courier made for it.
		b, err := r.m.p.upsertCourier(ctx, "mc-sim-"+r.tag+"-b")
		if err != nil {
			return err
		}
		if err := r.m.p.assign(ctx, r.deliveryID, b); err != nil {
			return err
		}
	}
	r.scen.Store(int32(s)) //nolint:gosec // s is 1..4
	return nil
}

func (r *run) heal(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.healLocked(ctx)
}

func (r *run) healLocked(ctx context.Context) error {
	if r.scenario() == ScenarioReassigned {
		if err := r.m.p.assign(ctx, r.deliveryID, r.courierA); err != nil {
			return err
		}
		r.released.Store(true) // the held pickup goes ahead once the reassignment is undone
	}
	r.scen.Store(0)
	return nil
}

// loop is the phone: one fix per FixInterval along the route (clamped at the last point), plus the early pickup
// confirmation and the one headless viewer. Scenarios change what it sends, never whether DT is touched elsewhere.
func (r *run) loop(ctx context.Context) {
	log := r.m.log.With("delivery_id", r.deliveryID)
	picked := false
	idx := 0
	for {
		iv := r.m.cfg.FixInterval
		switch r.scenario() {
		case ScenarioRateLimit:
			iv = rateLimitInterval
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(iv):
		}
		s := r.scenario()
		if s == ScenarioPhoneStops {
			continue
		}
		p := r.route[min(idx, len(r.route)-1)]
		idx++
		st, err := r.phone.postFix(ctx, fix{T: time.Now().UTC().Format(time.RFC3339), Lat: p.Lat, Lng: p.Lng,
			Accuracy: 6.5, Speed: 4.8, Heading: 225}, s == ScenarioInvalidToken)
		if err != nil && ctx.Err() == nil {
			log.Debug("sim phone: post failed", "error", err.Error())
		}
		// DT fans out to viewers only after pickup, so confirm it as soon as the courier is known (the first 200).
		if st == 200 && !picked && (!r.hold || r.released.Load()) {
			if err := r.phone.confirmPickup(ctx, r.deliveryID, "mc-sim-"+r.tag+"-pickup"); err != nil {
				log.Debug("sim phone: pickup failed", "error", err.Error())
				continue
			}
			picked = true
			r.pickedUp.Store(true)
			r.m.wg.Add(1)
			go func() {
				defer r.m.wg.Done()
				token, err := trackingToken(r.trackingURL)
				if err != nil {
					log.Debug("sim viewer: no token", "error", err.Error())
					return
				}
				runViewer(ctx, r.m.edge, r.m.stream, token, log)
			}()
		}
	}
}

// line returns n points on the straight line from a to b.
func line(a, b LatLng, n int) []LatLng {
	if n < 2 {
		n = 2
	}
	out := make([]LatLng, n)
	for i := range out {
		f := float64(i) / float64(n-1)
		out[i] = LatLng{round5(a.Lat + (b.Lat-a.Lat)*f), round5(a.Lng + (b.Lng-a.Lng)*f)}
	}
	return out
}

func round5(v float64) float64 { return math.Round(v*1e5) / 1e5 }
