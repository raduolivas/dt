package domain

import (
	"math"
	"time"
)

// Pair is a `[lat, lng]` route point.
type Pair [2]float64

// EtaWindowDTO is the `eta_window` object.
type EtaWindowDTO struct {
	MinSeconds int `json:"min_seconds"`
	MaxSeconds int `json:"max_seconds"`
}

// PlaceDTO is the pickup object.
type PlaceDTO struct {
	Location LatLng `json:"location"`
	Name     string `json:"name"`
}

// DropoffDTO is the dropoff object.
type DropoffDTO struct {
	Location LatLng  `json:"location"`
	Label    *string `json:"label"`
}

// RouteDTO is `TrackingRoute`.
type RouteDTO struct {
	ToPickup  []Pair `json:"to_pickup"`
	ToDropoff []Pair `json:"to_dropoff"`
}

// TimelineEntryDTO is `TrackingTimelineEntry`.
type TimelineEntryDTO struct {
	Status Status    `json:"status"`
	At     time.Time `json:"at"`
}

// TrackingCourierDTO is `TrackingCourier`.
type TrackingCourierDTO struct {
	DisplayName string     `json:"display_name"`
	Vehicle     string     `json:"vehicle"`
	PhotoURL    *string    `json:"photo_url"`
	Position    *LatLng    `json:"position"`
	HeadingDeg  *float64   `json:"heading_deg"`
	SpeedKmh    *float64   `json:"speed_kmh"`
	PositionAt  *time.Time `json:"position_at"`
	Signal      Signal     `json:"signal"`
}

// TrackingSnapshot is what the customer's tracking page shows.
type TrackingSnapshot struct {
	DeliveryID         string              `json:"delivery_id"`
	DeliveryModel      Model               `json:"delivery_model"`
	Status             Status              `json:"status"`
	Courier            *TrackingCourierDTO `json:"courier"`
	Pickup             PlaceDTO            `json:"pickup"`
	Dropoff            DropoffDTO          `json:"dropoff"`
	Route              RouteDTO            `json:"route"`
	EtaSeconds         *int                `json:"eta_seconds"`
	EtaWindow          *EtaWindowDTO       `json:"eta_window"`
	EtaState           EtaState            `json:"eta_state"`
	EtaQuality         string              `json:"eta_quality"`
	DistanceRemainingM *float64            `json:"distance_remaining_m"`
	Progress           *float64            `json:"progress"`
	Timeline           []TimelineEntryDTO  `json:"timeline"`
	UpdatedAt          time.Time           `json:"updated_at"`
	Version            int64               `json:"version"`
}

func pairs(pts []LatLng) []Pair {
	out := make([]Pair, len(pts))
	for i, p := range pts {
		out[i] = Pair{p.Lat, p.Lng}
	}
	return out
}

func courierDTO(st State, now time.Time) *TrackingCourierDTO {
	if st.Courier == nil {
		return nil
	}
	c := &TrackingCourierDTO{
		DisplayName: st.Courier.DisplayName,
		Vehicle:     st.Courier.Vehicle,
		PhotoURL:    st.Courier.PhotoURL,
		Signal:      SignalAt(st, now),
	}
	if f := st.LastFix; f != nil {
		p := f.Pos
		c.Position = &p
		c.HeadingDeg = f.HeadingDeg
		if f.SpeedMps != nil {
			k := *f.SpeedMps * 3.6
			c.SpeedKmh = &k
		}
		at := f.T
		c.PositionAt = &at
	}
	return c
}

func timelineDTO(tl []TimelineEntry) []TimelineEntryDTO {
	out := make([]TimelineEntryDTO, len(tl))
	for i, e := range tl {
		out[i] = TimelineEntryDTO(e)
	}
	return out
}

// distanceAndProgress derives distance_remaining_m and progress. It is null
// before pickup and while the status has no basis for it.
func distanceAndProgress(st State) (dist, progress *float64) {
	f := func(v float64) *float64 { return &v }
	switch st.Status {
	case StatusDelivered:
		return f(0), f(1)
	case StatusPickedUp, StatusArriving:
		total := pathLengthM(st.Route.ToDropoff)
		rem := total
		if st.LastFix != nil {
			rem = remainingAlong(st.Route.ToDropoff, st.LastFix.Pos)
		}
		if total <= 0 {
			return f(rem), nil
		}
		return f(rem), f(math.Max(0, math.Min(1, 1-rem/total)))
	}
	return nil, nil
}

func etaQuality(st State, now time.Time) string {
	if st.Route.Estimated {
		return "estimated"
	}
	if st.Courier != nil && CourierVisible(st.Model, st.Status) {
		if s := SignalAt(st, now); s == SignalStale || s == SignalLost {
			return "estimated"
		}
	}
	return "osrm"
}

type common struct {
	route              RouteDTO
	eta                *int
	window             *EtaWindowDTO
	etaState           EtaState
	quality            string
	distance, progress *float64
	timeline           []TimelineEntryDTO
	pickup             PlaceDTO
	dropoff            DropoffDTO
}

func buildCommon(st State, now time.Time, showToPickup bool) common {
	eta, win, es := ComputeETA(st, now)
	var w *EtaWindowDTO
	if win != nil {
		w = &EtaWindowDTO{MinSeconds: win[0], MaxSeconds: win[1]}
	}
	r := RouteDTO{ToDropoff: pairs(st.Route.ToDropoff)}
	if showToPickup && st.Model == ModelEndToEnd && st.Status.BeforePickup() && len(st.Route.ToPickup) > 0 {
		r.ToPickup = pairs(st.Route.ToPickup)
	}
	d, p := distanceAndProgress(st)
	return common{
		route: r, eta: eta, window: w, etaState: es, quality: etaQuality(st, now),
		distance: d, progress: p, timeline: timelineDTO(st.Timeline),
		pickup:  PlaceDTO{Location: st.Pickup, Name: st.PickupName},
		dropoff: DropoffDTO{Location: st.Dropoff},
	}
}

// Snapshot projects st for the customer. Hidden courier data is never copied:
// the courier and the route to the pickup are null when the courier is not
// visible for this delivery's model and status (MVP spec §4.5).
func Snapshot(st State, now time.Time) TrackingSnapshot {
	visible := CourierVisible(st.Model, st.Status)
	c := buildCommon(st, now, visible)
	s := TrackingSnapshot{
		DeliveryID: st.DeliveryID, DeliveryModel: st.Model, Status: st.Status,
		Pickup: c.pickup, Dropoff: c.dropoff, Route: c.route,
		EtaSeconds: c.eta, EtaWindow: c.window, EtaState: c.etaState, EtaQuality: c.quality,
		DistanceRemainingM: c.distance, Progress: c.progress, Timeline: c.timeline,
		UpdatedAt: st.UpdatedAt, Version: st.ViewerVersion,
	}
	if visible {
		s.Courier = courierDTO(st, now)
	}
	return s
}
