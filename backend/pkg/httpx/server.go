package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Server timeouts (spec §11.1).
const (
	ReadHeaderTimeout = 5 * time.Second
	ReadTimeout       = 15 * time.Second
	WriteTimeout      = 30 * time.Second
	IdleTimeout       = 60 * time.Second
	ShutdownTimeout   = 10 * time.Second
)

// Server is an HTTP server bound to a listener, with graceful shutdown.
type Server struct {
	srv *http.Server
	ln  net.Listener
	log *slog.Logger
}

// Listen binds addr and prepares a server for h.
func Listen(addr string, h http.Handler, log *slog.Logger) (*Server, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return nil, err
	}
	return &Server{
		srv: &http.Server{
			Handler:           h,
			ReadHeaderTimeout: ReadHeaderTimeout,
			ReadTimeout:       ReadTimeout,
			WriteTimeout:      WriteTimeout,
			IdleTimeout:       IdleTimeout,
			ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		},
		ln:  ln,
		log: log,
	}, nil
}

// Addr is the bound address, useful when listening on port 0.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Serve serves until ctx is done, then drains in-flight requests for up to
// ShutdownTimeout. It returns nil on a clean shutdown.
func (s *Server) Serve(ctx context.Context) error {
	errc := make(chan error, 1)
	go func() { errc <- s.srv.Serve(s.ln) }()

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	s.log.Info("shutting down", "addr", s.Addr())
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ShutdownTimeout)
	defer cancel()
	if err := s.srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
