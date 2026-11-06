package bootstrap_test

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/raduolivas/dt/backend/pkg/bootstrap"
	"github.com/raduolivas/dt/backend/pkg/testkit"
)

func TestRedisSpansHaveNoStatement(t *testing.T) {
	prevProp, prevTP := otel.GetTextMapPropagator(), otel.GetTracerProvider()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})

	opts, err := bootstrap.RedisOptions(testkit.StartRedis(t))
	if err != nil {
		t.Fatal(err)
	}
	c := bootstrap.NewRedisClient(opts)
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Set(context.Background(), "secret-key", "secret-value", 0).Err(); err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, sp := range rec.Ended() {
		if sp.Name() != "set" {
			continue
		}
		found = true
		for _, kv := range sp.Attributes() {
			if kv.Key == "db.statement" || kv.Key == "db.query.text" {
				t.Errorf("span has statement attribute %s=%v", kv.Key, kv.Value.String())
			}
			if v := kv.Value.String(); v == "secret-key" || v == "secret-value" || v == "set secret-key secret-value" {
				t.Errorf("span leaks command argument in %s=%s", kv.Key, v)
			}
		}
	}
	if !found {
		t.Fatalf("no redis set span among %d spans", len(rec.Ended()))
	}
}
