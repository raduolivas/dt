package otelx

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestShutdownAllRunsInParallel(t *testing.T) {
	e1, e2 := errors.New("one"), errors.New("two")
	slow := func(err error) func(context.Context) error {
		return func(context.Context) error { time.Sleep(3 * time.Second); return err }
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	err := shutdownAll(ctx, slow(e1), slow(e2))
	if d := time.Since(start); d >= 4*time.Second {
		t.Fatalf("took %v, want < 4s", d)
	}
	if !errors.Is(err, e1) || !errors.Is(err, e2) {
		t.Fatalf("err = %v, want both joined", err)
	}
}

func TestInitResourceHasVersion(t *testing.T) {
	for _, tc := range []struct{ env, want string }{{"abc123", "abc123"}, {"", "dev"}} {
		t.Run(tc.want, func(t *testing.T) {
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
			t.Setenv("DT_BUILD_COMMIT", tc.env)
			reader := sdkmetric.NewManualReader()
			testReader = reader
			t.Cleanup(func() { testReader = nil })
			shutdown, err := Init(context.Background(), "svc")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = shutdown(context.Background()) })
			c, _ := otel.Meter("t").Int64Counter("t.counter")
			c.Add(context.Background(), 1)
			var rm metricdata.ResourceMetrics
			if err := reader.Collect(context.Background(), &rm); err != nil {
				t.Fatal(err)
			}
			v, _ := rm.Resource.Set().Value("service.version")
			if v.AsString() != tc.want {
				t.Fatalf("service.version = %q, want %q", v.AsString(), tc.want)
			}
		})
	}
}
