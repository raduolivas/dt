package domain

import (
	"errors"
	"math"
	"time"
)

// FixAction is what to do with a fix.
type FixAction int

// Fix actions.
const (
	// Accept updates the live position and the trail.
	Accept FixAction = iota
	// TrailOnly stores the fix in the trail without moving the live position.
	TrailOnly
	// Drop discards the fix.
	Drop
)

func (a FixAction) String() string {
	switch a {
	case Accept:
		return "accept"
	case TrailOnly:
		return "trail_only"
	default:
		return "drop"
	}
}

// FixVerdict is the outcome of CheckFix.
type FixVerdict struct {
	Action FixAction
	Reason string
}

// Plausibility limits.
const (
	maxFutureSkew = 5 * time.Second
	maxFixAge     = 2 * time.Minute
	maxSpeedMps   = 200.0 / 3.6
	jumpDistanceM = 1000.0
	jumpWindow    = 10 * time.Second
)

// ErrInvalidFix is returned for a fix with non-finite or out-of-range values.
var ErrInvalidFix = errors.New("invalid fix")

// CheckFix judges f against the last accepted fix prev (may be nil) and the
// time it was received. The error is non-nil only for malformed input, with
// verdict Drop.
func CheckFix(prev *Fix, f Fix, receivedAt time.Time) (FixVerdict, error) {
	if !validLatLng(f.Pos) || f.T.IsZero() || math.IsNaN(f.AccuracyM) || f.AccuracyM < 0 {
		return FixVerdict{Drop, "invalid"}, ErrInvalidFix
	}
	if f.T.Sub(receivedAt) > maxFutureSkew {
		return FixVerdict{Drop, "future"}, nil
	}
	if f.SpeedMps != nil && *f.SpeedMps > maxSpeedMps {
		return FixVerdict{Drop, "speed"}, nil
	}
	if prev != nil {
		if dt := f.T.Sub(prev.T); dt > 0 {
			d := distanceM(prev.Pos, f.Pos)
			if d > jumpDistanceM && dt < jumpWindow {
				return FixVerdict{Drop, "jump"}, nil
			}
			if d/dt.Seconds() > maxSpeedMps {
				return FixVerdict{Drop, "speed"}, nil
			}
		} else {
			return FixVerdict{TrailOnly, "out_of_order"}, nil
		}
	}
	if receivedAt.Sub(f.T) > maxFixAge {
		return FixVerdict{TrailOnly, "old"}, nil
	}
	return FixVerdict{Accept, ""}, nil
}

func validLatLng(p LatLng) bool {
	return !math.IsNaN(p.Lat) && !math.IsNaN(p.Lng) &&
		p.Lat >= -90 && p.Lat <= 90 && p.Lng >= -180 && p.Lng <= 180
}
