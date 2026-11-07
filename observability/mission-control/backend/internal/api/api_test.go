package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/raduolivas/dt/observability/mission-control/backend/internal/api"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/index"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/store"
	"github.com/raduolivas/dt/observability/mission-control/backend/internal/topology"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test: needs Docker (run without -short)")
	}
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgis/postgis:18-3.6",
		postgres.WithDatabase("mc"), postgres.WithUsername("mc"), postgres.WithPassword("mc"),
		postgres.BasicWaitStrategies())
	testcontainers.CleanupContainer(t, c)
	require.NoError(t, err)
	url, err := c.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	s, err := store.Open(ctx, url)
	require.NoError(t, err)
	t.Cleanup(s.Close)
	return s
}

func newServer(t *testing.T, st *store.Store, bc *api.Broadcaster, mod func(*api.Options)) *httptest.Server {
	t.Helper()
	o := api.Options{Store: st, Topology: topology.Default(), Broadcaster: bc}
	if mod != nil {
		mod(&o)
	}
	srv := httptest.NewServer(api.New(o))
	t.Cleanup(srv.Close)
	return srv
}

func getJSON(t *testing.T, url string, out any) {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode, url)
	require.NoError(t, json.NewDecoder(resp.Body).Decode(out))
}

type sseMsg struct{ ID, Event, Data string }

// openSSE connects and returns a channel of messages (comments are skipped).
func openSSE(t *testing.T, url, lastID string) <-chan sseMsg {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed by the reader goroutine below
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "text/event-stream")
	out := make(chan sseMsg, 1000)
	go func() {
		defer close(out)
		defer func() { _ = resp.Body.Close() }()
		sc := bufio.NewScanner(resp.Body)
		var m sseMsg
		for sc.Scan() {
			l := sc.Text()
			switch {
			case l == "":
				if m.Event != "" {
					out <- m
				}
				m = sseMsg{}
			case strings.HasPrefix(l, "id: "):
				m.ID = l[4:]
			case strings.HasPrefix(l, "event: "):
				m.Event = l[7:]
			case strings.HasPrefix(l, "data: "):
				m.Data = l[6:]
			}
		}
	}()
	return out
}

func next(t *testing.T, ch <-chan sseMsg) sseMsg {
	t.Helper()
	select {
	case m, ok := <-ch:
		require.True(t, ok, "stream closed")
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("no SSE message in 5s")
		return sseMsg{}
	}
}

func ev(at time.Time, kind, source, target, delivery string) store.Event {
	return store.Event{ObservedAt: at, OccurredAt: at, Kind: kind, Source: source, Target: target,
		Outcome: "ok", DeliveryID: delivery, TenantID: "t1", Env: "test", Origin: "live"}
}

func idStr(id int64) string { b, _ := json.Marshal(id); return string(b) }

func TestSSEResumesFromLastEventID(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	bc := api.NewBroadcaster()
	srv := newServer(t, st, bc, nil)
	now := time.Now().UTC()
	evs := []store.Event{
		ev(now, "request", "gateway", "deliveries.rpc", "del_1"),
		ev(now, "request", "gateway", "deliveries.rpc", "del_1"),
		ev(now, "request", "gateway", "deliveries.rpc", "del_other"),
		ev(now, "request", "gateway", "deliveries.rpc", "del_1"),
		ev(now, "request", "gateway", "deliveries.rpc", "del_1"),
	}
	require.NoError(t, st.InsertEvents(ctx, evs))

	ch := openSSE(t, srv.URL+"/api/stream?delivery_id=del_1", idStr(evs[1].ID))
	m := next(t, ch)
	require.Equal(t, "event", m.Event)
	require.Equal(t, idStr(evs[3].ID), m.ID, "del_other is skipped, resume starts after Last-Event-ID")
	m = next(t, ch)
	require.Equal(t, idStr(evs[4].ID), m.ID)

	liveBatch := []store.Event{ev(now, "request", "gateway", "deliveries.rpc", "del_1")}
	require.NoError(t, st.InsertEvents(ctx, liveBatch))
	live := liveBatch[0]
	require.Greater(t, live.ID, evs[4].ID)
	_ = bc.OnEvent(ctx, live)
	m = next(t, ch)
	require.Equal(t, idStr(live.ID), m.ID)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(m.Data), &got))
	require.Equal(t, "del_1", got["deliveryId"])
}

