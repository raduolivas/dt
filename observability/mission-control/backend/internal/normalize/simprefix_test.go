package normalize

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
)

// R38: the order ref prefix marks the delivery even when no registry knows it yet (delivery.created can reach the
// normalizer before the simulator has recorded the id, and a restart empties the registry).
func TestSimPrefixMarksDeliveryCreatedSimulated(t *testing.T) {
	n := newNormalizer()
	n.SimRegistry = reg{}
	dc := &eventsv1.DeliveryCreated{DeliveryId: "del_9", ExternalId: "mc-sim-20261107-080000-abc123", DeliveryModel: "direct", Status: "created", PickupPolicy: "any"}
	ev, err := n.FromEnvelope("deliveries.delivery.created", envelopeFor(t, dc, "api", "del_9"), time.Now())
	require.NoError(t, err)
	require.Equal(t, "simulated", ev.Origin)
	require.Equal(t, "mc-sim-20261107-080000-abc123", ev.OrderRef)

	dc.ExternalId, dc.DeliveryId = "ORDER-1", "del_10"
	ev, err = n.FromEnvelope("deliveries.delivery.created", envelopeFor(t, dc, "api", "del_10"), time.Now())
	require.NoError(t, err)
	require.Equal(t, "live", ev.Origin)
}
