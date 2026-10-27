package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/raduolivas/dt/backend/pkg/ids"
	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

// ErrSimulationState means the delivery cannot be simulated now: it is final
// or a simulation of it is already running (409 simulation_state).
var ErrSimulationState = errors.New("simulation: the delivery is final or already being simulated")

// ErrSimulatorClosed means the simulator is shutting down.
var ErrSimulatorClosed = errors.New("simulation: the simulator is shutting down")

// RejectedError is a deliveries refusal the simulator passes on to its
// caller: Code is the deliveries problem code (validation_failed,
// courier_not_found, model_not_allowed, ...).
type RejectedError struct {
	Code string
	Err  error
}

func (e *RejectedError) Error() string {
	return "deliveries rejected the call: " + e.Code + ": " + e.Err.Error()
}
func (e *RejectedError) Unwrap() error { return e.Err }

// Simulator limits and behaviour (design §6).
const (
	// SimCourierExternalID is the external id of the test courier the
	// simulator creates when none is assigned.
	SimCourierExternalID = "dt-simulator"
	// simTick is the simulated time between two fixes.
	simTick = 2 * time.Second
	// simApproachM and simApproachBearing place an End-to-end courier's
	// start: 1.5 km south-west of the store, snapped to a road.
	simApproachM       = 1500.0
	simApproachBearing = 225.0
	// simHandover is the simulated time at the dropoff before delivered.
	simHandover = 30 * time.Second
	// simMaxSpeed caps speed_factor; simMaxGapS caps both signal gap values.
	simMaxSpeed = 20
	simMaxGapS  = 3600
	// simMaxRun bounds a run in real time.
	simMaxRun = 3 * time.Hour
	// simReportAttempts is how often a status report is tried.
	simReportAttempts = 3
	// simAccuracyM is the accuracy every simulated fix claims.
	simAccuracyM = 5.0
	// Bounds on the simulated driving speed in m/s.
	simMinMps, simMaxMps = 1.0, 40.0
)

// SignalGap stops the fixes for a while (design §6), to show the stale and
// lost states: AfterS seconds of simulated time after the start, for
// DurationS seconds of real time. The courier keeps driving through the gap
// at real-time speed, so the fixes after it are plausible.
type SignalGap struct{ AfterS, DurationS int }

// Simulator drives virtual couriers through sandbox deliveries (design §6).
// Runs are in-process, at most one per delivery in this instance.
type Simulator struct {
	Svc        *Service
	Deliveries Deliveries
	// Snapper is optional: without it the End-to-end start point is not
	// snapped to a road.
	Snapper RoadSnapper
	// Base is the runs' parent context (the service's lifetime); nil is
	// context.Background().
	Base context.Context
	// Sleep waits d of real time; nil uses a timer.
	Sleep func(ctx context.Context, d time.Duration) error
	// IDs returns a new simulation id; nil uses ids.New(ids.Simulation).
	IDs func() string
	Log *slog.Logger

	mu      sync.Mutex
	running map[string]context.CancelFunc // by runKey
	closed  bool
	wg      sync.WaitGroup
}

// runKey keys a run by tenant, environment and delivery, so one tenant's
// run never shows through to another.
func runKey(sc Scope, deliveryID string) string { return sc.TenantID + "|" + sc.Env + "|" + deliveryID }

func (s *Simulator) log() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return s.Log
}

func (s *Simulator) sleep(ctx context.Context, d time.Duration) error {
	if s.Sleep != nil {
		return s.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// reserve claims key with the run's cancel func.
func (s *Simulator) reserve(key string, cancel context.CancelFunc) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrSimulatorClosed
	}
	if _, ok := s.running[key]; ok {
		return ErrSimulationState
	}
	if s.running == nil {
		s.running = map[string]context.CancelFunc{}
	}
	s.running[key] = cancel
	s.wg.Add(1)
	return nil
}

func (s *Simulator) release(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, key)
	s.wg.Done()
}

