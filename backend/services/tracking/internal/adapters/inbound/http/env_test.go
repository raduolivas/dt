package httpapi_test

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	goredis "github.com/redis/go-redis/v9"

	"github.com/raduolivas/dt/backend/pkg/bootstrap"
	"github.com/raduolivas/dt/backend/pkg/testkit"
	"github.com/raduolivas/dt/backend/pkg/viewerjwt"
	httpapi "github.com/raduolivas/dt/backend/services/tracking/internal/adapters/inbound/http"
	"github.com/raduolivas/dt/backend/services/tracking/internal/adapters/outbound/redis"
	"github.com/raduolivas/dt/backend/services/tracking/internal/app"
	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

const (
	delA  = "del_01j9z3k8m2q4r6s8t0v2w4x6aa"
	delB  = "del_01j9z3k8m2q4r6s8t0v2w4x6bb"
	linkA = "tl_01j9z3k8m2q4r6s8t0v2w4x6aa"
	linkB = "tl_01j9z3k8m2q4r6s8t0v2w4x6bb"
)

var (
	scA     = app.Scope{TenantID: "ten_a", Env: "test"}
	claimsA = viewerjwt.Claims{DeliveryID: delA, TenantID: scA.TenantID, Env: scA.Env, LinkID: linkA}
)

// memSnapshots is the viewer snapshot source: one snapshot per delivery.
type memSnapshots struct {
	mu sync.Mutex
	m  map[string]scoped
}

type scoped struct {
	sc app.Scope
	s  domain.TrackingSnapshot
}

func (m *memSnapshots) ViewerSnapshot(_ context.Context, sc app.Scope, id string) (domain.TrackingSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.m[id]
	if !ok || v.sc != sc {
		return domain.TrackingSnapshot{}, app.ErrNotFound
	}
	return v.s, nil
}

type env struct {
	t      *testing.T
	rdb    *goredis.Client
	signer *viewerjwt.Signer
	snaps  *memSnapshots
	pub    *redis.Publisher
	hub    *httpapi.Hub
	srv    *httptest.Server
	stop   context.CancelFunc
	done   chan error
}

func newEnv(t *testing.T, tweak ...func(*httpapi.Deps)) *env {
	t.Helper()
	opts, err := bootstrap.RedisOptions(testkit.StartRedis(t))
	if err != nil {
		t.Fatal(err)
	}
	rdb := goredis.NewClient(opts)
	t.Cleanup(func() { _ = rdb.Close() })
	seed := make([]byte, ed25519.SeedSize)
	_, _ = rand.Read(seed)
	signer, err := viewerjwt.ParseKeys("k1:"+base64.StdEncoding.EncodeToString(seed), "k1")
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, rdb: rdb, signer: signer, snaps: &memSnapshots{m: map[string]scoped{}}, pub: redis.NewPublisher(rdb)}
	d := httpapi.Deps{
		Verifier: signer.Verifier(), Snapshots: e.snaps, Links: redis.NewViewerLinks(rdb), Feed: redis.NewFeed(rdb, nil),
		AuthTimeout: 500 * time.Millisecond, SwapInterval: 100 * time.Millisecond,
	}
	for _, f := range tweak {
		f(&d)
	}
	e.hub = httpapi.NewHub(d)
	mux := http.NewServeMux()
	e.hub.Mount(mux)
	e.srv = httptest.NewServer(mux)
	ctx, cancel := context.WithCancel(context.Background())
	e.stop, e.done = cancel, make(chan error, 1)
	go func() { e.done <- e.hub.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-e.done; err != nil {
			t.Errorf("hub Run = %v", err)
		}
		e.srv.Close()
	})
	e.set(delA, scA, snapshot(delA, 10, domain.StatusPickedUp, true))
	return e
}

func snapshot(id string, v int64, st domain.Status, courier bool) domain.TrackingSnapshot {
	s := domain.TrackingSnapshot{DeliveryID: id, DeliveryModel: domain.ModelDirect, Status: st, Version: v,
		Timeline: []domain.TimelineEntryDTO{{Status: st, At: time.Now().UTC()}}}
	if courier {
		p := domain.LatLng{Lat: 52.37, Lng: 4.9}
		s.Courier = &domain.TrackingCourierDTO{DisplayName: "Sam", Vehicle: "e_bike", Position: &p, Signal: domain.SignalLive}
	}
	return s
}

// set stores the delivery's current snapshot (what a new stream reads).
func (e *env) set(id string, sc app.Scope, s domain.TrackingSnapshot) {
	e.snaps.mu.Lock()
	e.snaps.m[id] = scoped{sc, s}
	e.snaps.mu.Unlock()
}

