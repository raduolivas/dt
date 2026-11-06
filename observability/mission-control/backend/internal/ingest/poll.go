package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
)

// PollInterval is the health and consumer poll period (spec §4.1).
const PollInterval = 5 * time.Second

var pollClient = &http.Client{Timeout: 2 * time.Second}

// changes remembers the last state per key and reports whether it differs.
type changes struct {
	mu   sync.Mutex
	last map[string]string
}

func (c *changes) changed(key, state string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last == nil {
		c.last = map[string]string{}
	}
	if old, ok := c.last[key]; ok && old == state {
		return false
	}
	c.last[key] = state
	return true
}

func probe(ctx context.Context, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := pollClient.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// PollHealth probes <base>/livez and <base>/readyz of every target each
// interval and emits one kind=health event per target when its state first
// becomes known and each time it changes. Liveness and readiness stay separate
// fields in attrs.
func PollHealth(ctx context.Context, targets map[string]string, every time.Duration, sink func(store.Event)) {
	var ch changes
	names := make([]string, 0, len(targets))
	for k := range targets {
		names = append(names, k)
	}
	sort.Strings(names)
	tick := func() {
		for _, name := range names {
			base := strings.TrimRight(targets[name], "/")
			live := probe(ctx, base+"/livez")
			ready := probe(ctx, base+"/readyz")
			if ctx.Err() != nil {
				return
			}
			if !ch.changed(name, fmt.Sprint(live, ready)) {
				continue
			}
			now := time.Now().UTC()
			ev := store.Event{ObservedAt: now, OccurredAt: now, Kind: "health", Source: name, Outcome: "ok", Origin: "live",
				Attrs: []byte(fmt.Sprintf(`{"live":%t,"ready":%t}`, live, ready))}
			switch {
			case !live:
				ev.Outcome, ev.Reason = "failed", "not_live"
			case !ready:
				ev.Outcome, ev.Reason = "failed", "not_ready"
			}
			sink(ev)
		}
	}
	every = orDefault(every)
	tick()
	loop(ctx, every, tick)
}

type jsz struct {
	AccountDetails []struct {
		StreamDetail []struct {
			Name           string `json:"name"`
			ConsumerDetail []struct {
				Name          string `json:"name"`
				NumPending    uint64 `json:"num_pending"`
				NumAckPending int    `json:"num_ack_pending"`
			} `json:"consumer_detail"`
		} `json:"stream_detail"`
	} `json:"account_details"`
}

// PollConsumers reads <monitorURL>/jsz?consumers=true each interval and emits a
// kind=health event for a consumer whenever its pending or ack-pending count
// changes. A monitor that cannot be reached emits one failed event for "nats"
// and one ok event when it answers again.
func PollConsumers(ctx context.Context, monitorURL string, every time.Duration, sink func(store.Event)) {
	var ch changes
	url := strings.TrimRight(monitorURL, "/") + "/jsz?consumers=true"
	emit := func(source, outcome, reason string, attrs map[string]any) {
		now := time.Now().UTC()
		b, _ := json.Marshal(attrs)
		sink(store.Event{ObservedAt: now, OccurredAt: now, Kind: "health", Source: source, Outcome: outcome,
			Reason: reason, Origin: "live", Attrs: b})
	}
	tick := func() {
		var out jsz
		if err := getJSON(ctx, url, &out); err != nil {
			if ctx.Err() == nil && ch.changed("monitor", "down") {
				emit("nats", "failed", "monitor_unreachable", map[string]any{})
			}
			return
		}
		if ch.changed("monitor", "up") {
			emit("nats", "ok", "", map[string]any{})
		}
		for _, a := range out.AccountDetails {
			for _, s := range a.StreamDetail {
				for _, c := range s.ConsumerDetail {
					if !ch.changed(s.Name+"/"+c.Name, fmt.Sprint(c.NumPending, c.NumAckPending)) {
						continue
					}
					emit("nats."+s.Name, "ok", "", map[string]any{"consumer": c.Name,
						"pending": c.NumPending, "ack_pending": c.NumAckPending})
				}
			}
		}
	}
	tick()
	loop(ctx, orDefault(every), tick)
}

func getJSON(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := pollClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(v)
}

func orDefault(d time.Duration) time.Duration {
	if d <= 0 {
		return PollInterval
	}
	return d
}

func loop(ctx context.Context, every time.Duration, f func()) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			f()
		}
	}
}
