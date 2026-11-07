// Package sim is Mission Control's simulation mode: a fake courier phone that posts fixes through DT's real gateway
// courier API, one headless SSE viewer, and the break scenarios 1-4. Scenarios 5 and 6 need Docker and are run by
// scripts/mc-break.sh, never by the backend.
//
// Everything here runs only when MC_SIMULATION=1 and MC_ENV=local with a dt_sk_test_ key (config.Load checks the
// first two, New the key). The sandbox key is used for partner calls only and is never logged or returned.
package sim

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/config"
)

var (
	// ErrUnknownRun is returned for a delivery this manager did not create.
	ErrUnknownRun = errors.New("sim: unknown run")
	// ErrBadScenario is returned for a scenario the backend does not run (only 1-4).
	ErrBadScenario = errors.New("sim: scenario must be 1-4 (5 and 6 run with make mc-break)")
	// ErrTooManyRuns is returned when the live-run cap is reached.
	ErrTooManyRuns = errors.New("sim: too many live runs")
)

const (
	maxRuns     = 8
	runLifetime = 30 * time.Minute
	defaultFix  = time.Second
	defaultLen  = 600
)

// Default route: Amsterdam, where DT's map data is (the same pickup as scripts/courier-link.sh).
var (
	defaultPickup  = LatLng{52.3731, 4.8922}
	defaultDropoff = LatLng{52.3600, 4.8852}
)

// LatLng is a position.
type LatLng struct{ Lat, Lng float64 }

// Config configures a Manager.
type Config struct {
	// GatewayURL serves the partner and courier APIs; EdgeURL serves the viewer API (session and stream), the one
	// origin the tracking page uses. Empty EdgeURL means GatewayURL.
	GatewayURL, EdgeURL string
	// Key is the sandbox secret key (dt_sk_test_).
	Key  string
	HTTP *http.Client
	Log  *slog.Logger
	// FixInterval is the phone's normal spacing between fixes (default 1 s).
	FixInterval time.Duration
	// Pickup and Dropoff default to Amsterdam; Fixes is the route length in fixes (default 600).
	Pickup, Dropoff LatLng
	Fixes           int
	// Origins looks up a delivery's stored origin (mc_order); optional.
	Origins OriginLookup
}

// Run describes a started simulated delivery.
type Run struct {
	DeliveryID  string `json:"delivery_id"`
	OrderRef    string `json:"order_ref"`
	TrackingURL string `json:"tracking_url"`
}

// RunStatus is a run and its active scenario (0 = healthy).
type RunStatus struct {
	DeliveryID string `json:"delivery_id"`
	OrderRef   string `json:"order_ref"`
	Scenario   int    `json:"scenario"`
	PickedUp   bool   `json:"picked_up"`
}

// Manager owns the runs.
type Manager struct {
	cfg    Config
	log    *slog.Logger
	p      *partner
	gwc    gw
	edge   gw
	stream *http.Client
	reg    *Registry
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.Mutex
	runs   map[string]*run
	order  []string // live runs, oldest first
	// reserved counts live runs plus starts in flight (the cap is on this).
	reserved int
}

