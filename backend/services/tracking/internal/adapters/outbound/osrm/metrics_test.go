package osrm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/raduolivas/dt/backend/pkg/otelx"
	"github.com/raduolivas/dt/backend/pkg/otelx/otelxtest"
	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

type key struct{ op, outcome string }

// collect returns the histogram counts and the error-counter values by
// operation and outcome, after asserting the label allow-list.
func collect(t *testing.T, reader *sdkmetric.ManualReader) (hist map[key]uint64, errs map[key]int64) {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	otelxtest.AssertAllowed(t, rm)
	hist, errs = map[key]uint64{}, map[key]int64{}
	label := func(set attribute.Set) key {
		if set.Len() != 2 {
			t.Fatalf("attributes = %v", set)
		}
		op, _ := set.Value("operation")
		oc, _ := set.Value("outcome")
		return key{op.AsString(), oc.AsString()}
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch d := m.Data.(type) {
			case metricdata.Histogram[float64]:
				if m.Name != "dt.routing.duration" {
					continue
				}
				if m.Unit != "s" {
					t.Fatalf("unit = %q", m.Unit)
				}
				for _, dp := range d.DataPoints {
					hist[label(dp.Attributes)] += dp.Count
				}
			case metricdata.Sum[int64]:
				if m.Name != "dt.routing.errors" {
					continue
				}
				for _, dp := range d.DataPoints {
					errs[label(dp.Attributes)] += dp.Value
				}
			}
		}
	}
	return hist, errs
}

func TestRoutingMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader), sdkmetric.WithView(otelx.Views()...))
	t.Cleanup(setMeterForTest(mp.Meter("test")))

	status, slow := 200, false
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if slow {
			select {
			case <-release:
			case <-time.After(2 * time.Second):
			}
			return
		}
		w.WriteHeader(status)
		if status == 200 {
			_, _ = w.Write([]byte(routeBody))
		}
	}))
	defer srv.Close()
	defer close(release)
	c := New(map[string]string{"car": srv.URL}, 100*time.Millisecond)
	a, b := domain.LatLng{Lat: 52.37311, Lng: 4.89348}, domain.LatLng{Lat: 52.37817, Lng: 4.90027}

	if _, _, err := c.Route(context.Background(), "car", a, b); err != nil {
		t.Fatal(err)
	}
	status = 500
	if _, _, err := c.Route(context.Background(), "car", a, b); err == nil {
		t.Fatal("500 must fail")
	}
	slow = true
	fixes := []domain.Fix{{T: time.Unix(1000, 0), Pos: a}, {T: time.Unix(1005, 0), Pos: b}}
	if _, err := c.Match(context.Background(), "car", fixes); err == nil {
		t.Fatal("slow must time out")
	}

	hist, errs := collect(t, reader)
	for k, want := range map[key]uint64{{"route", "ok"}: 1, {"route", "error"}: 1, {"match", "timeout"}: 1} {
		if hist[k] != want {
			t.Errorf("duration %v = %d, want %d (all: %v)", k, hist[k], want, hist)
		}
	}
	if len(hist) != 3 {
		t.Errorf("duration points = %v", hist)
	}
	if errs[key{"route", "error"}] != 1 || errs[key{"match", "timeout"}] != 1 || len(errs) != 2 {
		t.Errorf("errors = %v", errs)
	}
}

func TestRoutingSpansCarryNoCoordinates(t *testing.T) {
	prev := otel.GetTracerProvider()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()); otel.SetTracerProvider(prev) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	c := New(map[string]string{"car": srv.URL}, time.Second)
	a, b := domain.LatLng{Lat: 52.373111, Lng: 4.893481}, domain.LatLng{Lat: 52.378172, Lng: 4.900272}
	_, _, _ = c.Route(context.Background(), "car", a, b)
	srv.Close() // a transport error carries the URL in its message
	_, _, _ = c.Route(context.Background(), "car", a, b)

	spans := rec.Ended()
	if len(spans) != 2 || spans[0].Name() != "osrm.route" {
		t.Fatalf("spans = %v", spans)
	}
	for _, sp := range spans {
		var all []string
		for _, kv := range sp.Attributes() {
			all = append(all, string(kv.Key), kv.Value.String())
		}
		all = append(all, sp.Status().Description)
		for _, e := range sp.Events() {
			all = append(all, e.Name)
			for _, kv := range e.Attributes {
				all = append(all, string(kv.Key), kv.Value.String())
			}
		}
		for _, s := range all {
			for _, bad := range []string{"52.37", "4.89", "4.90", "http://", "127.0.0.1", "?"} {
				if strings.Contains(s, bad) {
					t.Errorf("span %s: %q contains %q", sp.Name(), s, bad)
				}
			}
		}
	}
}

func TestTransportErrorsCarryNoURL(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader), sdkmetric.WithView(otelx.Views()...))
	t.Cleanup(setMeterForTest(mp.Meter("test")))

	a, b := domain.LatLng{Lat: 52.373111, Lng: 4.893481}, domain.LatLng{Lat: 52.378172, Lng: 4.900272}
	check := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: no error", name)
		}
		for _, bad := range []string{"52.37", "4.89", "4.90", "http://", "?"} {
			if strings.Contains(err.Error(), bad) {
				t.Errorf("%s: %q contains %q", name, err, bad)
			}
		}
	}

	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		select {
		case <-release:
		case <-time.After(2 * time.Second):
		}
	}))
	defer slow.Close()
	defer close(release)
	_, _, err := New(map[string]string{"car": slow.URL}, 50*time.Millisecond).Route(context.Background(), "car", a, b)
	check("timeout", err)
	if !errors.Is(err, context.DeadlineExceeded) || outcomeOf(err) != outcomeTimeout {
		t.Errorf("timeout lost its classification: %v", err)
	}

	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead.Close()
	_, _, err = New(map[string]string{"car": dead.URL}, time.Second).Route(context.Background(), "car", a, b)
	check("closed server", err)
	if outcomeOf(err) != outcomeError {
		t.Errorf("outcome = %s", outcomeOf(err))
	}
}
