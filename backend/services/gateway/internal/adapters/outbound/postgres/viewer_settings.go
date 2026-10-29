package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/raduolivas/dt/backend/services/gateway/internal/adapters/outbound/postgres/db"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

// ViewerSettingsStore is the tenant_viewer_settings read model. Put is an
// idempotent upsert guarded by the event time, so it needs no transaction
// shared with the consumer's inbox: a redelivery changes nothing.
type ViewerSettingsStore struct {
	q *db.Queries
}

var _ app.ViewerSettingsStore = (*ViewerSettingsStore)(nil)

// NewViewerSettingsStore returns a ViewerSettingsStore over pool.
func NewViewerSettingsStore(pool *pgxpool.Pool) *ViewerSettingsStore {
	return &ViewerSettingsStore{q: db.New(pool)}
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

// Put implements app.ViewerSettingsStore.
func (s *ViewerSettingsStore) Put(ctx context.Context, tenantID string, v domain.ViewerSettings, logo []byte, at time.Time) error {
	origins := v.EmbedOrigins
	if origins == nil {
		origins = []string{}
	}
	p := db.PutViewerSettingsParams{TenantID: tenantID, EmbedOrigins: origins, UpdatedAt: at}
	if b := v.Branding; b != nil {
		p.DisplayName, p.PrimaryColor, p.Theme = text(b.DisplayName), text(b.PrimaryColor), text(b.Theme)
		p.LogoContentType, p.LogoSha256 = text(b.LogoContentType), text(b.LogoSHA256)
		if len(logo) > 0 {
			p.Logo = logo
		}
	}
	if err := s.q.PutViewerSettings(ctx, p); err != nil {
		return fmt.Errorf("put viewer settings: %w", err)
	}
	return nil
}

// Get implements app.ViewerSettingsStore.
func (s *ViewerSettingsStore) Get(ctx context.Context, tenantID string) (domain.ViewerSettings, bool, error) {
	r, err := s.q.GetViewerSettings(ctx, tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ViewerSettings{}, false, nil
	}
	if err != nil {
		return domain.ViewerSettings{}, false, fmt.Errorf("get viewer settings: %w", err)
	}
	out := domain.ViewerSettings{EmbedOrigins: r.EmbedOrigins}
	if len(out.EmbedOrigins) == 0 {
		out.EmbedOrigins = nil
	}
	if r.DisplayName.Valid || r.PrimaryColor.Valid || r.Theme.Valid || r.LogoSha256.Valid {
		out.Branding = &domain.Branding{DisplayName: r.DisplayName.String, PrimaryColor: r.PrimaryColor.String,
			Theme: r.Theme.String, LogoSHA256: r.LogoSha256.String, LogoContentType: r.LogoContentType.String}
	}
	return out, true, nil
}

// Logo implements app.ViewerSettingsStore.
func (s *ViewerSettingsStore) Logo(ctx context.Context, tenantID, sha string) ([]byte, string, error) {
	r, err := s.q.GetViewerLogo(ctx, db.GetViewerLogoParams{TenantID: tenantID, LogoSha256: text(sha)})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", app.ErrNotFound
	}
	if err != nil {
		return nil, "", fmt.Errorf("get viewer logo: %w", err)
	}
	return r.Logo, r.LogoContentType.String, nil
}
