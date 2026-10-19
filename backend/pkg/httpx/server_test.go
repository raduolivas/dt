package httpx_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/raduolivas/dt/backend/pkg/httpx"
	"github.com/raduolivas/dt/backend/pkg/logx"
)

func TestServerFinishesInFlightRequestOnShutdown(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})
	s, err := httpx.Listen("127.0.0.1:0", h, logx.New(io.Discard, slog.LevelDebug, "test"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- s.Serve(ctx) }()

	got := make(chan int, 1)
	go func() {
		resp, err := http.Get("http://" + s.Addr())
		if err != nil {
			t.Error(err)
			got <- 0
			return
		}
		resp.Body.Close()
		got <- resp.StatusCode
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()

	if code := <-got; code != http.StatusOK {
		t.Fatalf("in-flight request got %d", code)
	}
	if err := <-served; err != nil {
		t.Fatalf("Serve returned %v", err)
	}
}
