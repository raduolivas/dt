package app_test

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/raduolivas/dt/backend/services/deliveries/internal/domain"
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

func spanAttr(sp sdktrace.ReadOnlySpan, key string) string {
	for _, kv := range sp.Attributes() {
		if kv.Key == attribute.Key(key) {
			return kv.Value.AsString()
		}
	}
	return ""
}

func spanNamed(rec *tracetest.SpanRecorder, name string) sdktrace.ReadOnlySpan {
	var found sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.Name() == name {
			found = s
		}
	}
	return found
}

func TestDeliveriesReportSpanOutcome(t *testing.T) {
	rec := useSpanRecorder(t)
	ctx := context.Background()
	s, _, _ := newSvc()
	d, err := s.Create(ctx, scope, input("a"))
	if err != nil {
		t.Fatal(err)
	}
	if sp := spanNamed(rec, "deliveries.create"); sp == nil || spanAttr(sp, "dt.delivery_id") != d.ID {
		t.Fatalf("create span = %v", sp)
	}
	rep := domain.Report{EventID: "e1", Type: domain.ReportAtPickup, Actor: domain.ActorPartner}
	if _, _, err := s.Report(ctx, scope, d.ID, rep); err != nil {
		t.Fatal(err)
	}
	if sp := spanNamed(rec, "deliveries.report"); sp == nil || spanAttr(sp, "deliveries.outcome") != string(domain.OutcomeApplied) {
		t.Fatalf("first report span outcome wrong: %v", sp)
	}
	if _, out, err := s.ReportFor(ctx, scope, d.ID, "", rep); err != nil || out != domain.OutcomeDuplicate {
		t.Fatalf("%v %v", out, err)
	}
	sp := spanNamed(rec, "deliveries.reportfor")
	if sp == nil {
		t.Fatal("no deliveries.reportfor span")
	}
	for k, want := range map[string]string{
		"deliveries.outcome": "duplicate", "dt.delivery_id": d.ID, "dt.tenant_id": "ten_1", "dt.environment": "test",
	} {
		if got := spanAttr(sp, k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}