func TestSSEGapMarkerWhenResumeTooOld(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	srv := newServer(t, st, api.NewBroadcaster(), nil)
	now := time.Now().UTC()
	old := now.Add(-48 * time.Hour)
	evs := []store.Event{
		ev(old, "request", "gateway", "deliveries.rpc", "del_1"),
		ev(old, "request", "gateway", "deliveries.rpc", "del_1"),
		ev(old, "request", "gateway", "deliveries.rpc", "del_1"),
		ev(now, "request", "gateway", "deliveries.rpc", "del_1"),
		ev(now, "request", "gateway", "deliveries.rpc", "del_1"),
	}
	require.NoError(t, st.InsertEvents(ctx, evs))
	_, err := st.DeleteOlderThan(ctx, now.Add(-24*time.Hour))
	require.NoError(t, err)

	ch := openSSE(t, srv.URL+"/api/stream", idStr(evs[0].ID))
	m := next(t, ch)
	require.Equal(t, "gap", m.Event)
	var gap struct{ From, To int64 }
	require.NoError(t, json.Unmarshal([]byte(m.Data), &gap))
	require.Equal(t, evs[0].ID+1, gap.From)
	require.Equal(t, evs[3].ID-1, gap.To)
	require.Equal(t, idStr(evs[3].ID), next(t, ch).ID)
	require.Equal(t, idStr(evs[4].ID), next(t, ch).ID)
}

func TestSSEBundlesAboveRate(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	bc := api.NewBroadcaster()
	srv := newServer(t, st, bc, func(o *api.Options) { o.RatePerSec = 5; o.Window = 500 * time.Millisecond })
	ch := openSSE(t, srv.URL+"/api/stream", "")
	now := time.Now().UTC()
	batch := make([]store.Event, 12)
	for i := range batch {
		batch[i] = ev(now, "request", "gateway", "deliveries.rpc", "")
	}
	require.NoError(t, st.InsertEvents(ctx, batch))
	for _, e := range batch {
		require.NoError(t, bc.OnEvent(ctx, e))
	}
	for i := 1; i <= 5; i++ {
		require.Equal(t, "event", next(t, ch).Event)
	}
	b := next(t, ch)
	require.Equal(t, "bundle", b.Event)
	var bundle struct {
		Wire    string
		Count   int
		Outcome string
	}
	require.NoError(t, json.Unmarshal([]byte(b.Data), &bundle))
	require.Equal(t, "gateway>deliveries.rpc", bundle.Wire)
	require.Equal(t, 7, bundle.Count)
	require.Equal(t, "ok", bundle.Outcome)
	require.Equal(t, idStr(batch[11].ID), b.ID)
}

// Detector findings and health changes are never folded into a bundle (R25).
func TestSSEDetectorNotBundled(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	bc := api.NewBroadcaster()
	srv := newServer(t, st, bc, func(o *api.Options) { o.RatePerSec = 3; o.Window = 500 * time.Millisecond })
	ch := openSSE(t, srv.URL+"/api/stream", "")
	now := time.Now().UTC()
	var batch []store.Event
	for i := 0; i < 8; i++ {
		batch = append(batch, ev(now, "request", "gateway", "deliveries.rpc", ""))
	}
	batch = append(batch, ev(now, "detector", "mc", "tracking.state", "del_1"), ev(now, "health", "tracking", "", ""))
	require.NoError(t, st.InsertEvents(ctx, batch))
	for _, e := range batch {
		require.NoError(t, bc.OnEvent(ctx, e))
	}
	var kinds []string
	for i := 0; i < 6; i++ { // 3 events, detector, health, then the bundle
		m := next(t, ch)
		kinds = append(kinds, m.Event)
	}
	require.Contains(t, kinds, "detector")
	require.Equal(t, 1, countOf(kinds, "bundle"))
	require.Equal(t, 4, countOf(kinds, "event"), "3 requests + the health event")
}

