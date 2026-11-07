package sim

import (
	"context"
	"sync"
	"time"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/normalize"
)

// OrderRefPrefix marks the order refs simulation mode creates. The normalizer marks a DeliveryCreated whose
// external_id starts with it as origin=simulated (ruling R38), so the marker needs no registry entry and survives a
// restart. Use the prefix for sandbox orders only: a partner order ref starting with it would be shown as simulated.
const OrderRefPrefix = normalize.SimOrderRefPrefix

// OriginLookup returns the stored origin of a delivery from mc_order ("" when unknown).
type OriginLookup func(ctx context.Context, deliveryID string) (string, error)

const (
	lookupTimeout  = 2 * time.Second
	defaultNegTTL  = 2 * time.Second
	maxNegEntries  = 10000
	simulatedValue = "simulated"
)

// Registry tells which deliveries are simulated (normalize.SimRegistry). It knows the deliveries this process
// created and falls back to the stored order origin, so later events of a simulated delivery stay simulated after a
// restart. A nil Registry marks nothing.
type Registry struct {
	mu     sync.RWMutex
	ids    map[string]struct{}
	neg    map[string]time.Time // recent "not simulated" answers from the store
	negTTL time.Duration
	lookup OriginLookup
}

// NewRegistry returns an empty registry; lookup may be nil.
func NewRegistry(lookup OriginLookup) *Registry {
	return &Registry{ids: map[string]struct{}{}, neg: map[string]time.Time{}, negTTL: defaultNegTTL, lookup: lookup}
}

// Add records a simulated delivery.
func (r *Registry) Add(deliveryID string) {
	if r == nil || deliveryID == "" {
		return
	}
	r.mu.Lock()
	r.ids[deliveryID] = struct{}{}
	delete(r.neg, deliveryID)
	r.mu.Unlock()
}

// IsSimulated tells whether the delivery was created by simulation mode.
func (r *Registry) IsSimulated(deliveryID string) bool {
	if r == nil || deliveryID == "" {
		return false
	}
	r.mu.RLock()
	_, ok := r.ids[deliveryID]
	at, seen := r.neg[deliveryID]
	ttl := r.negTTL
	r.mu.RUnlock()
	if ok {
		return true
	}
	if r.lookup == nil || (seen && time.Since(at) < ttl) {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), lookupTimeout)
	defer cancel()
	origin, err := r.lookup(ctx, deliveryID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err == nil && origin == simulatedValue {
		r.ids[deliveryID] = struct{}{}
		delete(r.neg, deliveryID)
		return true
	}
	// the order row may not exist yet (events arrive before the index writes it): ask again after the TTL
	if len(r.neg) >= maxNegEntries {
		r.neg = map[string]time.Time{}
	}
	r.neg[deliveryID] = time.Now()
	return false
}
