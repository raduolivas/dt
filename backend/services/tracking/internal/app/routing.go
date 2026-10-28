package app

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

const (
	// offRouteM is how far a courier may be from the route before it is
	// rebuilt (design §5).
	offRouteM = 50.0
	// routeMinInterval spaces ETA recomputes and router calls per delivery.
	routeMinInterval = 10 * time.Second
)

// Router profiles.
const (
	profileCar  = "car"
	profileBike = "bike"
)

// profileFor maps a courier's vehicle to a routing profile. on_foot uses the
// bike graph (at walking speed in the fallback). Without a courier, or an
// unknown vehicle, the bike graph is used.
func profileFor(c *domain.Courier) string {
	if c != nil {
		switch c.Vehicle {
		case "car", "scooter":
			return profileCar
		}
	}
	return profileBike
}

// typicalKmh is the fallback speed by vehicle when the router is down.
func typicalKmh(c *domain.Courier) float64 {
	if c != nil {
		switch c.Vehicle {
		case "car":
			return 30
		case "scooter":
			return 25
		case "e_bike":
			return 18
		case "on_foot":
			return 5
		}
	}
	return 15 // bicycle
}

// routeMemo remembers the router's answers during one Apply, so a retry after
// a version conflict does not ask (or wait for) the router again.
type routeMemo struct {
	mu sync.Mutex
	m  map[string]routeAnswer
}

type routeAnswer struct {
	path []domain.LatLng
	sec  int
	err  error
}

func (s *Service) route(ctx context.Context, memo *routeMemo, profile string, from, to domain.LatLng) ([]domain.LatLng, int, error) {
	key := profile + "|" + fmtPt(from) + "|" + fmtPt(to)
	memo.mu.Lock()
	a, ok := memo.m[key]
	memo.mu.Unlock()
	if ok {
		return a.path, a.sec, a.err
	}
	path, sec, err := s.Router.Route(ctx, profile, from, to)
	memo.mu.Lock()
	if memo.m == nil {
		memo.m = map[string]routeAnswer{}
	}
	memo.m[key] = routeAnswer{path, sec, err}
	memo.mu.Unlock()
	return path, sec, err
}

func fmtPt(p domain.LatLng) string {
	return string(appendFloat(appendFloat(nil, p.Lat), p.Lng))
}

// refreshRoute keeps st.Route current (design §5, §5.1):
//   - it builds the dropoff leg from the store, and again from the courier
//     once the parcel is picked up and the courier has a position;
//   - in End-to-end it builds the courier-to-store leg before pickup;
//   - it rebuilds a leg when the courier is more than 50 m off it, at most
//     every 10 s;
//   - it recomputes the drive times from the courier's progress along the
//     legs at most every 10 s, with no router call;
//   - when the router fails it keeps the last path (or a straight line) and
//     estimates the time at the vehicle's typical speed, flagged Estimated.
func (s *Service) refreshRoute(ctx context.Context, memo *routeMemo, st *domain.State, now time.Time) {
	if s.Router == nil || st.Status.IsFinal() {
		return
	}
	r := &st.Route
	profile := profileFor(st.Courier)
	kmh := typicalKmh(st.Courier)
	fix := (*domain.LatLng)(nil)
	if st.LastFix != nil {
		fix = &st.LastFix.Pos
	}
	rerouteOK := r.RoutedAt.IsZero() || now.Sub(r.RoutedAt) >= routeMinInterval

	// Dropoff leg: from the store, then from the courier once picked up.
	stage, origin := domain.RouteFromStore, st.Pickup
	if (st.Status == domain.StatusPickedUp || st.Status == domain.StatusArriving) && fix != nil {
		stage, origin = domain.RouteFromCourier, *fix
	}
	// A new stage or profile builds at once; the other reasons wait for the
	// interval since the router was last asked, so a failing router or GPS
	// noise cannot cause a burst of calls.
	if len(r.ToDropoff) == 0 || r.DropoffStage != stage || r.Profile != profile ||
		(rerouteOK && (r.DropoffEstimated ||
			(stage == domain.RouteFromCourier && domain.OffRouteM(r.ToDropoff, origin) > offRouteM))) {
		s.buildLeg(ctx, memo, profile, kmh, origin, st.Dropoff, stage, r, true, now)
	}

	// Courier-to-store leg (End-to-end, before pickup).
	wantPickup := st.Model == domain.ModelEndToEnd && st.Courier != nil && fix != nil &&
		st.Status.BeforePickup() && domain.CourierVisible(st.Model, st.Status)
	if wantPickup && (len(r.ToPickup) == 0 || r.Profile != profile ||
		(rerouteOK && (r.PickupEstimated || domain.OffRouteM(r.ToPickup, *fix) > offRouteM))) {
		s.buildLeg(ctx, memo, profile, kmh, *fix, st.Pickup, "", r, false, now)
	}
	r.Profile = profile
	r.Estimated = r.DropoffEstimated || (wantPickup && r.PickupEstimated)

	// Drive times follow the courier along the legs, at most every 10 s.
	if now.Sub(r.ComputedAt) >= routeMinInterval && fix != nil {
		r.ComputedAt = now
		if stage == domain.RouteFromCourier {
			r.ToDropoffSec = remainingSec(r.ToDropoff, *fix, r.BaseDropoffSec, st.Dropoff, kmh, r.DropoffEstimated)
		}
		if wantPickup {
			r.ToPickupSec = remainingSec(r.ToPickup, *fix, r.BasePickupSec, st.Pickup, kmh, r.PickupEstimated)
		}
	}
}

