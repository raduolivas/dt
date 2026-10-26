package app

import (
	"context"
	"errors"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

// Errors returned by the use cases and the ports.
var (
	// ErrNotFound means the delivery does not exist for the scope's tenant
	// and environment.
	ErrNotFound = errors.New("delivery not found")
	// ErrCourierNotFound means the courier does not exist for the scope.
	ErrCourierNotFound = errors.New("courier not found")
	// ErrNoChange, returned by an Apply mutation, means it changed nothing:
	// Apply stores and publishes nothing and returns nil.
	ErrNoChange = errors.New("no change")
	// ErrConflict means the state kept changing under Apply (every
	// compare-and-set attempt lost).
	ErrConflict = errors.New("tracking state: concurrent update conflict")
)

// Scope is the tenant and environment a use case runs for. Env is "test" or
// "live".
type Scope struct{ TenantID, Env string }

// Clock returns the current time.
type Clock interface{ Now() time.Time }

// StateStore holds the live state of each delivery (Redis).
type StateStore interface {
	// Get returns the stored state; false when none is stored.
	Get(ctx context.Context, deliveryID string) (domain.State, bool, error)
	// Put stores st only if the stored state's Version is still prevVersion
	// (0: nothing stored), so a writer that loaded an older state never
	// overwrites a newer one; st.Version must be greater than prevVersion.
	// It reports whether st was stored.
	Put(ctx context.Context, st domain.State, prevVersion int64) (stored bool, err error)
	// ActiveDeliveriesForCourier returns the ids of the courier's non-final
	// deliveries, rebuilding the cached set from Postgres on a miss. It
	// returns ErrCourierNotFound when the courier does not exist for the
	// tenant and environment.
	ActiveDeliveriesForCourier(ctx context.Context, tenantID, env, courierID string) ([]string, error)
	// SetCourierActive adds (active) or removes deliveryID in the courier's
	// cached set. A set that is not cached is left alone: the next read
	// rebuilds it from Postgres.
	SetCourierActive(ctx context.Context, courierID, deliveryID string, active bool) error
}

// ReadModel reads the tracking read model (Postgres).
type ReadModel interface {
	// LoadState rebuilds a delivery's state from Postgres and its latest
	// trail fix; ErrNotFound when the delivery is unknown in the scope.
	LoadState(ctx context.Context, s Scope, deliveryID string) (domain.State, error)
	// RecentTrail returns the delivery's stored fixes from courierID with
	// from <= t <= to.
	RecentTrail(ctx context.Context, s Scope, deliveryID, courierID string, from, to time.Time) ([]domain.Fix, error)
}

// DeliveryRecord is a delivery's row in the read model.
type DeliveryRecord struct {
	ID, TenantID, Env   string
	Model               domain.Model
	Status              domain.Status
	Pickup              domain.LatLng
	PickupName          string
	Dropoff             domain.LatLng
	CreatedAt           time.Time
	ReadyAt             *time.Time
	CourierID           *string
	CourierVisibleSince *time.Time
	Timeline            []domain.TimelineEntry
	ErasedAt, FinalAt   *time.Time
	// EventVersion is the last applied envelope delivery_version.
	EventVersion uint64
	UpdatedAt    time.Time
}

// ProofPoint is a courier position recorded at a status change.
type ProofPoint struct {
	DeliveryID string
	Status     string // at_pickup | pickup_confirmed | arriving | delivered | failed
	At         time.Time
	Actor      string
	// Fix is the position, nil when no fix within 2 minutes.
	Fix *domain.Fix
}

// Projection is the read model bound to one consumer transaction and scope:
// its reads see the transaction's own writes.
type Projection interface {
	ReadModel
	// Delivery returns the delivery's record, locked for the transaction.
	Delivery(ctx context.Context, id string) (DeliveryRecord, bool, error)
	PutDelivery(ctx context.Context, rec DeliveryRecord) error
	Courier(ctx context.Context, id string) (domain.Courier, bool, error)
	PutCourier(ctx context.Context, c domain.Courier) error
	// CourierDeliveries returns the ids of the courier's non-final deliveries.
	CourierDeliveries(ctx context.Context, courierID string) ([]string, error)
	PutProofPoint(ctx context.Context, p ProofPoint) error
}

// SettingsProjection stores tenant settings in a consumer transaction.
type SettingsProjection interface {
	// SetTenantPrep stores the default preparation time as of at, unless
	// newer settings are stored; it reports whether it was stored.
	SetTenantPrep(ctx context.Context, tenantID string, prepMinutes *int, at time.Time) (bool, error)
	// Bind returns the projection for one scope in the same transaction.
	Bind(ctx context.Context, s Scope) (Projection, error)
	// ActiveDeliveries returns the scope's non-final delivery ids.
	ActiveDeliveries(ctx context.Context, s Scope) ([]string, error)
}

// TrailRow is one fix attached to one delivery.
type TrailRow struct {
	DeliveryID, TenantID, Env, CourierID string
	Fix                                  domain.Fix
}

// TrailWriter stores fixes in the background, in batches.
type TrailWriter interface {
	Append(rows []TrailRow)
}

// Publisher fans viewer snapshots out to every tracking instance.
type Publisher interface {
	Snapshot(ctx context.Context, deliveryID string, s domain.TrackingSnapshot) error
}

// Subject is what an event is about.
type Subject struct{ DeliveryID, CourierID string }

// Events enqueues tracking events in the outbox.
type Events interface {
	Enqueue(ctx context.Context, typ, tenantID, env string, subject Subject, msg proto.Message) error
}

// Sampler allows one courier.location_reported per courier per minute.
type Sampler interface {
	Allow(ctx context.Context, courierID string) (bool, error)
}

// Router finds roads. Profile is "car" or "bike".
type Router interface {
	// Route returns the path through pts in order and its drive time in
	// seconds.
	Route(ctx context.Context, profile string, pts ...domain.LatLng) (path []domain.LatLng, seconds int, err error)
	// Match snaps fixes to the road network and returns the matched path.
	Match(ctx context.Context, profile string, fixes []domain.Fix) ([]domain.LatLng, error)
}
