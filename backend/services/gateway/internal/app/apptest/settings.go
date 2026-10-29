package apptest

import (
	"context"
	"sync"
	"time"

	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

// MemSettings is an in-memory app.ViewerSettingsStore with the Postgres
// store's ordering rule: an event not newer than the stored one is ignored.
type MemSettings struct {
	mu   sync.Mutex
	rows map[string]memSettingsRow
	fail error
}

type memSettingsRow struct {
	s    domain.ViewerSettings
	logo []byte
	at   time.Time
}

var _ app.ViewerSettingsStore = (*MemSettings)(nil)

// NewMemSettings returns an empty store.
func NewMemSettings() *MemSettings { return &MemSettings{rows: map[string]memSettingsRow{}} }

// Fail makes every call return err (nil: work again).
func (m *MemSettings) Fail(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fail = err
}

// Get implements app.ViewerSettingsStore.
func (m *MemSettings) Get(_ context.Context, tenantID string) (domain.ViewerSettings, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return domain.ViewerSettings{}, false, m.fail
	}
	r, ok := m.rows[tenantID]
	return r.s, ok, nil
}

// Put implements app.ViewerSettingsStore.
func (m *MemSettings) Put(_ context.Context, tenantID string, s domain.ViewerSettings, logo []byte, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return m.fail
	}
	if old, ok := m.rows[tenantID]; ok && !old.at.Before(at) {
		return nil
	}
	m.rows[tenantID] = memSettingsRow{s: s, logo: append([]byte(nil), logo...), at: at}
	return nil
}

// Logo implements app.ViewerSettingsStore.
func (m *MemSettings) Logo(_ context.Context, tenantID, sha string) ([]byte, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return nil, "", m.fail
	}
	r, ok := m.rows[tenantID]
	if !ok || r.s.Branding == nil || r.s.Branding.LogoSHA256 != sha || r.logo == nil {
		return nil, "", app.ErrNotFound
	}
	return r.logo, r.s.Branding.LogoContentType, nil
}
