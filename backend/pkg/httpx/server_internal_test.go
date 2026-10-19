package httpx

import (
	"io"
	"log/slog"
	"net/http"
	"testing"
)

func TestServerTimeouts(t *testing.T) {
	s, err := Listen("127.0.0.1:0", http.NotFoundHandler(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer s.ln.Close()
	if s.srv.ReadHeaderTimeout != ReadHeaderTimeout || s.srv.ReadTimeout != ReadTimeout ||
		s.srv.WriteTimeout != WriteTimeout || s.srv.IdleTimeout != IdleTimeout {
		t.Fatalf("timeouts = %v %v %v %v", s.srv.ReadHeaderTimeout, s.srv.ReadTimeout, s.srv.WriteTimeout, s.srv.IdleTimeout)
	}
	if ShutdownTimeout.Seconds() != 10 {
		t.Fatalf("shutdown timeout = %v", ShutdownTimeout)
	}
}
