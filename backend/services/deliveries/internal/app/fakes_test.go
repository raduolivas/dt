package app_test

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/raduolivas/dt/backend/services/deliveries/internal/app"
	"github.com/raduolivas/dt/backend/services/deliveries/internal/domain"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

type fakeIDs struct{ n int }

func (f *fakeIDs) New(prefix string) string {
	f.n++
	return fmt.Sprintf("%s_%03d", prefix, f.n)
}

// fakeRepo is an in-memory Repo with all-or-nothing Mutate.
type fakeRepo struct {
	settings   domain.TenantSettings
	deliveries map[string]*domain.Delivery
	couriers   map[string]domain.Courier // by id
	events     []domain.Event
	lastList   app.ListFilter

	due      []app.PickupWatch
	warned   []string
	warnErrs map[string]error
	dueLimit int
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		deliveries: map[string]*domain.Delivery{},
		couriers:   map[string]domain.Courier{},
		warnErrs:   map[string]error{},
	}
}

func (r *fakeRepo) Settings(context.Context, string) (domain.TenantSettings, error) {
	return r.settings, nil
}

func (r *fakeRepo) Create(_ context.Context, s app.Scope, d *domain.Delivery, evts []domain.Event) error {
	for _, e := range r.deliveries {
		if e.TenantID == s.TenantID && e.Env == s.Env && e.ExternalID == d.ExternalID {
			return app.ErrExternalIDTaken
		}
	}
	c := *d
	r.deliveries[d.ID] = &c
	r.events = append(r.events, evts...)
	return nil
}

func (r *fakeRepo) Get(_ context.Context, s app.Scope, id string) (*domain.Delivery, error) {
	d, ok := r.deliveries[id]
	if !ok || d.TenantID != s.TenantID || d.Env != s.Env {
		return nil, app.ErrNotFound
	}
	c := *d
	return &c, nil
}

func (r *fakeRepo) List(_ context.Context, _ app.Scope, f app.ListFilter) ([]*domain.Delivery, bool, error) {
	r.lastList = f
	var out []*domain.Delivery
	for _, d := range r.deliveries {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, false, nil
}

func (r *fakeRepo) Mutate(ctx context.Context, s app.Scope, id string, fn func(*domain.Delivery) ([]domain.Event, error)) (*domain.Delivery, error) {
	d, err := r.Get(ctx, s, id)
	if err != nil {
		return nil, err
	}
	evts, err := fn(d)
	if err != nil {
		return nil, err
	}
	r.deliveries[id] = d
	r.events = append(r.events, evts...)
	return d, nil
}

func (r *fakeRepo) Courier(_ context.Context, _ app.Scope, id string) (domain.Courier, error) {
	c, ok := r.couriers[id]
	if !ok {
		return domain.Courier{}, app.ErrNotFound
	}
	return c, nil
}

func (r *fakeRepo) UpsertCourier(_ context.Context, _ app.Scope, c domain.Courier, evts []domain.Event) (domain.Courier, bool, error) {
	for id, e := range r.couriers {
		if e.ExternalID == c.ExternalID {
			c.ID, c.CreatedAt = id, e.CreatedAt
			r.couriers[id] = c
			r.events = append(r.events, evts...)
			return c, false, nil
		}
	}
	r.couriers[c.ID] = c
	r.events = append(r.events, evts...)
	return c, true, nil
}

func (r *fakeRepo) DuePickupWatches(_ context.Context, _ time.Time, limit int) ([]app.PickupWatch, error) {
	r.dueLimit = limit
	return r.due, nil
}

func (r *fakeRepo) WarnPickupPending(_ context.Context, w app.PickupWatch, _ time.Time) error {
	r.warned = append(r.warned, w.DeliveryID)
	return r.warnErrs[w.DeliveryID]
}