// publish stores and fans out a snapshot, as Apply does.
func (e *env) publish(s domain.TrackingSnapshot) {
	e.t.Helper()
	e.snaps.mu.Lock()
	cur := e.snaps.m[s.DeliveryID]
	if s.Version > cur.s.Version {
		e.snaps.m[s.DeliveryID] = scoped{cur.sc, s}
	}
	e.snaps.mu.Unlock()
	if err := e.pub.Snapshot(context.Background(), s.DeliveryID, s); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) token(c viewerjwt.Claims) string {
	e.t.Helper()
	tok, _, err := e.signer.Sign(c, time.Now())
	if err != nil {
		e.t.Fatal(err)
	}
	return tok
}

// revoke does what the gateway's Revoker does.
func (e *env) revoke(linkID string) {
	ctx := context.Background()
	e.rdb.Set(ctx, viewerjwt.RevokedKey(linkID), "1", viewerjwt.RevokedTTL)
	e.rdb.Publish(ctx, viewerjwt.RevocationsChannel, linkID)
}

// expire does what the gateway's AnnounceExpiry does.
func (e *env) expire(linkID string, at time.Time) {
	ctx := context.Background()
	e.rdb.Set(ctx, viewerjwt.ExpiryKey(linkID), at.Unix(), viewerjwt.ExpiryKeyTTL(at, time.Now()))
	e.rdb.Publish(ctx, viewerjwt.ExpiriesChannel, viewerjwt.EncodeExpiry(linkID, at))
}

type msg struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

func (m msg) snapshot(t *testing.T) domain.TrackingSnapshot {
	t.Helper()
	var s domain.TrackingSnapshot
	if m.Type != "snapshot" {
		t.Fatalf("message %s, want snapshot", m.Type)
	}
	if err := json.Unmarshal(m.Data, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (e *env) dial() *websocket.Conn {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, res, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.srv.URL, "http")+"/v1/tracking/stream",
		&websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"https://partner.example"}}})
	if err != nil {
		e.t.Fatalf("dial: %v", err)
	}
	if res != nil && res.Body != nil {
		_ = res.Body.Close()
	}
	e.t.Cleanup(func() { _ = c.CloseNow() })
	return c
}

func send(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func auth(t *testing.T, c *websocket.Conn, tok string) {
	t.Helper()
	send(t, c, map[string]string{"type": "auth", "token": tok})
}

// read returns the next server message other than ping, or the close
// status when the stream closed (-1 when none).
func read(t *testing.T, c *websocket.Conn, within time.Duration) (msg, websocket.StatusCode) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	for {
		_, b, err := c.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				t.Fatalf("no message within %v", within)
			}
			return msg{}, websocket.CloseStatus(err)
		}
		var m msg
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("bad message %q", b)
		}
		if m.Type != "ping" {
			return m, -1
		}
	}
}

func mustRead(t *testing.T, c *websocket.Conn) msg {
	t.Helper()
	m, code := read(t, c, 5*time.Second)
	if code != -1 {
		t.Fatalf("stream closed with %d, want a message", code)
	}
	return m
}

func mustClose(t *testing.T, c *websocket.Conn, want websocket.StatusCode, within time.Duration) {
	t.Helper()
	m, code := read(t, c, within)
	if code != want {
		t.Fatalf("got message %s / close %d, want close %d", m.Type, code, want)
	}
}

// sse opens the SSE stream and returns its events, one per message.
func (e *env) sse(tok string) (<-chan msg, response) {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/v1/tracking/stream/sse", nil)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = res.Body.Close() })
	out := make(chan msg, 64)
	meta := response{StatusCode: res.StatusCode, Header: res.Header}
	if res.StatusCode != http.StatusOK {
		close(out)
		return out, meta
	}
	go func() {
		defer close(out)
		sc := bufio.NewScanner(res.Body)
		var event, data string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			case line == "" && event != "":
				var m msg
				_ = json.Unmarshal([]byte(data), &m)
				if m.Type != event {
					m.Type = "mismatch:" + event
				}
				if m.Type != "ping" {
					out <- m
				}
				event, data = "", ""
			}
		}
	}()
	return out, meta
}

func next(t *testing.T, ch <-chan msg) (msg, bool) {
	t.Helper()
	select {
	case m, ok := <-ch:
		return m, ok
	case <-time.After(5 * time.Second):
		t.Fatal("no SSE event within 5 s")
	}
	return msg{}, false
}
