package sim

import "sync"

// Registry holds the ids of the deliveries simulation mode created. The normalizer reads it (through
// normalize.SimRegistry) to tag their events origin=simulated. A nil Registry marks nothing.
type Registry struct {
	mu  sync.RWMutex
	ids map[string]struct{}
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{ids: map[string]struct{}{}} }

// Add records a simulated delivery.
func (r *Registry) Add(deliveryID string) {
	if r == nil || deliveryID == "" {
		return
	}
	r.mu.Lock()
	r.ids[deliveryID] = struct{}{}
	r.mu.Unlock()
}

// IsSimulated tells whether the delivery was created by simulation mode.
func (r *Registry) IsSimulated(deliveryID string) bool {
	if r == nil || deliveryID == "" {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.ids[deliveryID]
	return ok
}
