// Package logx builds the structured JSON logger every service uses.
// Sensitive attributes (PII, coordinates, credentials) are always redacted.
package logx

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// New returns a JSON logger at level with a permanent service attribute.
func New(w io.Writer, level slog.Level, service string) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(NewRedactingHandler(h)).With("service", service)
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
