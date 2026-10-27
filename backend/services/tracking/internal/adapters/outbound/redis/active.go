package redis

import (
	"context"
	"strconv"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/raduolivas/dt/backend/services/tracking/internal/app"
)

// activeKey is dt:active, the sorted set of non-final deliveries with a
// visible courier (ruling M3-R3). A member is "<tenant>|<env>|<del_id>", so the
// sweeper applies each in its own scope; the score is the unix milliseconds
// the signal age counts from.
const activeSetKey = "dt:active"

// activePage is how many entries one ZRANGEBYSCORE returns.
const activePage = 1000

func activeMember(s app.Scope, id string) string { return s.TenantID + "|" + s.Env + "|" + id }

func parseActiveMember(m string) (app.ActiveDelivery, bool) {
	parts := strings.Split(m, "|")
	if len(parts) != 3 || parts[0] == "" || parts[2] == "" || (parts[1] != "test" && parts[1] != "live") {
		return app.ActiveDelivery{}, false
	}
	return app.ActiveDelivery{Scope: app.Scope{TenantID: parts[0], Env: parts[1]}, DeliveryID: parts[2]}, true
}

// SetActive implements app.StateStore.
func (s *States) SetActive(ctx context.Context, sc app.Scope, id string, since time.Time, active bool) error {
	m := activeMember(sc, id)
	if !active {
		return s.rdb.ZRem(ctx, activeSetKey, m).Err()
	}
	return s.rdb.ZAdd(ctx, activeSetKey, goredis.Z{Score: float64(since.UnixMilli()), Member: m}).Err()
}

// ActiveSince implements app.StateStore, oldest first, in pages.
func (s *States) ActiveSince(ctx context.Context, t time.Time) ([]app.ActiveDelivery, error) {
	maxScore := strconv.FormatInt(t.UnixMilli(), 10)
	var out []app.ActiveDelivery
	for offset := int64(0); ; offset += activePage {
		members, err := s.rdb.ZRangeArgs(ctx, goredis.ZRangeArgs{
			Key: activeSetKey, Start: "-inf", Stop: maxScore, ByScore: true, Offset: offset, Count: activePage,
		}).Result()
		if err != nil {
			return nil, err
		}
		for _, m := range members {
			if e, ok := parseActiveMember(m); ok {
				out = append(out, e)
			}
		}
		if len(members) < activePage {
			return out, nil
		}
	}
}

// Lease is an app.Lease on one Redis key (SET NX PX; renewed and released
// only by its holder).
type Lease struct {
	rdb *goredis.Client
	key string
}

var _ app.Lease = (*Lease)(nil)

// Lease keys.
const (
	// SweeperLeaseKey is the signal sweeper's lease.
	SweeperLeaseKey = "dt:lease:sweeper"
	// RetentionLeaseKey is the nightly retention job's lease.
	RetentionLeaseKey = "dt:lease:retention"
)

// NewLease returns the lease stored at key.
func NewLease(rdb *goredis.Client, key string) *Lease { return &Lease{rdb: rdb, key: key} }

var holdScript = goredis.NewScript(`
local cur = redis.call('GET', KEYS[1])
if not cur then
  redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
  return 1
end
if cur == ARGV[1] then
  redis.call('PEXPIRE', KEYS[1], ARGV[2])
  return 1
end
return 0
`)

var releaseScript = goredis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('DEL', KEYS[1]) end
return 0
`)

// Hold implements app.Lease.
func (l *Lease) Hold(ctx context.Context, holder string, ttl time.Duration) (bool, error) {
	n, err := holdScript.Run(ctx, l.rdb, []string{l.key}, holder, ttl.Milliseconds()).Int()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// Release implements app.Lease.
func (l *Lease) Release(ctx context.Context, holder string) error {
	return releaseScript.Run(ctx, l.rdb, []string{l.key}, holder).Err()
}
