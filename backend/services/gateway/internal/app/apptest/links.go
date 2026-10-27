// Package apptest holds in-memory implementations of the app ports for tests
// of the app and its adapters.
package apptest

import (
	"context"
	"sync"
	"time"

	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

// MemLinks is an in-memory app.LinkStore with the Postgres store's rules:
// at most one unrevoked link per delivery, and the first expiry wins.
type MemLinks struct {
	mu      sync.Mutex
	links   map[string]domain.TrackingLink
	hashes  map[string][]byte // link id → token hash
	expires map[string]time.Time
	fail    error
}

var _ app.LinkStore = (*MemLinks)(nil)

// NewMemLinks returns an empty store.
func NewMemLinks() *MemLinks {
	return &MemLinks{links: map[string]domain.TrackingLink{}, hashes: map[string][]byte{}, expires: map[string]time.Time{}}
}

// Fail makes every call return err (nil: work again).
func (m *MemLinks) Fail(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fail = err
}

// Hash returns the stored token hash of link id.
func (m *MemLinks) Hash(id string) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hashes[id]
}

// Replace implements app.LinkStore.
func (m *MemLinks) Replace(_ context.Context, l domain.TrackingLink, hash []byte) (domain.TrackingLink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return domain.TrackingLink{}, m.fail
	}
	for id, old := range m.links {
		if old.DeliveryID == l.DeliveryID && old.RevokedAt == nil {
			at := l.CreatedAt
			old.RevokedAt = &at
			m.links[id] = old
		}
	}
	if exp, ok := m.expires[l.DeliveryID]; ok {
		l.ExpiresAt = &exp
	}
	m.links[l.ID] = l
	m.hashes[l.ID] = append([]byte(nil), hash...)
	return l, nil
}

// FindByHash implements app.LinkStore.
func (m *MemLinks) FindByHash(_ context.Context, hash []byte) (domain.TrackingLink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return domain.TrackingLink{}, m.fail
	}
	for id, h := range m.hashes {
		if string(h) == string(hash) {
			return m.links[id], nil
		}
	}
	return domain.TrackingLink{}, app.ErrNotFound
}

// Get implements app.LinkStore.
func (m *MemLinks) Get(_ context.Context, id string) (domain.TrackingLink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return domain.TrackingLink{}, m.fail
	}
	l, ok := m.links[id]
	if !ok {
		return domain.TrackingLink{}, app.ErrNotFound
	}
	return l, nil
}

// Expire implements app.LinkStore.
func (m *MemLinks) Expire(_ context.Context, deliveryID string, at time.Time) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return nil, m.fail
	}
	if _, ok := m.expires[deliveryID]; !ok {
		m.expires[deliveryID] = at
	}
	exp := m.expires[deliveryID]
	var ids []string
	for id, l := range m.links {
		if l.DeliveryID == deliveryID && l.RevokedAt == nil && l.ExpiresAt == nil {
			l.ExpiresAt = &exp
			m.links[id] = l
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// RevokedSince implements app.LinkStore.
func (m *MemLinks) RevokedSince(_ context.Context, deliveryID string, since time.Time) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return nil, m.fail
	}
	var ids []string
	for id, l := range m.links {
		if l.DeliveryID == deliveryID && l.RevokedAt != nil && !l.RevokedAt.Before(since) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// MemRevoker records revoked link ids.
type MemRevoker struct {
	mu      sync.Mutex
	revoked []string
	fail    error
}

var _ app.Revoker = (*MemRevoker)(nil)

// Revoke implements app.Revoker.
func (r *MemRevoker) Revoke(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	r.revoked = append(r.revoked, id)
	return nil
}

// Revoked returns the ids revoked so far.
func (r *MemRevoker) Revoked() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.revoked...)
}

// Reset forgets the ids revoked so far.
func (r *MemRevoker) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.revoked = nil
}

// Fail makes Revoke return err (nil: work again).
func (r *MemRevoker) Fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fail = err
}