// buildLeg asks the router for a leg from -> to and stores it in r (the
// dropoff leg, else the pickup leg). On an error it keeps the last path when
// the leg is still the same, else draws a straight line, and estimates the
// time at the typical speed.
func (s *Service) buildLeg(ctx context.Context, memo *routeMemo, profile string, kmh float64, from, to domain.LatLng, stage string, r *domain.Route, dropoff bool, now time.Time) {
	path, sec, err := s.route(ctx, memo, profile, from, to)
	r.RoutedAt, r.ComputedAt = now, now
	failed := err != nil || len(path) < 2
	if failed {
		s.log().WarnContext(ctx, "route_fallback", "profile", profile, "error", err)
		sec = straightSec(from, to, kmh)
	}
	if dropoff {
		sameLeg := r.DropoffStage == stage && r.Profile == profile
		switch {
		case !failed:
			r.ToDropoff = path
		case !sameLeg || len(r.ToDropoff) < 2:
			r.ToDropoff = []domain.LatLng{from, to}
		}
		r.ToDropoffSec, r.BaseDropoffSec, r.DropoffStage, r.DropoffEstimated = sec, sec, stage, failed
		return
	}
	switch {
	case !failed:
		r.ToPickup = path
	case len(r.ToPickup) < 2 || r.Profile != profile:
		r.ToPickup = []domain.LatLng{from, to}
	}
	r.ToPickupSec, r.BasePickupSec, r.PickupEstimated = sec, sec, failed
}

func straightSec(from, to domain.LatLng, kmh float64) int {
	return int(domain.DistanceM(from, to) / (kmh / 3.6))
}

// remainingSec is the leg's drive time for a courier at p: the router's time
// for the whole leg scaled by the share of the path left, or, for an
// estimated leg, the straight line at the typical speed.
func remainingSec(path []domain.LatLng, p domain.LatLng, baseSec int, end domain.LatLng, kmh float64, estimated bool) int {
	if estimated {
		return straightSec(p, end, kmh)
	}
	total := domain.PathLengthM(path)
	if total <= 0 {
		return 0
	}
	rem := domain.RemainingAlongM(path, p)
	if rem >= total {
		return baseSec
	}
	return int(float64(baseSec) * rem / total)
}

func appendFloat(b []byte, f float64) []byte {
	return strconv.AppendFloat(append(b, ','), f, 'f', 6, 64)
}
