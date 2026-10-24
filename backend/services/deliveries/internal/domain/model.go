package domain

import "math"

// Model is a delivery model (spec §4.1).
type Model string

// Delivery models.
const (
	ModelDirect   Model = "direct"
	ModelEndToEnd Model = "end_to_end"
)

func (m Model) valid() bool { return m == ModelDirect || m == ModelEndToEnd }

// Location is a WGS84 coordinate.
type Location struct {
	Lat, Lng float64
}

func (l Location) valid() bool {
	return !math.IsNaN(l.Lat) && !math.IsNaN(l.Lng) &&
		l.Lat >= -90 && l.Lat <= 90 && l.Lng >= -180 && l.Lng <= 180
}

const earthRadiusKm = 6371.0

// distanceKm is the haversine distance between a and b.
func distanceKm(a, b Location) float64 {
	rad := func(d float64) float64 { return d * math.Pi / 180 }
	dLat, dLng := rad(b.Lat-a.Lat), rad(b.Lng-a.Lng)
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(rad(a.Lat))*math.Cos(rad(b.Lat))*math.Sin(dLng/2)*math.Sin(dLng/2)
	return 2 * earthRadiusKm * math.Asin(math.Min(1, math.Sqrt(h)))
}
