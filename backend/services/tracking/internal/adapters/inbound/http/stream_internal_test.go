package httpapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/raduolivas/dt/backend/pkg/viewerjwt"
	"github.com/raduolivas/dt/backend/services/tracking/internal/app"
	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

func TestExpiryAfterLeaveArmsNoTimer(t *testing.T) {
	h := NewHub(Deps{})
	s := newStream(viewerjwt.Claims{DeliveryID: "del_1", TenantID: "ten_a", Env: "test", LinkID: "tl_1"})
	h.leave(s) // the stream's handler is gone (also without join)
	s.expireAt(time.Now().Add(time.Hour))
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.expiry != nil {
		t.Fatal("an expiry timer was armed for a stream that already left")
	}
}

type nopFeed struct{}

func (nopFeed) Run(ctx context.Context, _ app.FeedHandler) error { <-ctx.Done(); return nil }
func (nopFeed) Subscribe(context.Context, string) error          { return nil }
func (nopFeed) Unsubscribe(context.Context, string)              {}

func TestHubFanoutSpanCountsViewers(t *testing.T) {
	prevProp, prevTP := otel.GetTextMapPropagator(), otel.GetTracerProvider()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})

	h := NewHub(Deps{Feed: nopFeed{}})
	for _, link := range []string{"tl_1", "tl_2"} {
		s := newStream(viewerjwt.Claims{DeliveryID: "del_1", TenantID: "ten_a", Env: "test", LinkID: link})
		if err := h.join(context.Background(), s); err != nil {
			t.Fatal(err)
		}
	}
	pctx, parent := otel.Tracer("test").Start(context.Background(), "parent")
	defer parent.End()
	data, _ := json.Marshal(domain.TrackingSnapshot{DeliveryID: "del_1", Version: 1})
	h.Snapshot(pctx, "del_1", data)

	var fan sdktrace.ReadOnlySpan
	n := 0
	for _, s := range rec.Ended() {
		if s.Name() == "tracking.viewer_fanout" {
			fan = s
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d tracking.viewer_fanout spans, want 1", n)
	}
	if fan.Parent().TraceID() != parent.SpanContext().TraceID() {
		t.Fatalf("fanout trace = %v, want %v", fan.Parent().TraceID(), parent.SpanContext().TraceID())
	}
	if fan.SpanKind() != trace.SpanKindInternal {
		t.Fatalf("kind = %v", fan.SpanKind())
	}
	want := map[string]attribute.Value{"tracking.viewers": attribute.IntValue(2), "dt.delivery_id": attribute.StringValue("del_1")}
	for _, kv := range fan.Attributes() {
		if w, ok := want[string(kv.Key)]; ok {
			if kv.Value != w {
				t.Fatalf("%s = %v, want %v", kv.Key, kv.Value, w)
			}
			delete(want, string(kv.Key))
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing attributes %v", want)
	}
}
