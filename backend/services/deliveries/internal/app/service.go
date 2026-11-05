package app

import (
	"context"
	"errors"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/raduolivas/dt/backend/pkg/ids"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/domain"
)

// List page size bounds.
const (
	defaultListLimit = 20
	maxListLimit     = 100
)

const tracerName = "github.com/raduolivas/dt/backend/services/deliveries/internal/app"

// startSpan starts the use case's span with the scope's ids; deliveryID may
// be empty when it is not known yet. The returned func ends the span,
// marking it failed when *err is set.
func startSpan(ctx context.Context, name string, sc Scope, deliveryID string) (context.Context, trace.Span, func(err error)) {
	attrs := []attribute.KeyValue{attribute.String("dt.tenant_id", sc.TenantID), attribute.String("dt.environment", sc.Env)}
	if deliveryID != "" {
		attrs = append(attrs, attribute.String("dt.delivery_id", deliveryID))
	}
	ctx, span := otel.Tracer(tracerName).Start(ctx, name, trace.WithAttributes(attrs...))
	return ctx, span, func(err error) {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, name+" failed")
		}
		span.End()
	}
}

// Service holds the deliveries use cases. Domain errors pass through
// unchanged; the transport layer maps them.
type Service struct {
	Repo  Repo
	Clock Clock
	IDs   IDs
	// Retention serves the nightly retention job.
	Retention RetentionStore
	// Log defaults to a discarding logger.
	Log *slog.Logger
}

// Create validates the input against the tenant's current settings and
// stores a confirmed delivery. The delivery keeps the settings' pickup
// policy from this moment on.
func (s *Service) Create(ctx context.Context, sc Scope, in domain.NewDeliveryInput) (d *domain.Delivery, err error) {
	ctx, span, end := startSpan(ctx, "deliveries.create", sc, "")
	defer func() { end(err) }()
	settings, err := s.Repo.Settings(ctx, sc.TenantID)
	if err != nil {
		return nil, err
	}
	d, evts, err := domain.NewDelivery(s.IDs.New(ids.Delivery), sc.TenantID, sc.Env, in, settings, s.Clock.Now())
	if err != nil {
		return nil, err
	}
	span.SetAttributes(attribute.String("dt.delivery_id", d.ID))
	if err := s.Repo.Create(ctx, sc, d, evts); err != nil {
		return nil, err
	}
	return d, nil
}

// Get returns a delivery of the scope.
func (s *Service) Get(ctx context.Context, sc Scope, id string) (*domain.Delivery, error) {
	return s.Repo.Get(ctx, sc, id)
}

// List returns a page of deliveries and whether more follow. Limit is
// clamped to 1-100 and defaults to 20.
func (s *Service) List(ctx context.Context, sc Scope, f ListFilter) ([]*domain.Delivery, bool, error) {
	switch {
	case f.Limit <= 0:
		f.Limit = defaultListLimit
	case f.Limit > maxListLimit:
		f.Limit = maxListLimit
	}
	return s.Repo.List(ctx, sc, f)
}

// ListCourierDeliveries returns the courier's assigned deliveries that are
// not final, oldest first.
func (s *Service) ListCourierDeliveries(ctx context.Context, sc Scope, courierID string) ([]*domain.Delivery, error) {
	return s.Repo.ListByCourier(ctx, sc, courierID)
}

// Courier returns the courier, or ErrCourierNotFound.
func (s *Service) Courier(ctx context.Context, sc Scope, id string) (domain.Courier, error) {
	c, err := s.Repo.Courier(ctx, sc, id)
	if errors.Is(err, ErrNotFound) {
		return domain.Courier{}, ErrCourierNotFound
	}
	return c, err
}

// Update applies a partial update.
func (s *Service) Update(ctx context.Context, sc Scope, id string, p domain.Patch) (_ *domain.Delivery, err error) {
	ctx, _, end := startSpan(ctx, "deliveries.update", sc, id)
	defer func() { end(err) }()
	return s.Repo.Mutate(ctx, sc, id, func(d *domain.Delivery) ([]domain.Event, error) {
		return d.Update(p, s.Clock.Now())
	})
}

