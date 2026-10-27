// Package tracking reads the customer's snapshot from the tracking service's
// internal RPC.
package tracking

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	eventsv1 "github.com/raduolivas/dt/backend/gen/dt/events/v1"
	trackingv1 "github.com/raduolivas/dt/backend/gen/dt/tracking/v1"
	"github.com/raduolivas/dt/backend/gen/dt/tracking/v1/trackingv1connect"
	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
	"github.com/raduolivas/dt/backend/services/gateway/internal/domain"
)

// Snapshots is app.SnapshotSource over TrackingService.GetSnapshot.
type Snapshots struct {
	c trackingv1connect.TrackingServiceClient
}

var _ app.SnapshotSource = (*Snapshots)(nil)

// NewSnapshots returns a SnapshotSource over c.
func NewSnapshots(c trackingv1connect.TrackingServiceClient) *Snapshots { return &Snapshots{c: c} }

// Snapshot reads the link's delivery in the link's tenant and environment;
// the link id is the actor.
func (s *Snapshots) Snapshot(ctx context.Context, l domain.TrackingLink) (json.RawMessage, error) {
	env := eventsv1.Environment_ENVIRONMENT_TEST
	if l.Env == domain.EnvLive {
		env = eventsv1.Environment_ENVIRONMENT_LIVE
	}
	res, err := s.c.GetSnapshot(ctx, &trackingv1.GetSnapshotRequest{
		Scope:      &trackingv1.Scope{TenantId: l.TenantID, Environment: env, ActorId: l.ID},
		DeliveryId: l.DeliveryID,
	})
	if err != nil {
		return nil, fmt.Errorf("tracking GetSnapshot: %w", err)
	}
	b := res.GetSnapshotJson()
	if !json.Valid(b) {
		return nil, errors.New("tracking GetSnapshot: snapshot is not JSON")
	}
	return json.RawMessage(b), nil
}
