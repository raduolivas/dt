package app_test

import (
	"context"
	"errors"
	"maps"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/raduolivas/dt/backend/services/accounts/internal/app"
	"github.com/raduolivas/dt/backend/services/accounts/internal/domain"
)

type published struct {
	Type     string
	TenantID string
	Env      domain.Environment
	Payload  proto.Message
}

// fakeRepo is an in-memory Repo with all-or-nothing transactions.
type fakeRepo struct {
	tenants map[string]domain.Tenant
	keys    map[string]domain.APIKey
	events  []published
	// failPublishAt makes the n-th (1-based) Publish call fail when > 0.
	failPublishAt int
	locked        []string // key ids read via GetKeyForUpdate
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{tenants: map[string]domain.Tenant{}, keys: map[string]domain.APIKey{}}
}

func (r *fakeRepo) InTx(ctx context.Context, fn func(context.Context, app.Tx) error) error {
	tenants, keys := maps.Clone(r.tenants), maps.Clone(r.keys)
	events := append([]published(nil), r.events...)
	calls := 0
	tx := &fakeTx{r: r, calls: &calls}
	if err := fn(ctx, tx); err != nil {
		r.tenants, r.keys, r.events = tenants, keys, events
		return err
	}
	return nil
}

type fakeTx struct {
	r     *fakeRepo
	calls *int
}

func (t *fakeTx) InsertTenant(_ context.Context, x domain.Tenant) error {
	t.r.tenants[x.ID] = x
	return nil
}

func (t *fakeTx) GetTenant(_ context.Context, id string) (domain.Tenant, error) {
	x, ok := t.r.tenants[id]
	if !ok {
		return domain.Tenant{}, domain.ErrNotFound
	}
	return x, nil
}

func (t *fakeTx) SetLiveEnabled(_ context.Context, id string, v bool) error {
	x, ok := t.r.tenants[id]
	if !ok {
		return domain.ErrNotFound
	}
	x.LiveEnabled = v
	t.r.tenants[id] = x
	return nil
}

func (t *fakeTx) SetTenantSettings(_ context.Context, id string, s domain.TenantSettings) error {
	x, ok := t.r.tenants[id]
	if !ok {
		return domain.ErrNotFound
	}
	x.Settings = s
	t.r.tenants[id] = x
	return nil
}

func (t *fakeTx) InsertKey(_ context.Context, k domain.APIKey) error {
	t.r.keys[k.ID] = k
	return nil
}

func (t *fakeTx) GetKeyForUpdate(_ context.Context, tenantID, keyID string) (domain.APIKey, error) {
	k, ok := t.r.keys[keyID]
	if !ok || k.TenantID != tenantID {
		return domain.APIKey{}, domain.ErrNotFound
	}
	t.r.locked = append(t.r.locked, keyID)
	return k, nil
}

func (t *fakeTx) UpdateKey(_ context.Context, k domain.APIKey) error {
	if _, ok := t.r.keys[k.ID]; !ok {
		return domain.ErrNotFound
	}
	t.r.keys[k.ID] = k
	return nil
}

func (t *fakeTx) ListKeys(_ context.Context, tenantID string, env domain.Environment) ([]domain.APIKey, error) {
	var out []domain.APIKey
	for _, k := range t.r.keys {
		if k.TenantID == tenantID && k.Env == env {
			out = append(out, k)
		}
	}
	return out, nil
}

func (t *fakeTx) Publish(_ context.Context, typ, tenantID string, env domain.Environment, p proto.Message) error {
	*t.calls++
	if t.r.failPublishAt > 0 && *t.calls == t.r.failPublishAt {
		return errors.New("outbox down")
	}
	t.r.events = append(t.r.events, published{typ, tenantID, env, p})
	return nil
}

// fakeKeys returns deterministic keys; the plaintext embeds a marker so tests
// can assert it never leaks into events.
type fakeKeys struct{ n int }

func (g *fakeKeys) Generate(env domain.Environment) (string, string, string, []byte, error) {
	g.n++
	lookup := "lookup" + string(rune('a'+g.n))
	plain := "dt_sk_" + string(env) + "_" + lookup + "SECRET-PLAINTEXT-" + string(rune('a'+g.n))
	return plain, lookup, "dt_sk_" + string(env) + "_" + lookup, []byte{0xde, 0xad, byte(g.n)}, nil
}

type fixedClock struct{ t time.Time }

func (c *fixedClock) Now() time.Time { return c.t }