// New validates the config; the key must be a test secret key. The error never contains the key.
func New(cfg Config) (*Manager, error) {
	const prefix = "dt_sk_test_"
	if !strings.HasPrefix(cfg.Key, prefix) || len(cfg.Key) == len(prefix) {
		return nil, errors.New("sim: the sandbox key must be a dt_sk_test_ secret key")
	}
	if cfg.GatewayURL == "" {
		return nil, errors.New("sim: no gateway url")
	}
	if cfg.EdgeURL == "" {
		cfg.EdgeURL = cfg.GatewayURL
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	if cfg.FixInterval <= 0 {
		cfg.FixInterval = defaultFix
	}
	if cfg.Pickup == (LatLng{}) {
		cfg.Pickup = defaultPickup
	}
	if cfg.Dropoff == (LatLng{}) {
		cfg.Dropoff = defaultDropoff
	}
	if cfg.Fixes <= 0 {
		cfg.Fixes = defaultLen
	}
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	sc := *cfg.HTTP
	sc.Timeout = 0 // the viewer stream lives as long as the run
	ctx, cancel := context.WithCancel(context.Background())
	g := gw{base: strings.TrimRight(cfg.GatewayURL, "/"), hc: cfg.HTTP}
	return &Manager{
		cfg: cfg, log: log, gwc: g, p: &partner{g: gw{base: g.base, hc: g.hc, idempotent: true}, key: cfg.Key},
		edge: gw{base: strings.TrimRight(cfg.EdgeURL, "/"), hc: cfg.HTTP}, stream: &sc,
		reg: NewRegistry(cfg.Origins), ctx: ctx, cancel: cancel, runs: map[string]*run{},
	}, nil
}

// FromConfig returns nil (no simulation) unless cfg.Simulation is set.
func FromConfig(c config.Config, log *slog.Logger, origins OriginLookup) (*Manager, error) {
	if !c.Simulation {
		return nil, nil //nolint:nilnil // nil means simulation is off
	}
	return New(Config{GatewayURL: c.GatewayURL, EdgeURL: c.EdgeURL, Key: c.SandboxKey, Log: log, Origins: origins})
}

// Registry is the set of simulated delivery ids (normalize.SimRegistry).
func (m *Manager) Registry() *Registry { return m.reg }

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Start creates a simulated delivery through the partner API (courier, delivery, assignment, courier session,
// token exchange) and starts its phone. ctx bounds the setup only; the run lives until Close or its lifetime ends.
func (m *Manager) Start(ctx context.Context) (Run, error) { return m.start(ctx, false) }

func (m *Manager) start(ctx context.Context, hold bool) (Run, error) {
	// Reserve the slot before the DT calls, so concurrent starts cannot exceed the cap; the run (or a failure) frees it.
	m.mu.Lock()
	if m.reserved >= maxRuns {
		m.mu.Unlock()
		return Run{}, ErrTooManyRuns
	}
	m.reserved++
	m.mu.Unlock()
	run, err := m.create(ctx, hold)
	if err != nil {
		m.mu.Lock()
		m.reserved--
		m.mu.Unlock()
	}
	return run, err
}

func (m *Manager) create(ctx context.Context, hold bool) (Run, error) {
	tag := randHex(3)
	ref := fmt.Sprintf("%s%s-%s", OrderRefPrefix, time.Now().UTC().Format("20060102-150405"), tag)
	courierID, err := m.p.upsertCourier(ctx, "mc-sim-"+tag+"-a")
	if err != nil {
		return Run{}, err
	}
	deliveryID, trackingURL, err := m.p.createDelivery(ctx, ref, m.cfg.Pickup, m.cfg.Dropoff)
	if err != nil {
		return Run{}, err
	}
	// before anything else can carry the id into the event stream
	m.reg.Add(deliveryID)
	if err := m.p.assign(ctx, deliveryID, courierID); err != nil {
		return Run{}, err
	}
	code, err := m.p.linkCode(ctx, courierID)
	if err != nil {
		return Run{}, err
	}
	ph := &phone{g: m.gwc}
	if err := ph.exchange(ctx, code); err != nil {
		return Run{}, err
	}
	r := &run{hold: hold,
		m: m, tag: tag, ref: ref, deliveryID: deliveryID, trackingURL: trackingURL, phone: ph, courierA: courierID,
		route: line(m.cfg.Pickup, m.cfg.Dropoff, m.cfg.Fixes),
	}
	rctx, cancel := context.WithTimeout(m.ctx, runLifetime)
	r.cancel = cancel
	m.mu.Lock()
	m.runs[deliveryID] = r
	m.order = append(m.order, deliveryID)
	m.mu.Unlock()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer cancel()
		r.loop(rctx)
		m.mu.Lock()
		delete(m.runs, deliveryID)
		m.order = slices.DeleteFunc(m.order, func(id string) bool { return id == deliveryID })
		m.reserved--
		m.mu.Unlock()
	}()
	return Run{DeliveryID: deliveryID, OrderRef: ref, TrackingURL: trackingURL}, nil
}

func (m *Manager) get(id string) (*run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[id]
	if !ok {
		return nil, ErrUnknownRun
	}
	return r, nil
}

// StartHeld is Start with the pickup held until the first heal (scenario 4 needs an unpicked delivery).
func (m *Manager) StartHeld(ctx context.Context) (Run, error) { return m.start(ctx, true) }

// ErrTooLate is returned when scenario 4 is asked for a delivery that is already picked up.
var ErrTooLate = errors.New("sim: a delivery can be reassigned only before pickup")

// Break starts scenario 1-4 on a run; an active scenario is healed first.
func (m *Manager) Break(deliveryID string, scenario int) error {
	if scenario < 1 || scenario > 4 {
		return ErrBadScenario
	}
	r, err := m.get(deliveryID)
	if err != nil {
		return err
	}
	return r.breakIt(m.ctx, scenario)
}

// Heal ends the active scenario of a run.
func (m *Manager) Heal(deliveryID string) error {
	r, err := m.get(deliveryID)
	if err != nil {
		return err
	}
	return r.heal(m.ctx)
}

// Status lists the live runs, oldest first.
func (m *Manager) Status() []RunStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []RunStatus{}
	for _, id := range m.order {
		if r, ok := m.runs[id]; ok {
			out = append(out, RunStatus{DeliveryID: id, OrderRef: r.ref, Scenario: r.scenario(), PickedUp: r.pickedUp.Load()})
		}
	}
	return out
}

// Close stops every phone and viewer.
func (m *Manager) Close() {
	m.cancel()
	m.wg.Wait()
}
