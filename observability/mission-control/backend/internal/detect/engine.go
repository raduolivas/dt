package detect

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/config"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
)

func (f Finding) key() string { return f.Detector + "|" + f.Hop + "|" + f.DeliveryID + "|" + f.Reason }

// activeRec is a finding that has been stored and not yet cleared.
type activeRec struct {
	f  Finding
	ev store.Event // the stored event, for tenant/env/origin when it is cleared
}

// Engine runs the rules and keeps the dedup state.
type Engine struct {
	src        Source
	th         config.Thresholds
	now        func() time.Time
	emit       func(Finding)
	onInserted func(ctx context.Context, e store.Event) error

	loaded    bool
	active    map[string]activeRec
	prevDrops map[string]int64
	dropAt    map[string]time.Time
}

// NewEngine returns an Engine. now, emit and onInserted may be nil.
func NewEngine(src Source, th config.Thresholds, now func() time.Time, emit func(Finding),
	onInserted func(ctx context.Context, e store.Event) error) *Engine {
	if now == nil {
		now = time.Now
	}
	return &Engine{src: src, th: th, now: now, emit: emit, onInserted: onInserted,
		active: map[string]activeRec{}, prevDrops: map[string]int64{}, dropAt: map[string]time.Time{}}
}

// load rebuilds the active set from the stored findings, so a restart neither
// repeats a finding nor forgets to clear one.
func (e *Engine) load(ctx context.Context) error {
	evs, err := e.src.Active(ctx)
	if err != nil {
		return err
	}
	for _, ev := range evs {
		var a struct {
			Rule     string  `json:"rule"`
			Evidence []int64 `json:"evidence"`
		}
		_ = json.Unmarshal(ev.Attrs, &a)
		f := Finding{Detector: a.Rule, Hop: ev.Target, DeliveryID: ev.DeliveryID, Reason: ev.Reason,
			Severity: ev.Outcome, Since: ev.OccurredAt, Evidence: a.Evidence}
		e.active[f.key()] = activeRec{f: f, ev: ev}
	}
	e.loaded = true
	return nil
}

func (e *Engine) input(ctx context.Context, now time.Time) (*input, error) {
	d := e.th.Detect
	in := &input{now: now, th: e.th, status: map[string]ev{}, dropAt: e.dropAt}
	win, err := e.src.Window(ctx, now.Add(-d.Window))
	if err != nil {
		return nil, err
	}
	in.win = wrap(win)
	sts, err := e.src.Statuses(ctx, now.Add(-d.StatusLookback))
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, s := range wrap(sts) {
		in.status[s.DeliveryID] = s
		ids = append(ids, s.DeliveryID)
	}
	if in.fixes, err = e.src.LastFixes(ctx, ids); err != nil {
		return nil, err
	}
	if in.fixes == nil {
		in.fixes = map[string]store.Fix{}
	}
	h, err := e.src.Health(ctx, now.Add(-d.Window))
	if err != nil {
		return nil, err
	}
	in.health = wrap(h)
	if in.self, err = e.src.Self(ctx); err != nil {
		return nil, err
	}
	for _, r := range in.self {
		if r.Drops > e.prevDrops[r.Source] {
			e.dropAt[r.Source] = now // a counter first seen above zero is a drop now
		}
		e.prevDrops[r.Source] = r.Drops
	}
	return in, nil
}