func countOf(ss []string, v string) int {
	n := 0
	for _, s := range ss {
		if s == v {
			n++
		}
	}
	return n
}

// Concurrent inserters commit in their own transactions; a connected client
// must still receive every id exactly once, in order, and a reconnect must see
// no hole (R24).
func TestSSENoHoleWithConcurrentInserters(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	bc := api.NewBroadcaster()
	srv := newServer(t, st, bc, func(o *api.Options) { o.RatePerSec = 100000 })
	ch := openSSE(t, srv.URL+"/api/stream", "")
	const workers, batches, per = 2, 25, 3
	idsCh := make(chan int64, workers*batches*per)
	done := make(chan struct{})
	for w := 0; w < workers; w++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for b := 0; b < batches; b++ {
				batch := make([]store.Event, per)
				for i := range batch {
					batch[i] = ev(time.Now().UTC(), "request", "gateway", "deliveries.rpc", "")
				}
				if err := st.InsertEvents(ctx, batch); err != nil {
					t.Error(err)
					return
				}
				for _, e := range batch {
					idsCh <- e.ID
					_ = bc.OnEvent(ctx, e)
				}
			}
		}()
	}
	for w := 0; w < workers; w++ {
		<-done
	}
	close(idsCh)
	want := map[int64]bool{}
	for id := range idsCh {
		want[id] = true
	}
	var prev int64
	for range want {
		m := next(t, ch)
		require.Equal(t, "event", m.Event)
		var id int64
		require.NoError(t, json.Unmarshal([]byte(m.ID), &id))
		require.Greater(t, id, prev, "ids arrive once and in order")
		require.True(t, want[id])
		prev = id
	}
	// reconnect from the first id: everything after it, no hole
	var first int64
	for id := range want {
		if first == 0 || id < first {
			first = id
		}
	}
	ch2 := openSSE(t, srv.URL+"/api/stream", idStr(first))
	prev = first
	for i := 0; i < len(want)-1; i++ {
		var id int64
		require.NoError(t, json.Unmarshal([]byte(next(t, ch2).ID), &id))
		require.Greater(t, id, prev)
		require.True(t, want[id])
		prev = id
	}
}

// A trace sibling (no delivery id) stored before the delivery row that
// carries its trace must still reach a delivery stream, live and on resume.
func TestSSEDeliveryStreamKeepsEarlierTraceSibling(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	bc := api.NewBroadcaster()
	srv := newServer(t, st, bc, nil)
	now := time.Now().UTC()
	base := []store.Event{ev(now, "request", "gateway", "deliveries.rpc", "del_1")}
	require.NoError(t, st.InsertEvents(ctx, base))

	ch := openSSE(t, srv.URL+"/api/stream?delivery_id=del_1", "")
	sibling := ev(now, "request", "deliveries.rpc", "deliveries.state_machine", "")
	sibling.TraceID = "trace-T"
	own := ev(now, "request", "gateway", "deliveries.rpc", "del_1")
	own.TraceID = "trace-T"
	other := ev(now, "request", "gateway", "deliveries.rpc", "del_other")
	for _, e := range []store.Event{sibling, other} {
		b := []store.Event{e}
		require.NoError(t, st.InsertEvents(ctx, b))
		if e.TraceID != "" {
			sibling = b[0]
		}
	}
	ownB := []store.Event{own}
	require.NoError(t, st.InsertEvents(ctx, ownB))
	own = ownB[0]
	_ = bc.OnEvent(ctx, own)

	require.Equal(t, idStr(sibling.ID), next(t, ch).ID, "sibling committed first arrives first")
	require.Equal(t, idStr(own.ID), next(t, ch).ID)

	ch2 := openSSE(t, srv.URL+"/api/stream?delivery_id=del_1", idStr(base[0].ID))
	require.Equal(t, idStr(sibling.ID), next(t, ch2).ID, "resume shows no hole")
	require.Equal(t, idStr(own.ID), next(t, ch2).ID)
}

