package redis

import (
	"context"
	"encoding/json"
	"fmt"

	goredis "github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/raduolivas/dt/backend/services/tracking/internal/app"
	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

// snapEnvelope is the pub/sub payload: the snapshot with the publisher's W3C
// traceparent (empty without a span). Before the envelope the payload was the
// bare snapshot; Feed still accepts that form during rolling deploys.
type snapEnvelope struct {
	TP string          `json:"tp"`
	S  json.RawMessage `json:"s"`
	// RT is when the gateway received the fix behind this snapshot (unix ms)
	// and Src the fix's source; both are absent for a snapshot no fix caused.
	// Viewers never see them: the hub decodes only S.
	RT  int64  `json:"rt,omitempty"`
	Src string `json:"src,omitempty"`
}

func traceparent(ctx context.Context) string {
	c := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, c)
	return c.Get("traceparent")
}

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

// Snapshot publishes s as JSON, wrapped in an envelope that carries the
// caller's traceparent.
func (p *Publisher) Snapshot(ctx context.Context, deliveryID string, s domain.TrackingSnapshot) error {
	b, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("encode snapshot: %w", err)
	}
	e := snapEnvelope{TP: traceparent(ctx), S: b}
	if at, src, ok := app.FixReceived(ctx); ok {
		e.RT, e.Src = at.UnixMilli(), src
	}
	env, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode snapshot envelope: %w", err)
	}
	return p.rdb.Publish(ctx, snapChannel(deliveryID), env).Err()
}
