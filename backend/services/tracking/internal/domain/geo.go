package domain

import "math"

const earthRadiusM = 6371000.0

func rad(d float64) float64 { return d * math.Pi / 180 }

// distanceM is the haversine distance between a and b in metres.
func distanceM(a, b LatLng) float64 {
	dLat := rad(b.Lat - a.Lat)
	dLng := rad(b.Lng - a.Lng)
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(rad(a.Lat))*math.Cos(rad(b.Lat))*math.Sin(dLng/2)*math.Sin(dLng/2)
	return 2 * earthRadiusM * math.Asin(math.Min(1, math.Sqrt(h)))
}

// xy is a point in a local equirectangular projection, in metres.
type xy struct{ x, y float64 }

func project(p, origin LatLng) xy {
	return xy{
		x: rad(p.Lng-origin.Lng) * earthRadiusM * math.Cos(rad(origin.Lat)),
		y: rad(p.Lat-origin.Lat) * earthRadiusM,
	}
}

// segmentDistance returns the distance from p to segment ab and the fraction
// t in [0,1] of the closest point along ab.
func segmentDistance(p, a, b xy) (dist, t float64) {
	dx, dy := b.x-a.x, b.y-a.y
	l2 := dx*dx + dy*dy
	if l2 > 0 {
		t = ((p.x-a.x)*dx + (p.y-a.y)*dy) / l2
		t = math.Max(0, math.Min(1, t))
	}
	cx, cy := a.x+t*dx, a.y+t*dy
	return math.Hypot(p.x-cx, p.y-cy), t
}

func pathLengthM(pts []LatLng) float64 {
	var s float64
	for i := 1; i < len(pts); i++ {
		s += distanceM(pts[i-1], pts[i])
	}
	return s
}

// remainingAlong returns the metres left on route for a traveller at p: the
// distance to the closest point on the route plus the rest of the route.
func remainingAlong(route []LatLng, p LatLng) float64 {
	if len(route) == 0 {
		return 0
	}
	if len(route) == 1 {
		return distanceM(p, route[0])
	}
	best, bestIdx, bestT := math.Inf(1), 0, 0.0
	pp := project(p, p)
	for i := 0; i+1 < len(route); i++ {
		d, t := segmentDistance(pp, project(route[i], p), project(route[i+1], p))
		if d < best {
			best, bestIdx, bestT = d, i, t
		}
	}
	segLen := distanceM(route[bestIdx], route[bestIdx+1])
	return best + segLen*(1-bestT) + pathLengthM(route[bestIdx+1:])
}

// Simplify reduces points with Douglas-Peucker, keeping the endpoints and
// every dropped point within toleranceM metres of the result.
func Simplify(points []LatLng, toleranceM float64) []LatLng {
	if len(points) <= 2 {
		return append([]LatLng(nil), points...)
	}
	keep := make([]bool, len(points))
	keep[0], keep[len(points)-1] = true, true
	origin := points[0]
	proj := make([]xy, len(points))
	for i, p := range points {
		proj[i] = project(p, origin)
	}
	type span struct{ lo, hi int }
	stack := []span{{0, len(points) - 1}}
	for len(stack) > 0 {
		s := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		worst, idx := -1.0, -1
		for i := s.lo + 1; i < s.hi; i++ {
			d, _ := segmentDistance(proj[i], proj[s.lo], proj[s.hi])
			if d > worst {
				worst, idx = d, i
			}
		}
		if idx >= 0 && worst > toleranceM {
			keep[idx] = true
			stack = append(stack, span{s.lo, idx}, span{idx, s.hi})
		}
	}
	out := make([]LatLng, 0, len(points))
	for i, k := range keep {
		if k {
			out = append(out, points[i])
		}
	}
	return out
}
