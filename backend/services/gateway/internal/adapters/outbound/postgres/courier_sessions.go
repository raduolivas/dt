package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	"github.com/raduolivas/dt/backend/pkg/outbox"
	"github.com/raduolivas/dt/backend/services/gateway/internal/adapters/outbound/postgres/db"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

// EventSource is the envelope source of every event the gateway publishes.
const EventSource = "gateway"

// Event types of the courier session events (subjects gateway.<type>).
const (
	EventCourierSessionStarted = "courier.session_started"
	EventCourierSessionEnded   = "courier.session_ended"
)

// CourierSessionStore keeps courier sessions in courier_sessions and every
// superseded refresh token hash in courier_refresh_used. Update holds a
// transaction-scoped advisory lock per courier, and rows it changes are read
// FOR UPDATE, so a link code or refresh token is used once under races.
type CourierSessionStore struct {
	pool *pgxpool.Pool
	q    *db.Queries
}

var _ app.CourierSessionStore = (*CourierSessionStore)(nil)

// NewCourierSessionStore returns a CourierSessionStore over pool.
func NewCourierSessionStore(pool *pgxpool.Pool) *CourierSessionStore {
	return &CourierSessionStore{pool: pool, q: db.New(pool)}
}

// Get implements app.CourierSessionStore.
func (s *CourierSessionStore) Get(ctx context.Context, id string) (domain.CourierSession, error) {
	return oneSession(s.q.GetCourierSession(ctx, id))
}

// FindByLinkHash implements app.CourierSessionStore.
func (s *CourierSessionStore) FindByLinkHash(ctx context.Context, h []byte) (domain.CourierSession, error) {
	return oneSession(s.q.GetCourierSessionByLinkHash(ctx, h))
}

// FindByRefreshHash implements app.CourierSessionStore.
func (s *CourierSessionStore) FindByRefreshHash(ctx context.Context, h []byte) (domain.CourierSession, error) {
	return oneSession(s.q.GetCourierSessionByRefreshHash(ctx, h))
}

// Update implements app.CourierSessionStore.
func (s *CourierSessionStore) Update(ctx context.Context, tenantID string, env domain.Environment, courierID string, fn func(app.CourierSessionTx) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if err := q.LockCourierSessions(ctx, db.LockCourierSessionsParams{TenantID: tenantID, Environment: string(env),
			CourierID: courierID}); err != nil {
			return fmt.Errorf("lock courier sessions: %w", err)
		}
		return fn(&courierTx{ctx: ctx, tx: tx, q: q, tenantID: tenantID, env: env, courierID: courierID})
	})
}

// Touch implements app.CourierSessionStore.
func (s *CourierSessionStore) Touch(ctx context.Context, id string, now time.Time) error {
	if err := s.q.TouchCourierSession(ctx, db.TouchCourierSessionParams{ID: id, Now: &now,
		StaleBefore: now.Add(-app.CourierTouchInterval)}); err != nil {
		return fmt.Errorf("touch courier session: %w", err)
	}
	return nil
}

// courierTx is app.CourierSessionTx over one transaction holding the
// courier's lock.
type courierTx struct {
	ctx                 context.Context // scoped to one Update call
	tx                  pgx.Tx
	q                   *db.Queries
	tenantID, courierID string
	env                 domain.Environment
}

func (t *courierTx) Lock(id string) (domain.CourierSession, error) {
	s, err := oneSession(t.q.LockCourierSession(t.ctx, id))
	if err != nil {
		return domain.CourierSession{}, err
	}
	if err := t.owns(s); err != nil {
		return domain.CourierSession{}, err
	}
	return s, nil
}

func (t *courierTx) Current(st domain.SessionStatus) (domain.CourierSession, error) {
	return oneSession(t.q.LockCurrentCourierSession(t.ctx, db.LockCurrentCourierSessionParams{TenantID: t.tenantID,
		Environment: string(t.env), CourierID: t.courierID, Status: string(st)}))
}

// owns guards against changing a session under another courier's lock.
func (t *courierTx) owns(s domain.CourierSession) error {
	if s.TenantID != t.tenantID || s.Env != t.env || s.CourierID != t.courierID {
		return fmt.Errorf("courier session %s is not under the held courier lock", s.ID)
	}
	return nil
}

