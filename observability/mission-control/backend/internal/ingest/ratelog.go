package ingest

import (
	"log/slog"
	"sync"
	"time"
)

// rateLog logs at most once per minute; the rest are skipped.
type rateLog struct {
	mu   sync.Mutex
	last time.Time
}

func (r *rateLog) log(l *slog.Logger, msg string, args ...any) {
	r.mu.Lock()
	ok := time.Since(r.last) >= time.Minute
	if ok {
		r.last = time.Now()
	}
	r.mu.Unlock()
	if ok {
		l.Warn(msg, args...)
	}
}
