// Package postgres implements the accounts app.Repo on PostgreSQL with
// sqlc-generated queries (package db). Events are written to the outbox in
// the same transaction as the state change.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/outbox"
	"github.com/raduolivas/dt/backend/services/accounts/internal/adapters/outbound/postgres/db"
	"github.com/raduolivas/dt/backend/services/accounts/internal/app"
	"github.com/raduolivas/dt/backend/services/accounts/internal/domain"
)

// Source is the envelope source of every event this service publishes.
const Source = "accounts"

// Repo is the Postgres unit of work.
type Repo struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

var _ app.Repo = (*Repo)(nil)

// New returns a Repo over pool; now stamps events' occurred_at (time.Now if nil).
func New(pool *pgxpool.Pool, now func() time.Time) *Repo {
	if now == nil {
		now = time.Now
	}
	return &Repo{pool: pool, now: now}
}

// InTx runs fn in one transaction, committed if fn returns nil.
func (r *Repo) InTx(ctx context.Context, fn func(ctx context.Context, tx app.Tx) error) error {
	return pgx.BeginFunc(ctx, r.pool, func(ptx pgx.Tx) error {
		return fn(ctx, &tx{tx: ptx, q: db.New(ptx), now: r.now})
	})
}

type tx struct {
	tx  pgx.Tx
	q   *db.Queries
	now func() time.Time
}

func (t *tx) InsertTenant(ctx context.Context, ten domain.Tenant) error {
	err := t.q.InsertTenant(ctx, db.InsertTenantParams{
		ID: ten.ID, Name: ten.Name, LiveEnabled: ten.LiveEnabled, CreatedAt: ten.CreatedAt,
		AllowedModels: nonNil(ten.Settings.AllowedModels), DefaultModel: ten.Settings.DefaultModel,
		PickupConfirmation: ten.Settings.PickupConfirmation, SettingsUpdatedAt: ten.Settings.UpdatedAt,
		DefaultPrepMinutes: prepToDB(ten.Settings.DefaultPrepMinutes),
	})
	if err != nil {
		return fmt.Errorf("insert tenant: %w", err)
	}
	return nil
}

func (t *tx) GetTenant(ctx context.Context, id string) (domain.Tenant, error) {
	row, err := t.q.GetTenant(ctx, id)
	if err != nil {
		return domain.Tenant{}, notFound("get tenant", err)
	}
	return domain.Tenant{
		ID: row.ID, Name: row.Name, LiveEnabled: row.LiveEnabled, CreatedAt: row.CreatedAt.UTC(),
		Settings: domain.TenantSettings{
			AllowedModels: row.AllowedModels, DefaultModel: row.DefaultModel,
			PickupConfirmation: row.PickupConfirmation, UpdatedAt: row.SettingsUpdatedAt.UTC(),
			DefaultPrepMinutes: prepFromDB(row.DefaultPrepMinutes),
		},
	}, nil
}

