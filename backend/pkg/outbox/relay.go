package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// Relay defaults and retention.
const (
	DefaultInterval = 200 * time.Millisecond
	DefaultBatch    = 100
	// Retention is how long published rows are kept before cleanup deletes
	// them; cleanup runs at start and then every CleanupInterval.
	Retention       = 7 * 24 * time.Hour
	CleanupInterval = time.Hour
)

// Relay publishes the service's outbox to JetStream. Run it as a
// bootstrap.Worker. Several replicas may run it: a transaction-scoped
// advisory lock lets one batch at a time through, which keeps rows published
// in created_at order across replicas.
type Relay struct {
	Pool     *pgxpool.Pool
	JS       jetstream.JetStream
	Log      *slog.Logger
	Interval time.Duration // time between batches; DefaultInterval if zero
	Batch    int           // rows per batch; DefaultBatch if zero

	metrics *relayMetrics
}

// Run publishes batches until ctx is cancelled, then returns nil. Database
// and NATS errors are logged and retried on the next tick; they never stop
// the service.
func (r *Relay) Run(ctx context.Context) error {
	interval, batch := r.Interval, r.Batch
	if interval <= 0 {
		interval = DefaultInterval
	}
	if batch <= 0 {
		batch = DefaultBatch
	}
	log := r.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if m, err := newRelayMetrics(newMeter()); err != nil {
		log.WarnContext(ctx, "outbox_metrics_failed", slog.Any("error", err))
	} else {
		r.metrics = m
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	cleanup := time.NewTicker(CleanupInterval)
	defer cleanup.Stop()

	r.cleanup(ctx, log)
	for {
		if err := r.publishBatch(ctx, batch); err != nil && ctx.Err() == nil {
			log.WarnContext(ctx, "outbox_relay_failed", slog.Any("error", err))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-cleanup.C:
			r.cleanup(ctx, log)
		case <-tick.C:
		}
	}
}

// relayLock serialises batches of one schema's outbox across replicas.
const relayLock = `SELECT pg_try_advisory_xact_lock(hashtext(current_schema() || '.outbox'))`

// publishBatch publishes up to batch rows in created_at order, in one
// transaction. It stops at the first publish error; the rows not marked
// published are retried on the next tick, in order.
func (r *Relay) publishBatch(ctx context.Context, batch int) error {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var locked bool
	if err := tx.QueryRow(ctx, relayLock).Scan(&locked); err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	if !locked {
		return nil // another relay is publishing this outbox
	}

	rows, err := tx.Query(ctx, `SELECT event_id, subject, payload, headers FROM outbox
		WHERE published_at IS NULL ORDER BY created_at, event_id
		FOR UPDATE SKIP LOCKED LIMIT $1`, batch)
	if err != nil {
		return fmt.Errorf("select: %w", err)
	}
	type row struct {
		EventID string
		Subject string
		Payload []byte
		Headers map[string]string
	}
	pending, err := pgx.CollectRows(rows, pgx.RowToStructByPos[row])
	if err != nil {
		return fmt.Errorf("select: %w", err)
	}
	if len(pending) == 0 {
		r.metrics.setPending(0)
		return nil
	}
	start := time.Now()
	defer func() { r.metrics.observePublish(ctx, time.Since(start)) }()

	tracer := otel.Tracer(tracerName)
	published := make([]string, 0, len(pending))
	var pubErr error
	for _, p := range pending {
		// The publish span continues the trace stored with the row; the
		// headers on the NATS message are re-injected from it, so the
		// consumer's span becomes its child.
		pctx := otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(p.Headers))
		pctx, span := tracer.Start(pctx, "publish "+p.Subject,
			trace.WithSpanKind(trace.SpanKindProducer),
			trace.WithAttributes(
				attribute.String("messaging.system", "nats"),
				attribute.String("messaging.destination.name", p.Subject),
				attribute.String("messaging.message.id", p.EventID),
				attribute.String("dt.event_id", p.EventID),
			))
		msg := nats.NewMsg(p.Subject)
		msg.Data = p.Payload
		for k, v := range p.Headers {
			msg.Header.Set(k, v)
		}
		otel.GetTextMapPropagator().Inject(pctx, headerCarrier(msg.Header))
		// PublishMsg waits for the stream's ack. Nats-Msg-Id makes a
		// re-publish after a lost ack or a failed commit a no-op.
		_, err := r.JS.PublishMsg(pctx, msg, jetstream.WithMsgID(p.EventID))
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "publish failed")
		}
		span.End()
		if err != nil {
			pubErr = fmt.Errorf("publish %s on %s: %w", p.EventID, p.Subject, err)
			break
		}
		published = append(published, p.EventID)
	}
	// The relay only reads one batch, so "pending" is what this batch left
	// behind; a backlog deeper than the batch shows as a non-zero value only
	// while publishing fails.
	r.metrics.setPending(int64(len(pending) - len(published)))
	if len(published) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE outbox SET published_at = now() WHERE event_id = ANY($1)`,
			published); err != nil {
			return errors.Join(pubErr, fmt.Errorf("mark published: %w", err))
		}
		if err := tx.Commit(ctx); err != nil {
			return errors.Join(pubErr, fmt.Errorf("commit: %w", err))
		}
	}
	return pubErr
}

// cleanup deletes rows published more than Retention ago.
func (r *Relay) cleanup(ctx context.Context, log *slog.Logger) {
	tag, err := r.Pool.Exec(ctx, `DELETE FROM outbox WHERE published_at < now() - make_interval(secs => $1)`,
		Retention.Seconds())
	if err != nil {
		if ctx.Err() == nil {
			log.WarnContext(ctx, "outbox_cleanup_failed", slog.Any("error", err))
		}
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		log.InfoContext(ctx, "outbox_cleanup", slog.Int64("deleted", n))
	}
}
