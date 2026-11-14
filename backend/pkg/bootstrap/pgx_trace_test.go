package bootstrap_test

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/raduolivas/dt/backend/pkg/bootstrap"
	"github.com/raduolivas/dt/backend/pkg/testkit"
)

func TestPoolTracerOmitsArguments(t *testing.T) {
	prevTP := otel.GetTracerProvider()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()); otel.SetTracerProvider(prevTP) })

	// otelpgx only traces under a recording parent span, as in a request.
	ctx, parent := tp.Tracer("test").Start(context.Background(), "request")
	defer parent.End()
	pool, err := bootstrap.NewPool(ctx, testkit.StartPostgres(t).URL("gateway"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var got string
	if err := pool.QueryRow(ctx, "SELECT $1::text", "dt_sk_test_x").Scan(&got); err != nil {
		t.Fatal(err)
	}

	var withStatement int
	for _, sp := range rec.Ended() {
		hasStmt := false
		for _, kv := range sp.Attributes() {
			if strings.Contains(kv.Value.String(), "dt_sk_test_x") {
				t.Errorf("span %q leaks the argument in %s", sp.Name(), kv.Key)
			}
			if strings.Contains(kv.Value.String(), "SELECT $1::text") {
				hasStmt = true
			}
		}
		if hasStmt {
			withStatement++
		}
	}
	if withStatement == 0 {
		t.Fatalf("no span carries the statement among %d spans", len(rec.Ended()))
	}
}
