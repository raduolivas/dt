package app

import (
	"context"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/raduolivas/dt/backend/services/accounts/internal/domain"
)

// Repo runs a unit of work in one transaction.
type Repo interface {
	InTx(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
}

// Tx is the transactional view of the accounts store. Publish writes to the
// outbox in the same transaction as the state change.
type Tx interface {
	InsertTenant(ctx context.Context, t domain.Tenant) error
	GetTenant(ctx context.Context, id string) (domain.Tenant, error)
	SetLiveEnabled(ctx context.Context, id string, v bool) error
	InsertKey(ctx context.Context, k domain.APIKey) error
	// GetKeyForUpdate locks the row; domain.ErrNotFound when the tenant differs.
	GetKeyForUpdate(ctx context.Context, tenantID, keyID string) (domain.APIKey, error)
	UpdateKey(ctx context.Context, k domain.APIKey) error
	ListKeys(ctx context.Context, tenantID string, env domain.Environment) ([]domain.APIKey, error)
	Publish(ctx context.Context, eventType, tenantID string, env domain.Environment, payload proto.Message) error
}

// KeyGenerator mints a new secret key. Only the hash is ever persisted.
type KeyGenerator interface {
	Generate(env domain.Environment) (plaintext, lookup, displayPrefix string, hash []byte, err error)
}

// Clock abstracts time for tests.
type Clock interface{ Now() time.Time }
