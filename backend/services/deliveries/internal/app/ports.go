package app

import (
	"context"
	"errors"
	"time"

	"github.com/raduolivas/dt/backend/services/deliveries/internal/domain"
)

// Errors returned by the use cases and the repository.
var (
	// ErrNotFound means the delivery does not exist for the scope's tenant
	// and environment.
	ErrNotFound = errors.New("delivery not found")
	// ErrExternalIDTaken means the tenant already has a delivery with this
	// external id in the environment.
	ErrExternalIDTaken = errors.New("external id already used")
	// ErrCourierNotFound means the courier does not exist for the scope.
	ErrCourierNotFound = errors.New("courier not found")
)

// Scope is the tenant, environment and actor a use case runs for.
type Scope struct{ TenantID, Env, ActorID string }

// ListFilter selects deliveries, newest first by id.
type ListFilter struct {
	Status                *domain.Status
	Model                 *domain.Model
	CreatedGTE, CreatedLT *time.Time
	ExternalID            *string
	Limit                 int
	StartingAfter         string
}

// PickupWatch is a delivery whose pickup confirmation has been pending
// since Since.
type PickupWatch struct {
	DeliveryID, TenantID, Env string
	Since                     time.Time
}

// Repo persists deliveries and couriers. Events are written to the outbox in
// the same transaction as the state they describe.
type Repo interface {
	// Settings returns the tenant's delivery settings, or the defaults when
	// none were received yet.
	Settings(ctx context.Context, tenantID string) (domain.TenantSettings, error)
	Create(ctx context.Context, s Scope, d *domain.Delivery, evts []domain.Event) error
	Get(ctx context.Context, s Scope, id string) (*domain.Delivery, error)
	List(ctx context.Context, s Scope, f ListFilter) ([]*domain.Delivery, bool, error)
	// Mutate loads id FOR UPDATE, runs fn, and persists the delivery, its
	// new history and fn's events in one transaction. If fn errors, nothing
	// is written.
	Mutate(ctx context.Context, s Scope, id string, fn func(*domain.Delivery) ([]domain.Event, error)) (*domain.Delivery, error)
	// Courier returns ErrNotFound when the courier does not exist.
	Courier(ctx context.Context, s Scope, id string) (domain.Courier, error)
	// UpsertCourier creates the courier or updates the one with the same
	// external id (keeping its id); the bool reports whether it was created.
	UpsertCourier(ctx context.Context, s Scope, c domain.Courier, evts []domain.Event) (domain.Courier, bool, error)
	DuePickupWatches(ctx context.Context, now time.Time, limit int) ([]PickupWatch, error)
	// WarnPickupPending emits delivery.pickup_confirmation_pending for w
	// and marks it warned, at most once.
	WarnPickupPending(ctx context.Context, w PickupWatch, now time.Time) error
}

// Clock abstracts time for tests.
type Clock interface{ Now() time.Time }

// IDs creates prefixed identifiers.
type IDs interface{ New(prefix string) string }