func (t *courierTx) Insert(s domain.CourierSession) error {
	if err := t.owns(s); err != nil {
		return err
	}
	if err := t.q.InsertCourierSession(t.ctx, db.InsertCourierSessionParams{ID: s.ID, TenantID: s.TenantID,
		Environment: string(s.Env), CourierID: s.CourierID, Mode: string(s.Mode), Status: string(s.State),
		CreatedAt: s.CreatedAt, StartedAt: s.StartedAt, ExpiresAt: s.ExpiresAt, EndedAt: s.EndedAt,
		EndReason: optText(string(s.EndReason)), ExpiresInSeconds: seconds(s.ExpiresIn), LinkCodeHash: s.LinkCodeHash,
		LinkExpiresAt: s.LinkExpiresAt, RefreshHash: s.RefreshHash, RefreshGeneration: generation(s.RefreshGeneration),
		LastSeenAt: s.LastSeenAt}); err != nil {
		return fmt.Errorf("insert courier session: %w", err)
	}
	return nil
}

func (t *courierTx) Save(s domain.CourierSession) error {
	if err := t.owns(s); err != nil {
		return err
	}
	if err := t.q.SupersedeRefreshHash(t.ctx, db.SupersedeRefreshHashParams{ID: s.ID, NewHash: s.RefreshHash}); err != nil {
		return fmt.Errorf("supersede refresh hash: %w", err)
	}
	if err := t.q.UpdateCourierSession(t.ctx, db.UpdateCourierSessionParams{ID: s.ID, Status: string(s.State),
		StartedAt: s.StartedAt, ExpiresAt: s.ExpiresAt, EndedAt: s.EndedAt, EndReason: optText(string(s.EndReason)),
		RefreshHash: s.RefreshHash, RefreshGeneration: generation(s.RefreshGeneration)}); err != nil {
		return fmt.Errorf("update courier session: %w", err)
	}
	return nil
}

func (t *courierTx) Started(s domain.CourierSession) error {
	if s.StartedAt == nil {
		return fmt.Errorf("courier session %s has not started", s.ID)
	}
	return t.enqueue(s, EventCourierSessionStarted,
		&eventsv1.CourierSessionStarted{SessionId: s.ID, CourierId: s.CourierID, Mode: string(s.Mode)}, *s.StartedAt)
}

func (t *courierTx) Ended(s domain.CourierSession) error {
	if s.EndedAt == nil {
		return fmt.Errorf("courier session %s has not ended", s.ID)
	}
	return t.enqueue(s, EventCourierSessionEnded,
		&eventsv1.CourierSessionEnded{SessionId: s.ID, CourierId: s.CourierID, Reason: string(s.EndReason)}, *s.EndedAt)
}

// enqueue writes a courier session event to the outbox on gateway.<type>.
// It carries ids and the mode or reason only: no personal data, never a
// token or code.
func (t *courierTx) enqueue(s domain.CourierSession, eventType string, msg proto.Message, at time.Time) error {
	penv := eventsv1.Environment_ENVIRONMENT_TEST
	if s.Env == domain.EnvLive {
		penv = eventsv1.Environment_ENVIRONMENT_LIVE
	}
	env, err := outbox.NewEnvelope(eventType, s.TenantID, penv, EventSource, &eventsv1.Subject{CourierId: s.CourierID}, msg, at)
	if err != nil {
		return err
	}
	return outbox.Enqueue(t.ctx, t.tx, outbox.SubjectFor(outbox.StreamGateway, eventType), env)
}

func oneSession(r db.CourierSession, err error) (domain.CourierSession, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CourierSession{}, app.ErrNotFound
	}
	if err != nil {
		return domain.CourierSession{}, fmt.Errorf("get courier session: %w", err)
	}
	return domain.CourierSession{ID: r.ID, TenantID: r.TenantID, Env: domain.Environment(r.Environment),
		CourierID: r.CourierID, Mode: domain.SessionMode(r.Mode), State: domain.SessionStatus(r.Status),
		CreatedAt: r.CreatedAt.UTC(), StartedAt: utc(r.StartedAt), ExpiresAt: utc(r.ExpiresAt), EndedAt: utc(r.EndedAt),
		EndReason: domain.EndReason(r.EndReason.String), ExpiresIn: time.Duration(r.ExpiresInSeconds) * time.Second,
		LinkCodeHash: r.LinkCodeHash, LinkExpiresAt: utc(r.LinkExpiresAt), RefreshHash: r.RefreshHash,
		RefreshGeneration: int(r.RefreshGeneration), LastSeenAt: utc(r.LastSeenAt)}, nil
}

// seconds is a session length in whole seconds; the domain bounds it to
// 900–43200.
func seconds(d time.Duration) int32 {
	return int32(d / time.Second) //nolint:gosec // bounded by domain.SessionLength
}

func generation(n int) int32 {
	return int32(n) //nolint:gosec // one rotation per refresh; never near 2^31
}
