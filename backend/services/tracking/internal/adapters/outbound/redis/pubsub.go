package redis

import (
	"context"
	"encoding/json"
	"fmt"

	goredis "github.com/redis/go-redis/v9"

	"github.com/raduolivas/dt/backend/services/tracking/internal/app"
	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

// Publisher publishes viewer snapshots on dt:snap:<del_id>, where every
// tracking instance with a viewer of that delivery listens.
type Publisher struct {
	rdb *goredis.Client
}

var _ app.Publisher = (*Publisher)(nil)

// NewPublisher returns a publisher over rdb.
func NewPublisher(rdb *goredis.Client) *Publisher { return &Publisher{rdb: rdb} }

// SnapChannel is the channel a delivery's viewer snapshots go to.
func SnapChannel(deliveryID string) string { return snapChannel(deliveryID) }

// Snapshot publishes s as JSON.
func (p *Publisher) Snapshot(ctx context.Context, deliveryID string, s domain.TrackingSnapshot) error {
	b, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("encode snapshot: %w", err)
	}
	return p.rdb.Publish(ctx, snapChannel(deliveryID), b).Err()
}
