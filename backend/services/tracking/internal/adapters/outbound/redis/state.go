// Package redis keeps the tracking live state, the couriers' active
// delivery sets, the location-report sampler and the snapshot fan-out in
// Redis.
package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/raduolivas/dt/backend/services/tracking/internal/app"
	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

// TTLs.
const (
	// FinalStateTTL keeps a finished delivery's state for late readers.
	FinalStateTTL = 24 * time.Hour
	// ActiveStateTTL bounds a state nobody updates; it is rebuilt from
	// Postgres when it expires.
	ActiveStateTTL = 7 * 24 * time.Hour
	// ActiveSetTTL bounds how long a cached courier set can drift from
	// Postgres (a set rebuilt while a consumer transaction was in flight).
	ActiveSetTTL = 5 * time.Minute
	// sampleTTL is the courier.location_reported sampling period.
	sampleTTL = time.Minute
)

// scopeMember marks the tenant and environment a courier set belongs to;
// "@" never starts a delivery id.
const scopeMember = "@scope:"

func stateKey(id string) string          { return "dt:state:" + id }
func activeKey(cou string) string        { return "dt:courier:" + cou + ":active" }
func sampleKey(cou string) string        { return "dt:courier:" + cou + ":reported" }
func snapChannel(id string) string       { return "dt:snap:" + id }
func scopeTag(tenant, env string) string { return scopeMember + tenant + "|" + env }

// CourierSource lists a courier's non-final deliveries from Postgres; it
// returns app.ErrCourierNotFound for a courier outside the scope.
type CourierSource func(ctx context.Context, s app.Scope, courierID string) ([]string, error)

// States is the app.StateStore and app.Sampler on Redis. The state is a
// hash {v: version, s: state JSON} at dt:state:<del_id>.
type States struct {
	rdb    *goredis.Client
	source CourierSource
}

var (
	_ app.StateStore = (*States)(nil)
	_ app.Sampler    = (*States)(nil)
)

// NewStates returns the store; source rebuilds missing courier sets.
func NewStates(rdb *goredis.Client, source CourierSource) *States {
	return &States{rdb: rdb, source: source}
}

// putScript stores the state only when the stored version is ARGV[1] (0 for
// none) and lower than the new one, ARGV[2].
var putScript = goredis.NewScript(`
local cur = tonumber(redis.call('HGET', KEYS[1], 'v') or '0')
local prev = tonumber(ARGV[1])
local new = tonumber(ARGV[2])
if cur ~= prev or cur >= new then return 0 end
redis.call('HSET', KEYS[1], 'v', ARGV[2], 's', ARGV[3])
redis.call('PEXPIRE', KEYS[1], ARGV[4])
return 1
`)

// Get returns the stored state.
func (s *States) Get(ctx context.Context, id string) (domain.State, bool, error) {
	b, err := s.rdb.HGet(ctx, stateKey(id), "s").Bytes()
	if errors.Is(err, goredis.Nil) {
		return domain.State{}, false, nil
	}
	if err != nil {
		return domain.State{}, false, err
	}
	var st domain.State
	if err := json.Unmarshal(b, &st); err != nil {
		return domain.State{}, false, fmt.Errorf("decode state %s: %w", id, err)
	}
	return st, true, nil
}

// Put is a compare-and-set on the version (see app.StateStore).
func (s *States) Put(ctx context.Context, st domain.State, prevVersion int64) (bool, error) {
	if st.Version <= prevVersion {
		return false, fmt.Errorf("put state %s: version %d not above %d", st.DeliveryID, st.Version, prevVersion)
	}
	b, err := json.Marshal(st)
	if err != nil {
		return false, fmt.Errorf("encode state: %w", err)
	}
	ttl := ActiveStateTTL
	if st.Status.IsFinal() {
		ttl = FinalStateTTL
	}
	n, err := putScript.Run(ctx, s.rdb, []string{stateKey(st.DeliveryID)},
		strconv.FormatInt(prevVersion, 10), strconv.FormatInt(st.Version, 10), b, ttl.Milliseconds()).Int()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// ActiveDeliveriesForCourier reads the cached set, rebuilding it from
// Postgres when missing. A set cached for another scope means the courier
// is not this scope's.
func (s *States) ActiveDeliveriesForCourier(ctx context.Context, tenantID, env, courierID string) ([]string, error) {
	members, err := s.rdb.SMembers(ctx, activeKey(courierID)).Result()
	if err != nil {
		return nil, err
	}
	if len(members) == 0 {
		ids, err := s.source(ctx, app.Scope{TenantID: tenantID, Env: env}, courierID)
		if err != nil {
			return nil, err
		}
		vals := make([]any, 0, len(ids)+1)
		vals = append(vals, scopeTag(tenantID, env))
		for _, id := range ids {
			vals = append(vals, id)
		}
		key := activeKey(courierID)
		_, err = s.rdb.TxPipelined(ctx, func(p goredis.Pipeliner) error {
			p.Del(ctx, key)
			p.SAdd(ctx, key, vals...)
			p.PExpire(ctx, key, ActiveSetTTL)
			return nil
		})
		if err != nil {
			return nil, err
		}
		return ids, nil
	}
	tag := scopeTag(tenantID, env)
	ids := make([]string, 0, len(members))
	scoped := false
	for _, m := range members {
		if strings.HasPrefix(m, scopeMember) {
			scoped = m == tag
			continue
		}
		ids = append(ids, m)
	}
	if !scoped {
		return nil, app.ErrCourierNotFound
	}
	return ids, nil
}

// setActiveScript adds (ARGV[2] == "1") or removes ARGV[1] only when the set
// is cached.
var setActiveScript = goredis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return 0 end
if ARGV[2] == '1' then redis.call('SADD', KEYS[1], ARGV[1]) else redis.call('SREM', KEYS[1], ARGV[1]) end
return 1
`)

// SetCourierActive updates a cached set; a missing one is rebuilt on read.
func (s *States) SetCourierActive(ctx context.Context, courierID, deliveryID string, active bool) error {
	flag := "0"
	if active {
		flag = "1"
	}
	return setActiveScript.Run(ctx, s.rdb, []string{activeKey(courierID)}, deliveryID, flag).Err()
}

// Allow reports whether this is the courier's first location report this
// minute (SET NX EX 60).
func (s *States) Allow(ctx context.Context, courierID string) (bool, error) {
	return s.rdb.SetNX(ctx, sampleKey(courierID), 1, sampleTTL).Result()
}
