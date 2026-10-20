package redislimit

import (
	"context"
	"time"

	"github.com/go-redis/redis_rate/v10"
	"github.com/redis/go-redis/v9"

	"github.com/raduolivas/dt/backend/services/gateway/internal/app"
)

// callTimeout bounds every limiter call so a slow Redis cannot slow the API.
const callTimeout = 50 * time.Millisecond

type limiter struct{ rl *redis_rate.Limiter }

// NewLimiter returns an app.Limiter backed by c. Each call is cut off after
// 50 ms; the error is the caller's cue to fail open.
func NewLimiter(c *redis.Client) app.Limiter {
	return &limiter{rl: redis_rate.NewLimiter(c)}
}

func (l *limiter) Allow(ctx context.Context, key string, lim app.Limit) (app.Decision, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	res, err := l.rl.Allow(ctx, key, redis_rate.Limit{Rate: lim.Rate, Burst: lim.Burst, Period: lim.Period})
	if err != nil {
		return app.Decision{}, err
	}
	d := app.Decision{
		Allowed:    res.Allowed > 0,
		Limit:      lim.Burst,
		Remaining:  res.Remaining,
		ResetAfter: max(res.ResetAfter, 0),
	}
	if !d.Allowed {
		d.RetryAfter = max(res.RetryAfter, 0)
	}
	return d, nil
}
