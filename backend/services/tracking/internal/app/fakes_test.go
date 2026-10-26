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
	trail    map[string][]domain.Fix
	proofs   []ProofPoint
	prep     map[string]*int
}

func newFakeRead() *fakeRead {
	return &fakeRead{recs: map[string]DeliveryRecord{}, couriers: map[string]domain.Courier{},
		trail: map[string][]domain.Fix{}, prep: map[string]*int{}}
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
	if tr := f.trail[id]; len(tr) > 0 {
		l := tr[len(tr)-1]
		st.LastFix = &l
	}
	return st, nil
}

func (f *fakeRead) RecentTrail(_ context.Context, _ Scope, id string, from, to time.Time) ([]domain.Fix, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Fix
	for _, x := range f.trail[id] {
		if !x.T.Before(from) && !x.T.After(to) {
			out = append(out, x)
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

type enqueued struct {
	typ     string
	subject Subject
	msg     proto.Message
}

type fakeEvents struct {
	mu  sync.Mutex
	got []enqueued
}

func (f *fakeEvents) Enqueue(_ context.Context, typ, _, _ string, subj Subject, msg proto.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
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