// Assign attaches a courier to a delivery. An unknown courier is
// ErrCourierNotFound.
func (s *Service) Assign(ctx context.Context, sc Scope, deliveryID, courierID string) (_ *domain.Delivery, err error) {
	ctx, _, end := startSpan(ctx, "deliveries.assign", sc, deliveryID)
	defer func() { end(err) }()
	c, err := s.Repo.Courier(ctx, sc, courierID)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrCourierNotFound
	}
	if err != nil {
		return nil, err
	}
	ref := domain.CourierRef{ID: c.ID, ExternalID: c.ExternalID, DisplayName: c.DisplayName, Vehicle: c.Vehicle}
	return s.Repo.Mutate(ctx, sc, deliveryID, func(d *domain.Delivery) ([]domain.Event, error) {
		return d.Assign(ref, s.Clock.Now())
	})
}

// Report applies a status report, stamping ReceivedAt from the clock, and
// returns what the domain did with it.
func (s *Service) Report(ctx context.Context, sc Scope, id string, r domain.Report) (*domain.Delivery, domain.Outcome, error) {
	return s.report(ctx, "deliveries.report", sc, id, "", r)
}

// ReportFor is Report with an ownership guard: when courierID is set, a
// delivery that is not assigned to that courier, whatever its status, is
// ErrNotFound. The guard runs under the row lock, so a reassignment cannot
// slip between the check and the report, and a final delivery's own
// repeated event still answers duplicate.
func (s *Service) ReportFor(ctx context.Context, sc Scope, id, courierID string, r domain.Report) (*domain.Delivery, domain.Outcome, error) {
	return s.report(ctx, "deliveries.reportfor", sc, id, courierID, r)
}

func (s *Service) report(ctx context.Context, name string, sc Scope, id, courierID string, r domain.Report) (_ *domain.Delivery, _ domain.Outcome, err error) {
	ctx, span, end := startSpan(ctx, name, sc, id)
	defer func() { end(err) }()
	var out domain.Outcome
	d, err := s.Repo.Mutate(ctx, sc, id, func(d *domain.Delivery) ([]domain.Event, error) {
		if courierID != "" && !d.AssignedTo(courierID) {
			return nil, ErrNotFound
		}
		r.ReceivedAt = s.Clock.Now()
		o, evts, err := d.Report(r)
		out = o
		return evts, err
	})
	if err != nil {
		return nil, "", err
	}
	span.SetAttributes(attribute.String("deliveries.outcome", string(out)))
	return d, out, nil
}

// Cancel cancels a delivery that is not final.
func (s *Service) Cancel(ctx context.Context, sc Scope, id, reason string) (_ *domain.Delivery, err error) {
	ctx, _, end := startSpan(ctx, "deliveries.cancel", sc, id)
	defer func() { end(err) }()
	return s.Repo.Mutate(ctx, sc, id, func(d *domain.Delivery) ([]domain.Event, error) {
		return d.Cancel(reason, s.Clock.Now())
	})
}

// ErasePersonalData erases a final delivery's personal data.
func (s *Service) ErasePersonalData(ctx context.Context, sc Scope, id string) (*domain.Delivery, error) {
	return s.Repo.Mutate(ctx, sc, id, func(d *domain.Delivery) ([]domain.Event, error) {
		return d.ErasePersonalData(s.Clock.Now())
	})
}

// UpsertCourier creates or updates a courier by external id; the bool
// reports whether it was created.
func (s *Service) UpsertCourier(ctx context.Context, sc Scope, in domain.CourierInput) (domain.Courier, bool, error) {
	c, err := domain.NewCourier(s.IDs.New(ids.Courier), in, s.Clock.Now())
	if err != nil {
		return domain.Courier{}, false, err
	}
	return s.Repo.UpsertCourier(ctx, sc, c, []domain.Event{c.UpsertedEvent()})
}