func (t *tx) SetTenantSettings(ctx context.Context, id string, s domain.TenantSettings) error {
	n, err := t.q.SetTenantSettings(ctx, db.SetTenantSettingsParams{
		ID: id, AllowedModels: nonNil(s.AllowedModels), DefaultModel: s.DefaultModel,
		PickupConfirmation: s.PickupConfirmation, SettingsUpdatedAt: s.UpdatedAt,
		DefaultPrepMinutes: prepToDB(s.DefaultPrepMinutes),
	})
	if err != nil {
		return fmt.Errorf("set tenant settings: %w", err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (t *tx) SetLiveEnabled(ctx context.Context, id string, v bool) error {
	n, err := t.q.SetLiveEnabled(ctx, db.SetLiveEnabledParams{ID: id, LiveEnabled: v})
	if err != nil {
		return fmt.Errorf("set live enabled: %w", err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (t *tx) InsertKey(ctx context.Context, k domain.APIKey) error {
	createdBy, err := marshalActor(k.CreatedBy)
	if err != nil {
		return err
	}
	err = t.q.InsertKey(ctx, db.InsertKeyParams{
		ID: k.ID, TenantID: k.TenantID, Environment: string(k.Env), Name: k.Name, Lookup: k.Lookup,
		DisplayPrefix: k.DisplayPrefix, Hash: k.Hash, Scopes: nonNil(k.Scopes), AllowedIps: nonNil(k.AllowedIPs),
		CreatedBy: createdBy, CreatedAt: k.CreatedAt, ExpiresAt: k.ExpiresAt, RevokedAt: k.RevokedAt,
	})
	if err != nil {
		return fmt.Errorf("insert api key: %w", err)
	}
	return nil
}

// GetKeyForUpdate locks the key row (SELECT … FOR UPDATE) so concurrent
// mutations of one key commit, and enqueue their events, in order.
func (t *tx) GetKeyForUpdate(ctx context.Context, tenantID, keyID string) (domain.APIKey, error) {
	row, err := t.q.GetKeyForUpdate(ctx, db.GetKeyForUpdateParams{ID: keyID, TenantID: tenantID})
	if err != nil {
		return domain.APIKey{}, notFound("get api key", err)
	}
	return toKey(row)
}

func (t *tx) UpdateKey(ctx context.Context, k domain.APIKey) error {
	n, err := t.q.UpdateKey(ctx, db.UpdateKeyParams{
		ID: k.ID, TenantID: k.TenantID, Name: k.Name, Scopes: nonNil(k.Scopes), AllowedIps: nonNil(k.AllowedIPs),
		ExpiresAt: k.ExpiresAt, RevokedAt: k.RevokedAt,
	})
	if err != nil {
		return fmt.Errorf("update api key: %w", err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (t *tx) ListKeys(ctx context.Context, tenantID string, env domain.Environment) ([]domain.APIKey, error) {
	rows, err := t.q.ListKeys(ctx, db.ListKeysParams{TenantID: tenantID, Environment: string(env)})
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	out := make([]domain.APIKey, 0, len(rows))
	for _, row := range rows {
		k, err := toKey(row)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, nil
}

// Publish enqueues the event on accounts.<eventType> in this transaction.
func (t *tx) Publish(ctx context.Context, eventType, tenantID string, env domain.Environment, payload proto.Message) error {
	e, err := outbox.NewEnvelope(eventType, tenantID, protoEnv(env), Source, nil, payload, t.now())
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, t.tx, outbox.SubjectFor(outbox.StreamAccounts, eventType), e)
}

func protoEnv(e domain.Environment) eventsv1.Environment {
	switch e {
	case domain.EnvTest:
		return eventsv1.Environment_ENVIRONMENT_TEST
	case domain.EnvLive:
		return eventsv1.Environment_ENVIRONMENT_LIVE
	}
	return eventsv1.Environment_ENVIRONMENT_UNSPECIFIED
}

func notFound(op string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	return fmt.Errorf("%s: %w", op, err)
}

// actor is the created_by JSON shape.
type actor struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Role string `json:"role"`
}

func marshalActor(a domain.Actor) ([]byte, error) {
	b, err := json.Marshal(actor{Type: a.Type, ID: a.ID, Role: string(a.Role)})
	if err != nil {
		return nil, fmt.Errorf("marshal created_by: %w", err)
	}
	return b, nil
}

func toKey(row db.ApiKey) (domain.APIKey, error) {
	var a actor
	if err := json.Unmarshal(row.CreatedBy, &a); err != nil {
		return domain.APIKey{}, fmt.Errorf("api key %s: created_by: %w", row.ID, err)
	}
	k := domain.APIKey{
		ID: row.ID, TenantID: row.TenantID, Name: row.Name, Lookup: row.Lookup, DisplayPrefix: row.DisplayPrefix,
		Env: domain.Environment(row.Environment), Hash: row.Hash, Scopes: row.Scopes, AllowedIPs: row.AllowedIps,
		CreatedBy: domain.Actor{Type: a.Type, ID: a.ID, Role: domain.Role(a.Role)},
		CreatedAt: row.CreatedAt.UTC(), ExpiresAt: utc(row.ExpiresAt), RevokedAt: utc(row.RevokedAt),
	}
	if len(k.AllowedIPs) == 0 {
		k.AllowedIPs = nil
	}
	if len(k.Scopes) == 0 {
		k.Scopes = nil
	}
	return k, nil
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// nonNil turns a nil slice into an empty one: the array columns are NOT NULL.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func prepToDB(p *int) pgtype.Int4 {
	if p == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: int32(*p), Valid: true}
}

func prepFromDB(p pgtype.Int4) *int {
	if !p.Valid {
		return nil
	}
	v := int(p.Int32)
	return &v
}