// Tick evaluates every rule once and stores what changed: a finding that is
// new or changed severity, and a "cleared" for one that stopped holding. A
// source error aborts the tick without clearing anything.
func (e *Engine) Tick(ctx context.Context) error {
	if !e.loaded {
		if err := e.load(ctx); err != nil {
			return fmt.Errorf("detect: load active findings: %w", err)
		}
	}
	now := e.now().UTC()
	in, err := e.input(ctx, now)
	if err != nil {
		return fmt.Errorf("detect: read: %w", err)
	}
	current := map[string]Finding{}
	var order []string
	for _, rule := range rules {
		for _, f := range rule(in) {
			if _, dup := current[f.key()]; !dup {
				current[f.key()] = f
				order = append(order, f.key())
			}
		}
	}
	sort.Strings(order)

	var out []Finding
	var evs []store.Event
	for _, k := range order {
		f := current[k]
		if prev, ok := e.active[k]; ok && prev.f.Severity == f.Severity {
			continue
		}
		out = append(out, f)
		evs = append(evs, e.event(in, f, store.Event{}, now))
	}
	var gone []string
	for k := range e.active {
		if _, ok := current[k]; !ok {
			gone = append(gone, k)
		}
	}
	sort.Strings(gone)
	for _, k := range gone {
		prev := e.active[k]
		f := prev.f
		f.Severity, f.Since = SevCleared, now
		out = append(out, f)
		evs = append(evs, e.event(in, f, prev.ev, now))
	}
	if len(evs) == 0 {
		return nil
	}
	if err := e.src.Insert(ctx, evs); err != nil {
		return fmt.Errorf("detect: store findings: %w", err)
	}
	for i, f := range out {
		if f.Severity == SevCleared {
			delete(e.active, f.key())
		} else {
			e.active[f.key()] = activeRec{f: f, ev: evs[i]}
		}
		if e.onInserted != nil {
			_ = e.onInserted(ctx, evs[i])
		}
		if e.emit != nil {
			e.emit(f)
		}
	}
	return nil
}

// event builds the kind=detector row of a finding. Source "mc", Target the
// hop (the API groups findings by target), Outcome the severity. Tenant,
// environment and origin come from the delivery's own events, else from the
// first evidence event, else from prev (the event a clear replaces).
func (e *Engine) event(in *input, f Finding, prev store.Event, now time.Time) store.Event {
	ev := store.Event{ObservedAt: now, OccurredAt: now, Kind: "detector", Source: "mc", Target: f.Hop,
		Outcome: f.Severity, Reason: f.Reason, DeliveryID: f.DeliveryID, Origin: "live",
		TenantID: prev.TenantID, Env: prev.Env, OrderRef: prev.OrderRef}
	if prev.Origin != "" {
		ev.Origin = prev.Origin
	}
	var from *store.Event
	if st, ok := in.status[f.DeliveryID]; ok && f.DeliveryID != "" {
		from = &st.Event
	} else if len(f.Evidence) > 0 {
		for i := range in.win {
			if in.win[i].ID == f.Evidence[0] {
				from = &in.win[i].Event
				break
			}
		}
	}
	if from != nil {
		ev.TenantID, ev.Env, ev.Origin = from.TenantID, from.Env, from.Origin
	}
	attrs := map[string]any{}
	for k, v := range f.Attrs {
		attrs[k] = v
	}
	evidence := f.Evidence
	if evidence == nil {
		evidence = []int64{}
	}
	attrs["rule"], attrs["severity"], attrs["evidence"] = f.Detector, f.Severity, evidence
	attrs["since"] = f.Since.UTC().Format(time.RFC3339)
	ev.Attrs, _ = json.Marshal(attrs)
	return ev
}

// Options configure Run.
type Options struct {
	Thresholds config.Thresholds
	// Now is the clock (tests inject a fake one); default time.Now.
	Now func() time.Time
	// OnInserted is called per stored finding (the live stream hook).
	OnInserted func(ctx context.Context, e store.Event) error
	Log        *slog.Logger
}

// Run evaluates the rules now and then every `every` (default 5 s) until ctx
// ends. emit is called for each stored finding, including clears.
func Run(ctx context.Context, st *store.Store, every time.Duration, emit func(Finding), opt Options) {
	if every <= 0 {
		every = 5 * time.Second
	}
	log := opt.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	eng := NewEngine(storeSource{st}, opt.Thresholds, opt.Now, emit, opt.OnInserted)
	tick := func() {
		if err := eng.Tick(ctx); err != nil && ctx.Err() == nil {
			log.Warn("detectors: tick failed", "error", err.Error())
		}
	}
	tick()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}