func TestSSEDisconnectUnsubscribes(t *testing.T) {
	bc := api.NewBroadcaster()
	srv := newServer(t, nil, bc, nil)
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, 1, bc.Len())
	cancel()
	_ = resp.Body.Close()
	require.Eventually(t, func() bool { return bc.Len() == 0 }, 3*time.Second, 20*time.Millisecond)
}

func TestTimelineMarksPreStartGap(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	srv := newServer(t, st, api.NewBroadcaster(), nil)
	now := time.Now().UTC()
	require.NoError(t, st.InsertEvents(ctx, []store.Event{
		ev(now.Add(-10*time.Minute), "message_published", "deliveries", "nats.DELIVERIES", "del_1"),
		ev(now.Add(-1*time.Minute), "message_published", "deliveries", "nats.DELIVERIES", "del_1"),
	}))
	require.NoError(t, st.RecordStartup(ctx, now.Add(-8*time.Minute))) // first ever start
	require.NoError(t, st.Heartbeat(ctx, now.Add(-7*time.Minute)))
	require.NoError(t, st.RecordStartup(ctx, now.Add(-5*time.Minute)))

	var tl struct {
		Events []map[string]any
		Next   int64
		Gaps   []struct {
			From, To time.Time
			Kind     string
		}
	}
	getJSON(t, srv.URL+"/api/orders/del_1/timeline", &tl)
	require.Len(t, tl.Events, 2)
	var downs, bands int
	for _, g := range tl.Gaps {
		switch g.Kind {
		case "mc_down":
			downs++
			require.WithinDuration(t, now.Add(-7*time.Minute), g.From, time.Second)
			require.WithinDuration(t, now.Add(-5*time.Minute), g.To, time.Second)
		case "no_trace":
			bands++
			require.WithinDuration(t, now.Add(-10*time.Minute), g.From, time.Second)
			require.WithinDuration(t, now.Add(-8*time.Minute), g.To, time.Second)
		}
	}
	require.Equal(t, 1, downs)
	require.Equal(t, 1, bands, "one band before Mission Control's first start, no per-run bands")
	require.Len(t, tl.Gaps, 2)
}

type stepsResp struct {
	Order struct {
		DeliveryID string
		OrderRef   string
	}
	StatusSteps []struct{ Status, State string }
	Detectors   []map[string]any
}

func states(r stepsResp) map[string]string {
	m := map[string]string{}
	for _, s := range r.StatusSteps {
		m[s.Status] = s.State
	}
	return m
}

func withAttrs(e store.Event, attrs string) store.Event { e.Attrs = []byte(attrs); return e }

