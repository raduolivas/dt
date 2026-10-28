package domain

import "time"

const proofMaxAge = 2 * time.Minute

// ProofPosition is the latest fix at or before at, no more than 2 minutes
// older, or nil. The trail need not be sorted.
func ProofPosition(trail []Fix, at time.Time) *Fix {
	var best *Fix
	for i := range trail {
		f := &trail[i]
		if f.T.After(at) || at.Sub(f.T) > proofMaxAge {
			continue
		}
		if best == nil || f.T.After(best.T) {
			best = f
		}
	}
	if best == nil {
		return nil
	}
	c := *best
	return &c
}
