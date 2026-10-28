package redis

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/raduolivas/dt/backend/pkg/viewerjwt"
	"github.com/raduolivas/dt/backend/services/tracking/internal/app"
)

// Snapshot polling limit (contract: 30 requests a minute per viewer token).
const (
	snapshotLimit  = 30
	snapshotWindow = time.Minute
)

// ViewerLinks reads the gateway's link deny-list (dt:viewer:revoked:<tl_id>)
// and expiries (dt:viewer:expiry:<tl_id>), and counts snapshot polls.
type ViewerLinks struct{ rdb *goredis.Client }

var _ app.ViewerLinks = (*ViewerLinks)(nil)

// NewViewerLinks returns the reader over rdb.
func NewViewerLinks(rdb *goredis.Client) *ViewerLinks { return &ViewerLinks{rdb: rdb} }

// Revoked implements app.ViewerLinks.
func (v *ViewerLinks) Revoked(ctx context.Context, linkID string) (bool, error) {
	n, err := v.rdb.Exists(ctx, viewerjwt.RevokedKey(linkID)).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// Expiry implements app.ViewerLinks.
func (v *ViewerLinks) Expiry(ctx context.Context, linkID string) (time.Time, bool, error) {
	s, err := v.rdb.Get(ctx, viewerjwt.ExpiryKey(linkID)).Result()
	if errors.Is(err, goredis.Nil) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	sec, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("link expiry: malformed value")
	}
	return time.Unix(sec, 0).UTC(), true, nil
}

var limitScript = goredis.NewScript(`
local n = redis.call('INCR', KEYS[1])
if n == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[1]) end
local ttl = redis.call('PTTL', KEYS[1])
if ttl < 0 then redis.call('PEXPIRE', KEYS[1], ARGV[1]); ttl = tonumber(ARGV[1]) end
return {n, ttl}
`)

// AllowSnapshot implements app.ViewerLinks with a fixed one-minute window
// at rl:viewer:snap:<key>.
func (v *ViewerLinks) AllowSnapshot(ctx context.Context, key string) (bool, time.Duration, error) {
	res, err := limitScript.Run(ctx, v.rdb, []string{"rl:viewer:snap:" + key}, snapshotWindow.Milliseconds()).Int64Slice()
	if err != nil {
		return false, 0, err
	}
	if len(res) != 2 {
		return false, 0, errors.New("snapshot limiter: unexpected reply")
	}
	if res[0] <= snapshotLimit {
		return true, 0, nil
	}
	return false, time.Duration(res[1]) * time.Millisecond, nil
}