func TestStatusStepsFromEvents(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	srv := newServer(t, st, api.NewBroadcaster(), nil)
	now := time.Now().UTC()
	created := withAttrs(ev(now.Add(-5*time.Minute), "message_published", "deliveries", "nats.DELIVERIES", "del_1"),
		`{"event_type":"delivery.created","status":"confirmed","external_id":"ORD-1"}`)
	created.OrderRef = "ORD-1"
	require.NoError(t, st.InsertEvents(ctx, []store.Event{
		created,
		withAttrs(ev(now.Add(-4*time.Minute), "message_published", "deliveries", "nats.DELIVERIES", "del_1"),
			`{"event_type":"delivery.status_changed","from":"confirmed","to":"courier_assigned"}`),
		withAttrs(ev(now.Add(-2*time.Minute), "message_published", "deliveries", "nats.DELIVERIES", "del_1"),
			`{"event_type":"delivery.status_changed","from":"courier_assigned","to":"picked_up"}`),
	}))
	require.NoError(t, index.New(st).OnEvent(ctx, created))

	var r stepsResp
	getJSON(t, srv.URL+"/api/orders/del_1", &r)
	require.Equal(t, "ORD-1", r.Order.OrderRef)
	require.Equal(t, map[string]string{"confirmed": "done", "courier_assigned": "done", "at_pickup": "pending",
		"picked_up": "done", "arriving": "pending", "delivered": "pending"}, states(r))
	require.Equal(t, []string{"confirmed", "courier_assigned", "at_pickup", "picked_up", "arriving", "delivered"},
		[]string{r.StatusSteps[0].Status, r.StatusSteps[1].Status, r.StatusSteps[2].Status, r.StatusSteps[3].Status,
			r.StatusSteps[4].Status, r.StatusSteps[5].Status})

	// a lost courier signal turns the current step into a warning
	require.NoError(t, st.InsertEvents(ctx, []store.Event{
		withAttrs(ev(now.Add(-time.Minute), "message_published", "tracking", "nats.TRACKING", "del_1"),
			`{"event_type":"tracking.courier_signal_lost"}`),
	}))
	r = stepsResp{}
	getJSON(t, srv.URL+"/api/orders/del_1", &r)
	got := states(r)
	require.Equal(t, "warning", got["picked_up"])
	require.Equal(t, "done", got["confirmed"])
	require.Equal(t, "pending", got["arriving"])
}

func TestStatusStepWarningClearsAfterRestore(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	srv := newServer(t, st, api.NewBroadcaster(), nil)
	now := time.Now().UTC()
	require.NoError(t, st.InsertEvents(ctx, []store.Event{
		withAttrs(ev(now.Add(-5*time.Minute), "message_published", "deliveries", "nats.DELIVERIES", "del_1"),
			`{"event_type":"delivery.created","status":"confirmed"}`),
		withAttrs(ev(now.Add(-4*time.Minute), "message_published", "tracking", "nats.TRACKING", "del_1"),
			`{"event_type":"tracking.courier_signal_lost"}`),
	}))
	var r stepsResp
	getJSON(t, srv.URL+"/api/orders/del_1", &r)
	require.Equal(t, "warning", states(r)["confirmed"])
	require.NoError(t, st.InsertEvents(ctx, []store.Event{
		withAttrs(ev(now.Add(-3*time.Minute), "message_published", "tracking", "nats.TRACKING", "del_1"),
			`{"event_type":"tracking.courier_signal_restored"}`),
	}))
	r = stepsResp{}
	getJSON(t, srv.URL+"/api/orders/del_1", &r)
	require.Equal(t, "done", states(r)["confirmed"], "recovered: no false amber")
	// lost again after the restore: warning returns
	require.NoError(t, st.InsertEvents(ctx, []store.Event{
		withAttrs(ev(now.Add(-2*time.Minute), "message_published", "tracking", "nats.TRACKING", "del_1"),
			`{"event_type":"tracking.courier_signal_lost"}`),
	}))
	r = stepsResp{}
	getJSON(t, srv.URL+"/api/orders/del_1", &r)
	require.Equal(t, "warning", states(r)["confirmed"])
}

func TestCSPHeader(t *testing.T) {
	srv := newServer(t, nil, api.NewBroadcaster(), nil)
	for _, p := range []string{"/api/nothing-here", "/api/stream"} {
		ctx, cancel := context.WithCancel(context.Background())
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+p, nil)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		require.Equal(t, "default-src 'self'", resp.Header.Get("Content-Security-Policy"), p)
		_ = resp.Body.Close()
		cancel()
	}
	rec := httptest.NewRecorder()
	api.CSP(api.AuthSeam(http.NotFoundHandler())).ServeHTTP(rec, httptest.NewRequest("GET", "/livez", nil))
	require.Equal(t, "default-src 'self'", rec.Header().Get("Content-Security-Policy"))
}

