package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

// ViewerSettingsStore keeps the per-tenant settings of the hosted tracking
// page (Postgres), fed by the accounts tenant.settings_set event.
type ViewerSettingsStore interface {
	// Get returns the tenant's settings; false when it has none.
	Get(ctx context.Context, tenantID string) (domain.ViewerSettings, bool, error)
	// Put stores the settings as of at, with the logo that s.Branding names
	// (nil without one). It changes nothing when at is not newer than the
	// stored settings' time.
	Put(ctx context.Context, tenantID string, s domain.ViewerSettings, logo []byte, at time.Time) error
	// Logo returns the tenant's logo with the given hex SHA-256 and its
	// content type, or ErrNotFound.
	Logo(ctx context.Context, tenantID, sha string) ([]byte, string, error)
}

// TrackPage decides how the hosted page may be framed.
type TrackPage struct {
	Links    *TrackingLinks
	Settings ViewerSettingsStore
}

// FrameAncestors returns the frame-ancestors value for the page opened with
// token: the tenant's embed origins when the token names a link (active,
// expired or revoked alike), else 'none'. It never says which case applied.
// An error means a store failed.
func (p *TrackPage) FrameAncestors(ctx context.Context, token string) (string, error) {
	tenantID, ok, err := p.Links.TenantOf(ctx, token)
	if err != nil {
		return "", err
	}
	if !ok {
		return domain.FrameAncestors(nil), nil
	}
	s, _, err := p.Settings.Get(ctx, tenantID)
	if err != nil {
		return "", fmt.Errorf("get viewer settings: %w", err)
	}
	return domain.FrameAncestors(s.EmbedOrigins), nil
}

// Logo returns a tenant logo and its content type, or ErrNotFound.
func (p *TrackPage) Logo(ctx context.Context, tenantID, sha string) ([]byte, string, error) {
	b, ct, err := p.Settings.Logo(ctx, tenantID, sha)
	if errors.Is(err, ErrNotFound) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", fmt.Errorf("get logo: %w", err)
	}
	return b, ct, nil
}
