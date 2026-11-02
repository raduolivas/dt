package apptest

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

// CourierEvent is a courier.session_* event MemCourierSessions recorded.
type CourierEvent struct {
	Type      string // "courier.session_started" | "courier.session_ended"
	SessionID string
	CourierID string
	Mode      domain.SessionMode
	Reason    domain.EndReason
}

// MemCourierSessions is an in-memory app.CourierSessionStore with the
// Postgres store's rules: at most one active and one pending_link session
// per courier, unique link hashes, superseded refresh hashes kept, and an
// Update that rolls back when fn fails.
type MemCourierSessions struct {
	mu     sync.Mutex
	rows   map[string]domain.CourierSession
	used   map[string]string // superseded refresh hash → session id
	events []CourierEvent
}

var _ app.CourierSessionStore = (*MemCourierSessions)(nil)

// NewMemCourierSessions returns an empty store.
func NewMemCourierSessions() *MemCourierSessions {
	return &MemCourierSessions{rows: map[string]domain.CourierSession{}, used: map[string]string{}}
}

// Events returns the events committed so far.
func (m *MemCourierSessions) Events() []CourierEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.events)
}

// Get implements app.CourierSessionStore.
func (m *MemCourierSessions) Get(_ context.Context, id string) (domain.CourierSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.rows[id]
	if !ok {
		return domain.CourierSession{}, app.ErrNotFound
	}
	return s, nil
}

// FindByLinkHash implements app.CourierSessionStore.
func (m *MemCourierSessions) FindByLinkHash(_ context.Context, h []byte) (domain.CourierSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.rows {
		if s.LinkCodeHash != nil && string(s.LinkCodeHash) == string(h) {
			return s, nil
		}
	}
	return domain.CourierSession{}, app.ErrNotFound
}

// FindByRefreshHash implements app.CourierSessionStore.
func (m *MemCourierSessions) FindByRefreshHash(_ context.Context, h []byte) (domain.CourierSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.used[string(h)]; ok {
		return m.rows[id], nil
	}
	for _, s := range m.rows {
		if s.RefreshHash != nil && string(s.RefreshHash) == string(h) {
			return s, nil
		}
	}
	return domain.CourierSession{}, app.ErrNotFound
}

// Update implements app.CourierSessionStore. One Update runs at a time.
func (m *MemCourierSessions) Update(_ context.Context, tenantID string, env domain.Environment, courierID string, fn func(app.CourierSessionTx) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	tx := &memCourierTx{m: m, tenantID: tenantID, env: env, courierID: courierID,
		rows: maps.Clone(m.rows), used: maps.Clone(m.used)}
	if err := fn(tx); err != nil {
		return err
	}
	m.rows, m.used, m.events = tx.rows, tx.used, append(m.events, tx.events...)
	return nil
}

// Touch implements app.CourierSessionStore.
func (m *MemCourierSessions) Touch(_ context.Context, id string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.rows[id]
	if !ok {
		return nil
	}
	if s.LastSeenAt == nil || now.Sub(*s.LastSeenAt) > app.CourierTouchInterval {
		s.LastSeenAt = &now
		m.rows[id] = s
	}
	return nil
}

type memCourierTx struct {
	m                   *MemCourierSessions
	tenantID, courierID string
	env                 domain.Environment
	rows                map[string]domain.CourierSession
	used                map[string]string
	events              []CourierEvent
}

func (t *memCourierTx) Lock(id string) (domain.CourierSession, error) {
	s, ok := t.rows[id]
	if !ok {
		return domain.CourierSession{}, app.ErrNotFound
	}
	if s.TenantID != t.tenantID || s.Env != t.env || s.CourierID != t.courierID {
		return domain.CourierSession{}, fmt.Errorf("apptest: session %s is not the locked courier's", id)
	}
	return s, nil
}

func (t *memCourierTx) Current(st domain.SessionStatus) (domain.CourierSession, error) {
	for _, s := range t.rows {
		if s.TenantID == t.tenantID && s.Env == t.env && s.CourierID == t.courierID && s.State == st {
			return s, nil
		}
	}
	return domain.CourierSession{}, app.ErrNotFound
}