func TestComponentHealthNotProbed(t *testing.T) {
	st := openStore(t)
	srv := newServer(t, st, api.NewBroadcaster(), nil)
	var c struct {
		Health struct {
			Liveness, Readiness struct{ State string }
		}
		Recent []map[string]any
	}
	getJSON(t, srv.URL+"/api/components/deliveries", &c)
	require.Equal(t, "not_probed", c.Health.Liveness.State)
	require.Equal(t, "not_probed", c.Health.Readiness.State)
	require.NotNil(t, c.Recent)
	resp, err := http.Get(srv.URL + "/api/components/nope")
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// An uninstrumented cube shows not_instrumented until something is seen there;
// then it lights like any other (a future pgx tracing rollout lights pg.*).
func TestTopologyUninstrumentedLightsWithActivity(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	srv := newServer(t, st, api.NewBroadcaster(), nil)
	now := time.Now().UTC()
	require.NoError(t, st.InsertEvents(ctx, []store.Event{ev(now.Add(-5*time.Second), "request", "tracking.outbox", "pg.tracking", "")}))
	var r struct {
		Components []struct{ ID, State string }
	}
	getJSON(t, srv.URL+"/api/topology", &r)
	got := map[string]string{}
	for _, c := range r.Components {
		got[c.ID] = c.State
	}
	require.Equal(t, "active", got["pg.tracking"])
	require.Equal(t, "not_instrumented", got["pg.deliveries"])
	require.Equal(t, "not_instrumented", got["partner"])
}

func TestEventDetailAndComponentLogs(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	srv := newServer(t, st, api.NewBroadcaster(), nil)
	now := time.Now().UTC()
	span := ev(now, "request", "tracking", "tracking.state", "del_1")
	span.TraceID, span.SpanID = "t-1", "s-1"
	logline := withAttrs(ev(now, "log", "tracking", "", "del_1"), `{"body":"x","severity":"ERROR"}`)
	logline.TraceID, logline.SpanID = "t-1", "s-1"
	otherLog := withAttrs(ev(now, "log", "tracking", "", "del_2"), `{"body":"y","severity":"INFO"}`)
	batch := []store.Event{span, logline, otherLog}
	require.NoError(t, st.InsertEvents(ctx, batch))
	span = batch[0]

	var d struct {
		Event map[string]any
		Logs  []map[string]any
	}
	getJSON(t, srv.URL+"/api/events/"+idStr(span.ID), &d)
	require.Len(t, d.Logs, 1)

	var l struct{ Logs []map[string]any }
	getJSON(t, srv.URL+"/api/components/tracking/logs?delivery_id=del_1", &l)
	require.Len(t, l.Logs, 1)
	l.Logs = nil
	getJSON(t, srv.URL+"/api/components/tracking/logs", &l)
	require.Len(t, l.Logs, 2)
	l.Logs = nil
	getJSON(t, srv.URL+"/api/components/tracking/logs?level=error", &l)
	require.Len(t, l.Logs, 1)
}

func TestOrdersSearch(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	srv := newServer(t, st, api.NewBroadcaster(), nil)
	e := ev(time.Now().UTC(), "message_published", "deliveries", "nats.DELIVERIES", "del_1")
	e.OrderRef = "ORD-1"
	require.NoError(t, index.New(st).OnEvent(ctx, e))
	var hits []map[string]any
	getJSON(t, srv.URL+"/api/orders?q=ORD-1", &hits)
	require.Len(t, hits, 1)
	require.Equal(t, "del_1", hits[0]["deliveryId"])
	hits = nil
	getJSON(t, srv.URL+"/api/orders?q=nope", &hits)
	require.NotNil(t, hits)
	require.Empty(t, hits)
}
