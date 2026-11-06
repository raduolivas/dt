// Package logx builds the structured JSON logger every service uses.
// Sensitive attributes (PII, coordinates, credentials) are always redacted.
package logx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
)

// New returns a JSON logger at level with a permanent service attribute.
// With OTEL_EXPORTER_OTLP_ENDPOINT set, redacted records are also sent to the
// global OTLP LoggerProvider (installed by otelx.Init).
func New(w io.Writer, level slog.Level, service string) *slog.Logger {
	var h slog.Handler = slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	if endpointSet() {
		// The OTLP side always says yes to Enabled, so it gets the same level
		// filter as stdout.
		h = fanout{h, minLevel{otelslog.NewHandler(service, otelslog.WithLoggerProvider(otel.GetLoggerProvider())), level}}
	}
	return slog.New(NewRedactingHandler(h)).With("service", service)
}

// endpointSet mirrors otelx.Enabled without importing otelx (which imports httpx).
func endpointSet() bool { return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" }

// minLevel drops records below min before they reach the wrapped handler.
type minLevel struct {
	slog.Handler
	min slog.Level
}

func (m minLevel) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= m.min && m.Handler.Enabled(ctx, l)
}

func (m minLevel) WithAttrs(attrs []slog.Attr) slog.Handler {
	return minLevel{m.Handler.WithAttrs(attrs), m.min}
}

func (m minLevel) WithGroup(name string) slog.Handler {
	return minLevel{m.Handler.WithGroup(name), m.min}
}

// fanout sends each record to every child handler.
type fanout []slog.Handler

func (f fanout) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range f {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (f fanout) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range f {
		if h.Enabled(ctx, r.Level) {
			errs = append(errs, h.Handle(ctx, r.Clone()))
		}
	}
	return errors.Join(errs...)
}

func (f fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (f fanout) WithGroup(name string) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithGroup(name)
	}
	return out
}

// ParseLevel accepts debug, info, warn or error (case-insensitive).
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("logx: unknown log level %q", s)
}