// Wait blocks until every run started so far has ended.
func (s *Simulator) Wait() { s.wg.Wait() }

// Close stops every run, waits for them to end and refuses new ones.
func (s *Simulator) Close() {
	s.mu.Lock()
	s.closed = true
	for _, cancel := range s.running {
		cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

// Run is a service worker: when ctx ends it stops the runs and waits for
// them, so none is still ingesting or reporting while the pools close.
func (s *Simulator) Run(ctx context.Context) error {
	<-ctx.Done()
	s.Close()
	return nil
}

// Start checks the request, assigns the simulated courier when the delivery
// has none (upserting "dt-simulator"), and starts the run in the
// background. speedFactor 0 is 1. It returns ErrInvalidArgument for a speed
// outside 1–20, a gap value outside 0–3600 or a live scope (sandbox only),
// ErrNotFound for a delivery outside the scope (checked before the run
// guard, which is per tenant and environment), ErrSimulationState for a
// final delivery or one already being simulated, ErrSimulatorClosed during
// shutdown and an *RejectedError when deliveries refuses the courier
// assignment.
func (s *Simulator) Start(ctx context.Context, sc Scope, deliveryID string, speedFactor float64, gap *SignalGap) (string, error) {
	if speedFactor == 0 {
		speedFactor = 1
	}
	if !(speedFactor >= 1 && speedFactor <= simMaxSpeed) {
		return "", fmt.Errorf("%w: speed_factor must be 1 to %d", ErrInvalidArgument, simMaxSpeed)
	}
	if gap != nil && (gap.AfterS < 0 || gap.AfterS > simMaxGapS || gap.DurationS < 0 || gap.DurationS > simMaxGapS) {
		return "", fmt.Errorf("%w: signal_gap values must be 0 to %d", ErrInvalidArgument, simMaxGapS)
	}
	if sc.Env != "test" {
		return "", fmt.Errorf("%w: the simulator runs in the test environment only", ErrInvalidArgument)
	}
	// The scoped read comes first: a delivery outside the scope is
	// ErrNotFound whether or not another tenant's run holds it.
	d, err := s.Deliveries.Get(ctx, sc, deliveryID)
	if err != nil {
		return "", err
	}
	if d.Status.IsFinal() {
		return "", ErrSimulationState
	}
	base := s.Base
	if base == nil {
		base = context.Background()
	}
	runCtx, cancel := context.WithTimeout(base, simMaxRun)
	key := runKey(sc, deliveryID)
	if err := s.reserve(key, cancel); err != nil {
		cancel()
		return "", err
	}
	r, err := s.prepare(ctx, sc, d, speedFactor, gap)
	if err != nil {
		cancel()
		s.release(key)
		return "", err
	}
	go func() {
		defer s.release(key)
		defer cancel()
		ctx := runCtx
		if err := r.run(ctx); err != nil {
			s.log().WarnContext(ctx, "simulation_stopped", "simulation_id", r.id, "delivery_id", deliveryID, "error", err)
			return
		}
		s.log().InfoContext(ctx, "simulation_finished", "simulation_id", r.id, "delivery_id", deliveryID)
	}()
	return r.id, nil
}

// prepare assigns the simulated courier if the delivery has none.
func (s *Simulator) prepare(ctx context.Context, sc Scope, d SimDelivery, speed float64, gap *SignalGap) (*simRun, error) {
	deliveryID := d.ID
	if d.Courier == nil {
		c, err := s.Deliveries.UpsertCourier(ctx, sc, CourierUpsert{
			ExternalID: SimCourierExternalID, DisplayName: "Sim Courier", Vehicle: "e_bike"})
		if err != nil {
			return nil, fmt.Errorf("upsert simulated courier: %w", err)
		}
		if err := s.Deliveries.Assign(ctx, sc, deliveryID, c.ID); err != nil {
			return nil, fmt.Errorf("assign simulated courier: %w", err)
		}
		// Assignment moves an End-to-end delivery to courier_assigned.
		if d, err = s.Deliveries.Get(ctx, sc, deliveryID); err != nil {
			return nil, err
		}
		if d.Courier == nil {
			d.Courier = &c
		}
	}
	id := ids.New(ids.Simulation)
	if s.IDs != nil {
		id = s.IDs()
	}
	return &simRun{s: s, sc: sc, d: d, id: id, speed: speed, gap: gap, started: s.Svc.Clock.Now()}, nil
}

// simRun is one simulation.
type simRun struct {
	s       *Simulator
	sc      Scope
	d       SimDelivery
	id      string
	speed   float64
	gap     *SignalGap
	started time.Time

	elapsed time.Duration // simulated time since the start
	gapUsed time.Duration // real time spent in the signal gap
	pos     domain.LatLng
	heading float64
	failed  bool // an ingest failed (logged once)
}

// run follows design §6: an End-to-end courier drives from 1.5 km away to
// the store and reports at_pickup; a Direct courier waits at the store. Both
// wait for the ready time, confirm the pickup as the policy requires, drive
// to the dropoff and report delivered. A delivery already picked up is
// driven from the store.
func (r *simRun) run(ctx context.Context) error {
	r.pos = r.d.Pickup
	if r.d.Status.BeforePickup() {
		if r.d.Model == domain.ModelEndToEnd && r.d.Status != domain.StatusAtPickup {
			start := r.approachStart(ctx)
			if err := r.drive(ctx, start, r.d.Pickup); err != nil {
				return err
			}
			if err := r.report(ctx, StatusReport{Type: "at_pickup", Actor: "courier"}); err != nil {
				return err
			}
		}
		if err := r.waitReady(ctx); err != nil {
			return err
		}
		if err := r.confirmPickup(ctx); err != nil {
			return err
		}
	}
	if err := r.drive(ctx, r.d.Pickup, r.d.Dropoff); err != nil {
		return err
	}
	for end := r.elapsed + simHandover; r.elapsed < end; {
		if err := r.tick(ctx, 0); err != nil {
			return err
		}
	}
	return r.report(ctx, StatusReport{Type: "delivered", Actor: "courier"})
}

func (r *simRun) profile() string { return profileFor(r.d.Courier) }

// approachStart is the point 1.5 km south-west of the store, snapped to the
// nearest road when a snapper is configured and answers.
func (r *simRun) approachStart(ctx context.Context) domain.LatLng {
	p := domain.Destination(r.d.Pickup, simApproachBearing, simApproachM)
	if r.s.Snapper == nil {
		return p
	}
	q, err := r.s.Snapper.Nearest(ctx, r.profile(), p)
	if err != nil {
		r.s.log().WarnContext(ctx, "simulation_snap_failed", "simulation_id", r.id, "error", err)
		return p
	}
	return q
}

// path is the road path from -> to and its speed in m/s: the router's drive
// time, else a straight line at the vehicle's typical speed.
func (r *simRun) path(ctx context.Context, from, to domain.LatLng) ([]domain.LatLng, float64) {
	mps := typicalKmh(r.d.Courier) / 3.6
	if r.s.Svc.Router == nil {
		return []domain.LatLng{from, to}, mps
	}
	path, sec, err := r.s.Svc.Router.Route(ctx, r.profile(), from, to)
	if err != nil || len(path) < 2 {
		r.s.log().WarnContext(ctx, "simulation_route_fallback", "simulation_id", r.id, "error", err)
		return []domain.LatLng{from, to}, mps
	}
	if l := domain.PathLengthM(path); sec > 0 && l > 0 {
		mps = math.Max(simMinMps, math.Min(simMaxMps, l/float64(sec)))
	}
	return path, mps
}

// drive moves the courier from -> to along the road, one fix per tick, the
// last one at to.
func (r *simRun) drive(ctx context.Context, from, to domain.LatLng) error {
	path, mps := r.path(ctx, from, to)
	total := domain.PathLengthM(path)
	for dist := 0.0; ; dist = math.Min(total, dist+mps*simTick.Seconds()) {
		r.pos, r.heading = domain.Along(path, dist)
		v := mps
		if dist >= total {
			v = 0
		}
		if err := r.tick(ctx, v); err != nil {
			return err
		}
		if dist >= total {
			return nil
		}
	}
}

// waitReady keeps the courier at the store until the ready time (ready_at,
// else creation plus the default preparation time), counted in simulated
// time from the start. With no ready time known it returns at once.
func (r *simRun) waitReady(ctx context.Context) error {
	st, err := r.s.Svc.liveState(ctx, r.sc, r.d.ID)
	if err != nil {
		r.s.log().WarnContext(ctx, "simulation_ready_time_unknown", "simulation_id", r.id, "error", err)
		return nil
	}
	ready, ok := domain.ReadyTime(st)
	if !ok {
		return nil
	}
	for wait := ready.Sub(r.started); r.elapsed < wait; {
		if err := r.tick(ctx, 0); err != nil {
			return err
		}
	}
	return nil
}

// confirmPickup confirms the pickup the way the policy requires: as the
// courier (any, courier), as the store (store), or both.
func (r *simRun) confirmPickup(ctx context.Context) error {
	courier := StatusReport{Type: "pickup_confirmed", Actor: "courier"}
	store := StatusReport{Type: "pickup_confirmed", Actor: "partner", By: "store"}
	var reports []StatusReport
	switch r.d.PickupPolicy {
	case "store":
		reports = []StatusReport{store}
	case "courier_and_store":
		reports = []StatusReport{courier, store}
	default: // any, courier
		reports = []StatusReport{courier}
	}
	for _, rep := range reports {
		if err := r.report(ctx, rep); err != nil {
			return err
		}
	}
	return nil
}

// report sends one status report under an event id derived from the
// simulation, so a retry is a duplicate deliveries ignores.
func (r *simRun) report(ctx context.Context, rep StatusReport) error {
	rep.EventID = r.id + ":" + rep.Type
	if rep.Type == "pickup_confirmed" {
		rep.EventID += ":" + rep.Actor
	}
	var err error
	for attempt := range simReportAttempts {
		if attempt > 0 {
			if serr := r.s.sleep(ctx, time.Second); serr != nil {
				return serr
			}
		}
		if err = r.s.Deliveries.Report(ctx, r.sc, r.d.ID, rep); err == nil {
			return nil
		}
		var rej *RejectedError
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrSimulationState) || errors.As(err, &rej) {
			break
		}
	}
	return fmt.Errorf("report %s: %w", rep.Type, err)
}

// tick sends a fix at the current position (unless the signal gap is on)
// and waits one tick: 2 s of simulated time, which is 2 s / speed_factor of
// real time, or 2 s of real time during the gap.
func (r *simRun) tick(ctx context.Context, mps float64) error {
	wait := time.Duration(float64(simTick) / r.speed)
	if r.gap != nil && r.elapsed >= time.Duration(r.gap.AfterS)*time.Second &&
		r.gapUsed < time.Duration(r.gap.DurationS)*time.Second {
		wait = simTick
		r.gapUsed += simTick
	} else {
		r.emit(ctx, mps)
	}
	r.elapsed += simTick
	return r.s.sleep(ctx, wait)
}

// emit sends one fix through the ingest path, tagged simulator. A failure
// is logged once per run; the next tick tries again.
func (r *simRun) emit(ctx context.Context, mps float64) {
	v, h := mps, r.heading
	f := domain.Fix{T: r.s.Svc.Clock.Now(), Pos: r.pos, AccuracyM: simAccuracyM, SpeedMps: &v, HeadingDeg: &h}
	if _, _, err := r.s.Svc.Ingest(ctx, r.sc, r.d.Courier.ID, []domain.Fix{f}, "simulator"); err != nil && !r.failed {
		r.failed = true
		r.s.log().WarnContext(ctx, "simulation_ingest_failed", "simulation_id", r.id, "error", err)
	}
}
