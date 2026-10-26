package app

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// fakeStates stores states as JSON, like Redis.
type fakeStates struct {
	mu      sync.Mutex
	m       map[string][]byte
	active  map[string][]string // courier -> delivery ids
	scope   map[string]Scope    // courier -> scope
	beforeP func()              // runs before each Put (to simulate a racing writer)
	puts    int
	failPut map[string]error // delivery id -> Put error
}

func newFakeStates() *fakeStates {
	return &fakeStates{m: map[string][]byte{}, active: map[string][]string{}, scope: map[string]Scope{}}
}

func (f *fakeStates) Get(_ context.Context, id string) (domain.State, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.m[id]
	if !ok {
		return domain.State{}, false, nil
	}
	var st domain.State
	err := json.Unmarshal(b, &st)
	return st, true, err
}

func (f *fakeStates) Put(_ context.Context, st domain.State, prev int64) (bool, error) {
	if f.beforeP != nil {
		hook := f.beforeP
		f.beforeP = nil
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failPut[st.DeliveryID]; err != nil {
		return false, err
	}
	f.puts++
	var cur int64
	if b, ok := f.m[st.DeliveryID]; ok {
		var old domain.State
		_ = json.Unmarshal(b, &old)
		cur = old.Version
	}
	if cur != prev || cur >= st.Version {
		return false, nil
	}
	b, err := json.Marshal(st)
	f.m[st.DeliveryID] = b
	return true, err
}

func (f *fakeStates) put(st domain.State) {
	b, _ := json.Marshal(st)
	f.mu.Lock()
	f.m[st.DeliveryID] = b
	f.mu.Unlock()
}

func (f *fakeStates) flush() {
	f.mu.Lock()
	f.m = map[string][]byte{}
	f.mu.Unlock()
}

func (f *fakeStates) ActiveDeliveriesForCourier(_ context.Context, tenant, env, courier string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sc, ok := f.scope[courier]
	if !ok || sc != (Scope{tenant, env}) {
		return nil, ErrCourierNotFound
	}
	return slices.Clone(f.active[courier]), nil
}

func (f *fakeStates) SetCourierActive(_ context.Context, courier, id string, active bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := slices.DeleteFunc(f.active[courier], func(s string) bool { return s == id })
	if active {
		ids = append(ids, id)
	}
	f.active[courier] = ids
	return nil
}

// fakeRead rebuilds states from records.
type fakeRead struct {
	mu       sync.Mutex
	recs     map[string]DeliveryRecord
	couriers map[string]domain.Courier
	trail    map[string][]trailFix
	proofs   []ProofPoint
	prep     map[string]*int
}

func newFakeRead() *fakeRead {
	return &fakeRead{recs: map[string]DeliveryRecord{}, couriers: map[string]domain.Courier{},
		trail: map[string][]trailFix{}, prep: map[string]*int{}}
}

func (f *fakeRead) LoadState(_ context.Context, sc Scope, id string) (domain.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.recs[id]
	if !ok || rec.TenantID != sc.TenantID || rec.Env != sc.Env {
		return domain.State{}, ErrNotFound
	}
	var st domain.State
	var c *domain.Courier
	if rec.CourierID != nil {
		cc := f.couriers[*rec.CourierID]
		c = &cc
	}
	ApplyRecord(&st, rec, c)
	st.DefaultPrepMinutes = f.prep[sc.TenantID]
	for _, x := range f.trail[id] {
		if rec.CourierID != nil && x.courier == *rec.CourierID {
			l := x.f
			st.LastFix = &l
		}
	}
	return st, nil
}

type trailFix struct {
	courier string
	f       domain.Fix
}

func (f *fakeRead) addTrail(id, courier string, fx domain.Fix) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trail[id] = append(f.trail[id], trailFix{courier, fx})
}

