package app

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

// Trail page sizes (the public API allows 1 to 1000, default 500).
const (
	DefaultTrailLimit = 500
	MaxTrailLimit     = 1000
)

// ErrInvalidArgument means a limit or cursor the caller sent is not valid.
var ErrInvalidArgument = errors.New("invalid argument")

// PartnerView returns the partner's live view of a delivery: the state
// without the visibility filter. A delivery outside sc is ErrNotFound.
func (s *Service) PartnerView(ctx context.Context, sc Scope, deliveryID string) (domain.PartnerViewDTO, error) {
	st, err := s.liveState(ctx, sc, deliveryID)
	if err != nil {
		return domain.PartnerViewDTO{}, err
	}
	return domain.PartnerView(st, s.Clock.Now()), nil
}

// TrailPage is one page of the partner's trail.
type TrailPage struct {
	Fixes   []domain.Fix
	HasMore bool
	// NextCursor is the opaque cursor of the next page; empty on the last.
	NextCursor string
	// RawAvailable is false once the raw fixes are gone; Simplified is then
	// the route (nil when none was stored).
	RawAvailable bool
	Simplified   []domain.LatLng
}

// ListTrail returns the delivery's fixes oldest first, limit at a time
// (0: DefaultTrailLimit), after cursor ("": from the start). A delivery
// outside sc is ErrNotFound; a limit out of range or a cursor that is not
// one this service issued is ErrInvalidArgument.
func (s *Service) ListTrail(ctx context.Context, sc Scope, deliveryID, cursor string, limit int) (TrailPage, error) {
	if limit == 0 {
		limit = DefaultTrailLimit
	}
	if limit < 0 || limit > MaxTrailLimit {
		return TrailPage{}, fmt.Errorf("%w: limit must be 1 to %d", ErrInvalidArgument, MaxTrailLimit)
	}
	var after TrailEntry
	if cursor != "" {
		var err error
		if after, err = decodeCursor(cursor); err != nil {
			return TrailPage{}, err
		}
	}
	rd, err := s.Partner.Trail(ctx, sc, deliveryID, after, limit)
	if err != nil {
		return TrailPage{}, err
	}
	pg := TrailPage{RawAvailable: rd.RawAvailable, Simplified: rd.Simplified}
	entries := rd.Entries
	if len(entries) > limit {
		entries, pg.HasMore = entries[:limit], true
	}
	pg.Fixes = make([]domain.Fix, len(entries))
	for i, e := range entries {
		pg.Fixes[i] = e.Fix
	}
	if pg.HasMore {
		pg.NextCursor = encodeCursor(entries[len(entries)-1])
	}
	return pg, nil
}

// ListProofPoints returns the delivery's proof points in time order. A
// delivery outside sc is ErrNotFound.
func (s *Service) ListProofPoints(ctx context.Context, sc Scope, deliveryID string) ([]ProofPoint, error) {
	return s.Partner.ProofPoints(ctx, sc, deliveryID)
}

// A cursor is the last fix's time (microseconds) and row id, base64url.
func encodeCursor(e TrailEntry) string {
	raw := strconv.FormatInt(e.Fix.T.UnixMicro(), 10) + ":" + strconv.FormatInt(e.ID, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(c string) (TrailEntry, error) {
	bad := fmt.Errorf("%w: starting_after is not a cursor of this API", ErrInvalidArgument)
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return TrailEntry{}, bad
	}
	tm, id, ok := strings.Cut(string(b), ":")
	if !ok {
		return TrailEntry{}, bad
	}
	us, err1 := strconv.ParseInt(tm, 10, 64)
	rid, err2 := strconv.ParseInt(id, 10, 64)
	if err1 != nil || err2 != nil || rid < 0 {
		return TrailEntry{}, bad
	}
	return TrailEntry{ID: rid, Fix: domain.Fix{T: time.UnixMicro(us).UTC()}}, nil
}