func (t *memCourierTx) check(s domain.CourierSession) error {
	if s.TenantID != t.tenantID || s.Env != t.env || s.CourierID != t.courierID {
		return fmt.Errorf("apptest: session %s is not the locked courier's", s.ID)
	}
	for _, o := range t.rows {
		if o.ID == s.ID {
			continue
		}
		if o.TenantID == s.TenantID && o.Env == s.Env && o.CourierID == s.CourierID && o.State == s.State &&
			s.State != domain.SessionEnded {
			return fmt.Errorf("apptest: courier %s already has a %s session", s.CourierID, s.State)
		}
		if s.LinkCodeHash != nil && string(o.LinkCodeHash) == string(s.LinkCodeHash) {
			return fmt.Errorf("apptest: duplicate link hash")
		}
	}
	return nil
}

func (t *memCourierTx) Insert(s domain.CourierSession) error {
	if _, ok := t.rows[s.ID]; ok {
		return fmt.Errorf("apptest: duplicate session %s", s.ID)
	}
	if err := t.check(s); err != nil {
		return err
	}
	t.rows[s.ID] = s
	return nil
}

func (t *memCourierTx) Save(s domain.CourierSession) error {
	old, ok := t.rows[s.ID]
	if !ok {
		return app.ErrNotFound
	}
	if err := t.check(s); err != nil {
		return err
	}
	if old.RefreshHash != nil && string(old.RefreshHash) != string(s.RefreshHash) {
		t.used[string(old.RefreshHash)] = s.ID
	}
	t.rows[s.ID] = s
	return nil
}

func (t *memCourierTx) Started(s domain.CourierSession) error {
	t.events = append(t.events, CourierEvent{Type: "courier.session_started", SessionID: s.ID, CourierID: s.CourierID, Mode: s.Mode})
	return nil
}

func (t *memCourierTx) Ended(s domain.CourierSession) error {
	t.events = append(t.events, CourierEvent{Type: "courier.session_ended", SessionID: s.ID, CourierID: s.CourierID, Reason: s.EndReason})
	return nil
}

// MemCourierDeny is an in-memory app.CourierDenyList.
type MemCourierDeny struct {
	mu     sync.Mutex
	denied map[string]bool
	fail   error
}

var _ app.CourierDenyList = (*MemCourierDeny)(nil)

// Deny implements app.CourierDenyList.
func (d *MemCourierDeny) Deny(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.fail != nil {
		return d.fail
	}
	if d.denied == nil {
		d.denied = map[string]bool{}
	}
	d.denied[id] = true
	return nil
}

// Denied implements app.CourierDenyList.
func (d *MemCourierDeny) Denied(_ context.Context, id string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.fail != nil {
		return false, d.fail
	}
	return d.denied[id], nil
}

// Fail makes every call return err (nil: work again).
func (d *MemCourierDeny) Fail(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.fail = err
}

// MemCouriers is an app.CourierLookup over a fixed set of couriers.
type MemCouriers struct {
	known map[string]bool // tenant/env/courier
}

var _ app.CourierLookup = (*MemCouriers)(nil)

// Add makes courierID exist in tenantID and env.
func (c *MemCouriers) Add(tenantID string, env domain.Environment, courierID string) {
	if c.known == nil {
		c.known = map[string]bool{}
	}
	c.known[tenantID+"/"+string(env)+"/"+courierID] = true
}

// CourierExists implements app.CourierLookup.
func (c *MemCouriers) CourierExists(_ context.Context, p domain.Principal, courierID string) error {
	if !c.known[p.TenantID+"/"+string(p.Env)+"/"+courierID] {
		return app.ErrCourierNotFound
	}
	return nil
}

// MemAudit is an in-memory app.AuditStore.
type MemAudit struct {
	mu      sync.Mutex
	entries []app.AuditEntry
}

var _ app.AuditStore = (*MemAudit)(nil)

// Record implements app.AuditStore.
func (a *MemAudit) Record(_ context.Context, e app.AuditEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, e)
	return nil
}

// Entries returns the recorded entries.
func (a *MemAudit) Entries() []app.AuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.entries)
}

var _ app.CourierTokenHasher = Hasher{}

// HashCourierRefresh implements app.CourierTokenHasher, as keyhash does.
func (h Hasher) HashCourierRefresh(token string) []byte { return h.Hash("courier-refresh:" + token) }

// HashCourierLink implements app.CourierTokenHasher, as keyhash does.
func (h Hasher) HashCourierLink(code string) []byte { return h.Hash("courier-link:" + code) }
