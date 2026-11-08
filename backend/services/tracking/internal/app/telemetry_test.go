package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

// useSpanRecorder installs a global tracer provider with a span recorder.
// Tests using it are not parallel: they swap globals.
func useSpanRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	prevProp, prevTP := otel.GetTextMapPropagator(), otel.GetTracerProvider()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})
	return rec
}

func endedSpan(rec *tracetest.SpanRecorder, name string) sdktrace.ReadOnlySpan {
	for _, s := range rec.Ended() {
		if s.Name() == name {
			return s
		}
	}
	return nil
}

func attr(kvs []attribute.KeyValue, key string) (attribute.Value, bool) {
	for _, kv := range kvs {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

type dropEvent struct {
	delivery, reason string
	count            int
}

func dropEvents(sp sdktrace.ReadOnlySpan) []dropEvent {
	var out []dropEvent
	for _, e := range sp.Events() {
		if e.Name != "fix_dropped" {
			continue
		}
		d, _ := attr(e.Attributes, "dt.delivery_id")
		r, _ := attr(e.Attributes, "reason")
		c, _ := attr(e.Attributes, "count")
		out = append(out, dropEvent{d.AsString(), r.AsString(), int(c.AsInt64())})
	}
	return out
}

func eventNames(sp sdktrace.ReadOnlySpan) []string {
	var out []string
	for _, e := range sp.Events() {
		out = append(out, e.Name)
	}
	return out
}

func TestIngestDropReasons(t *testing.T) {
	fast := 100.0
	tests := []struct {
		name  string
		setup func(t *testing.T, f *fixture)
		fixes func(now time.Time) []domain.Fix
		want  dropEvent
	}{
		{"no_active_delivery",
			func(t *testing.T, f *fixture) { f.courier(t, "cou_1") },
			func(now time.Time) []domain.Fix {
				return []domain.Fix{fixAt(now, 52.369, 4.9), fixAt(now, 52.3691, 4.9)}
			},
			dropEvent{"", "no_active_delivery", 2}},
		{"delivery_final",
			func(t *testing.T, f *fixture) {
				f.courier(t, "cou_1")
				f.delivery(t, "del_1", domain.ModelDirect, "cou_1", domain.StatusDelivered)
				_ = f.states.SetCourierActive(context.Background(), "cou_1", "del_1", true)
			},
			func(now time.Time) []domain.Fix { return []domain.Fix{fixAt(now, 52.369, 4.9)} },
			dropEvent{"del_1", "delivery_final", 1}},
		{"delivery_reassigned",
			func(t *testing.T, f *fixture) {
				f.courier(t, "cou_1")
				f.courier(t, "cou_2")
				f.delivery(t, "del_1", domain.ModelEndToEnd, "cou_2", domain.StatusCourierAssigned)
				_ = f.states.SetCourierActive(context.Background(), "cou_1", "del_1", true)
			},
			func(now time.Time) []domain.Fix { return []domain.Fix{fixAt(now, 52.369, 4.9)} },
			dropEvent{"del_1", "delivery_reassigned", 1}},
		{"delivery_not_found",
			func(t *testing.T, f *fixture) {
				f.courier(t, "cou_1")
				// Active set names a delivery whose state does not exist.
				_ = f.states.SetCourierActive(context.Background(), "cou_1", "del_gone", true)
			},
			func(now time.Time) []domain.Fix { return []domain.Fix{fixAt(now, 52.369, 4.9)} },
			dropEvent{"del_gone", "delivery_not_found", 1}},
		{"invalid",
			func(t *testing.T, f *fixture) {
				f.courier(t, "cou_1")
				f.delivery(t, "del_1", domain.ModelDirect, "cou_1", domain.StatusPickedUp)
			},
			func(now time.Time) []domain.Fix { return []domain.Fix{fixAt(now, 200, 4.9)} },
			dropEvent{"del_1", "invalid", 1}},
		{"future",
			func(t *testing.T, f *fixture) {
				f.courier(t, "cou_1")
				f.delivery(t, "del_1", domain.ModelDirect, "cou_1", domain.StatusPickedUp)
			},
			func(now time.Time) []domain.Fix { return []domain.Fix{fixAt(now.Add(time.Minute), 52.369, 4.9)} },
			dropEvent{"del_1", "future", 1}},
		{"speed",
			func(t *testing.T, f *fixture) {
				f.courier(t, "cou_1")
				f.delivery(t, "del_1", domain.ModelDirect, "cou_1", domain.StatusPickedUp)
			},
			func(now time.Time) []domain.Fix {
				fx := fixAt(now, 52.369, 4.9)
				fx.SpeedMps = &fast
				return []domain.Fix{fx}
			},
			dropEvent{"del_1", "speed", 1}},
		{"jump",
			func(t *testing.T, f *fixture) {
				f.courier(t, "cou_1")
				f.delivery(t, "del_1", domain.ModelDirect, "cou_1", domain.StatusPickedUp)
				f.clock.Advance(time.Second)
				if acc, _, err := f.svc.Ingest(context.Background(), sc, "cou_1", []domain.Fix{fixAt(f.clock.Now(), 52.369, 4.9)}, "relay"); err != nil || acc != 1 {
					t.Fatalf("first fix: %d %v", acc, err)
				}
			},
			func(now time.Time) []domain.Fix { return []domain.Fix{fixAt(now.Add(time.Second), 52.39, 4.9)} },
			dropEvent{"del_1", "jump", 1}},
		{"state_update_failed",
			func(t *testing.T, f *fixture) {
				f.courier(t, "cou_1")
				f.delivery(t, "del_1", domain.ModelDirect, "cou_1", domain.StatusPickedUp)
				f.states.failPut = map[string]error{"del_1": errors.New("redis down")}
			},
			func(now time.Time) []domain.Fix { return []domain.Fix{fixAt(now, 52.369, 4.9)} },
			dropEvent{"del_1", "state_update_failed", 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := useSpanRecorder(t)
			f := newFixture(t)
			tc.setup(t, f)
			f.clock.Advance(time.Second)
			acc, drop, err := f.svc.Ingest(context.Background(), sc, "cou_1", tc.fixes(f.clock.Now()), "relay")
			if tc.name != "state_update_failed" && err != nil {
				t.Fatal(err)
			}
			if acc != 0 {
				t.Fatalf("accepted %d, want 0", acc)
			}
			// The last tracking.ingest span is the one under test (jump
			// ingests once during setup).
			var sp sdktrace.ReadOnlySpan
			for _, s := range rec.Ended() {
				if s.Name() == "tracking.ingest" {
					sp = s
				}
			}
			if sp == nil {
				t.Fatal("no tracking.ingest span")
			}
			got := dropEvents(sp)
			if len(got) != 1 || got[0] != tc.want {
				t.Fatalf("fix_dropped events = %+v, want %+v", got, tc.want)
			}
			if v, ok := attr(sp.Attributes(), "tracking.fixes.dropped"); !ok || int(v.AsInt64()) != tc.want.count {
				t.Fatalf("tracking.fixes.dropped = %v (%v), want %d (Ingest returned %d)", v, ok, tc.want.count, drop)
			}
			if v, ok := attr(sp.Attributes(), "tracking.fixes.accepted"); !ok || v.AsInt64() != 0 {
				t.Fatalf("tracking.fixes.accepted = %v (%v)", v, ok)
			}
			if v, _ := attr(sp.Attributes(), "dt.tenant_id"); v.AsString() != "ten_a" {
				t.Fatalf("dt.tenant_id = %v", v)
			}
			if v, _ := attr(sp.Attributes(), "dt.environment"); v.AsString() != "test" {
				t.Fatalf("dt.environment = %v", v)
			}
		})
	}
}

func TestIngestKeptElsewhereIsNotDropped(t *testing.T) {
	rec := useSpanRecorder(t)
	f := newFixture(t)
	f.courier(t, "cou_1")
	f.delivery(t, "del_a", domain.ModelEndToEnd, "cou_1", domain.StatusPickedUp)
	f.delivery(t, "del_b", domain.ModelEndToEnd, "cou_1", domain.StatusPickedUp)
	f.clock.Advance(time.Second)
	// del_b cannot store: its copy of the fix is lost, del_a keeps it.
	f.states.failPut = map[string]error{"del_b": errors.New("redis down")}
	acc, drop, err := f.svc.Ingest(context.Background(), sc, "cou_1", []domain.Fix{fixAt(f.clock.Now(), 52.369, 4.9)}, "relay")
	if err != nil || acc != 1 || drop != 0 {
		t.Fatalf("Ingest = %d,%d,%v want 1,0,nil", acc, drop, err)
	}
	if got := dropEvents(endedSpan(rec, "tracking.ingest")); len(got) != 0 {
		t.Fatalf("fix_dropped events = %+v, want none", got)
	}
}

func TestIngestDropLogHasNoCoordinates(t *testing.T) {
	f := newFixture(t)
	var buf bytes.Buffer
	f.svc.Log = slog.New(slog.NewJSONHandler(&buf, nil))
	f.courier(t, "cou_1")
	f.delivery(t, "del_1", domain.ModelDirect, "cou_1", domain.StatusPickedUp)
	f.clock.Advance(time.Second)
	if _, _, err := f.svc.Ingest(context.Background(), sc, "cou_1", []domain.Fix{fixAt(f.clock.Now(), 52.123456, 4.987654), fixAt(f.clock.Now(), 200, 4.987654)}, "relay"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, `"msg":"ingest_fixes_dropped"`) || !strings.Contains(out, `"reason":"invalid"`) ||
		!strings.Contains(out, `"count":1`) || !strings.Contains(out, `"delivery_id":"del_1"`) {
		t.Fatalf("log = %s", out)
	}
	for _, bad := range []string{"lat", "lng", "52.123456", "4.987654", "200"} {
		if strings.Contains(out, bad) {
			t.Errorf("log contains %q: %s", bad, out)
		}
	}
}

func TestApplySpanEvents(t *testing.T) {
	move := func(st *domain.State) error {
		st.ApplyFix(fixAt(t0.Add(2*time.Second), 52.369, 4.9), t0.Add(2*time.Second))
		return nil
	}
	tests := []struct {
		name   string
		setup  func(f *fixture)
		events []string
	}{
		{"stored and published", func(f *fixture) {}, []string{"state_stored", "snapshot_published"}},
		{"conflict", func(f *fixture) {
			// A racing writer bumps the version before the first Put; the
			// retry stores.
			f.states.beforeP = func() {
				st, _, _ := f.states.Get(context.Background(), "del_1")
				st.Version += 1000
				f.states.put(st)
			}
		}, []string{"state_conflict", "state_stored", "snapshot_published"}},
		{"publish failure", func(f *fixture) { f.pub.err = errors.New("nats down") },
			[]string{"state_stored", "snapshot_publish_failed"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := useSpanRecorder(t)
			f := newFixture(t)
			f.courier(t, "cou_1")
			f.delivery(t, "del_1", domain.ModelDirect, "cou_1", domain.StatusPickedUp)
			f.clock.Advance(2 * time.Second)
			tc.setup(f)
			before := len(rec.Ended())
			if err := f.svc.Apply(context.Background(), sc, "del_1", move); err != nil {
				t.Fatal(err)
			}
			var sp sdktrace.ReadOnlySpan
			for _, s := range rec.Ended()[before:] {
				if s.Name() == "tracking.apply" {
					sp = s
				}
			}
			if sp == nil {
				t.Fatal("no tracking.apply span")
			}
			got := eventNames(sp)
			if strings.Join(got, ",") != strings.Join(tc.events, ",") {
				t.Fatalf("events = %v, want %v", got, tc.events)
			}
			if sp.Status().Code != codes.Unset {
				t.Fatalf("status = %v, want Unset", sp.Status())
			}
			for k, want := range map[string]string{"dt.delivery_id": "del_1", "dt.tenant_id": "ten_a", "dt.environment": "test"} {
				if v, _ := attr(sp.Attributes(), k); v.AsString() != want {
					t.Errorf("%s = %q, want %q", k, v.AsString(), want)
				}
			}
		})
	}
}