func (f *fakeRead) RecentTrail(_ context.Context, _ Scope, id, courier string, from, to time.Time) ([]domain.Fix, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Fix
	for _, x := range f.trail[id] {
		if x.courier == courier && !x.f.T.Before(from) && !x.f.T.After(to) {
			out = append(out, x.f)
		}
	}
	return out, nil
}

func (f *fakeRead) Delivery(_ context.Context, id string) (DeliveryRecord, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.recs[id]
	return r, ok, nil
}

func (f *fakeRead) PutDelivery(_ context.Context, r DeliveryRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recs[r.ID] = r
	return nil
}

func (f *fakeRead) Courier(_ context.Context, id string) (domain.Courier, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.couriers[id]
	return c, ok, nil
}

func (f *fakeRead) PutCourier(_ context.Context, c domain.Courier) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.couriers[c.ID] = c
	return nil
}

func (f *fakeRead) CourierDeliveries(_ context.Context, cid string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for id, r := range f.recs {
		if r.CourierID != nil && *r.CourierID == cid && r.FinalAt == nil {
			out = append(out, id)
		}
	}
	return out, nil
}

func (f *fakeRead) PutProofPoint(_ context.Context, p ProofPoint) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.proofs = append(f.proofs, p)
	return nil
}

type fakeTrail struct {
	mu   sync.Mutex
	rows []TrailRow
}

func (f *fakeTrail) Append(rows []TrailRow) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, rows...)
}

func (f *fakeTrail) forDelivery(id string) []TrailRow {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []TrailRow
	for _, r := range f.rows {
		if r.DeliveryID == id {
			out = append(out, r)
		}
	}
	return out
}

type published struct {
	id string
	s  domain.TrackingSnapshot
}

type fakePub struct {
	mu  sync.Mutex
	got []published
}

func (f *fakePub) Snapshot(_ context.Context, id string, s domain.TrackingSnapshot) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, published{id, s})
	return nil
}

func (f *fakePub) forDelivery(id string) []domain.TrackingSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.TrackingSnapshot
	for _, p := range f.got {
		if p.id == id {
			out = append(out, p.s)
		}
	}
	return out
}

// fakeRouter routes in a straight line between its points at mps metres per
// second and counts its calls.
type fakeRouter struct {
	mu       sync.Mutex
	calls    int
	profiles []string
	err      error
	mps      float64
}

func (r *fakeRouter) Route(_ context.Context, profile string, pts ...domain.LatLng) ([]domain.LatLng, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.profiles = append(r.profiles, profile)
	if r.err != nil {
		return nil, 0, r.err
	}
	mps := r.mps
	if mps == 0 {
		mps = 5
	}
	return slices.Clone(pts), int(domain.PathLengthM(pts) / mps), nil
}

func (r *fakeRouter) Match(_ context.Context, _ string, fixes []domain.Fix) ([]domain.LatLng, error) {
	out := make([]domain.LatLng, len(fixes))
	for i, f := range fixes {
		out[i] = f.Pos
	}
	return out, nil
}

func (r *fakeRouter) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

type enqueued struct {
	typ     string
	subject Subject
	msg     proto.Message
}

type fakeEvents struct {
	mu   sync.Mutex
	got  []enqueued
	fail error // when set, every Enqueue fails with it
	hang bool  // when set, Enqueue blocks until its context ends
}

func (f *fakeEvents) Enqueue(ctx context.Context, typ, _, _ string, subj Subject, msg proto.Message) error {
	if f.hang {
		<-ctx.Done()
		return ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.got = append(f.got, enqueued{typ, subj, msg})
	return nil
}

func (f *fakeEvents) ofType(typ string) []enqueued {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []enqueued
	for _, e := range f.got {
		if e.typ == typ {
			out = append(out, e)
		}
	}
	return out
}

type fakeSampler struct {
	mu   sync.Mutex
	seen map[string]bool
}

func (f *fakeSampler) Allow(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.seen == nil {
		f.seen = map[string]bool{}
	}
	if f.seen[id] {
		return false, nil
	}
	f.seen[id] = true
	return true, nil
}
