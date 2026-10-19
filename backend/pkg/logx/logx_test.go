package logx_test

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/raduolivas/dt/backend/pkg/logx"
)

func logTo(buf *bytes.Buffer) *slog.Logger { return logx.New(buf, slog.LevelDebug, "test") }

func TestRedactsSensitiveTopLevelKeys(t *testing.T) {
	var buf bytes.Buffer
	logTo(&buf).Info("x", "lat", 52.37, "first_name", "Sam", "api_key", "dt_sk_test_abc", "status", "picked_up")
	out := buf.String()
	for _, leak := range []string{"52.37", "Sam", "dt_sk_test_abc"} {
		if strings.Contains(out, leak) {
			t.Fatalf("leaked %q in %s", leak, out)
		}
	}
	for _, want := range []string{`"status":"picked_up"`, `"service":"test"`, `"lat":"[REDACTED]"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s in %s", want, out)
		}
	}
}

func TestRedactsInsideGroupsAndWith(t *testing.T) {
	var buf bytes.Buffer
	logTo(&buf).With("notes", "ring the bell").Info("x", slog.Group("courier", "lng", 4.9, "vehicle", "e_bike"))
	out := buf.String()
	// Match the JSON value, not the bare number: "4.9" also occurs in timestamps.
	if strings.Contains(out, "ring the bell") || strings.Contains(out, `"lng":4.9`) {
		t.Fatalf("leak: %s", out)
	}
	if !strings.Contains(out, `"courier":{"lng":"[REDACTED]"`) {
		t.Fatalf("lng not redacted: %s", out)
	}
	if !strings.Contains(out, `"vehicle":"e_bike"`) {
		t.Fatalf("over-redacted: %s", out)
	}
}

func TestRedactsWholeSensitiveGroup(t *testing.T) {
	var buf bytes.Buffer
	logTo(&buf).Info("x", slog.Group("position", "x", 1, "y", 2))
	if !strings.Contains(buf.String(), `"position":"[REDACTED]"`) {
		t.Fatalf("got %s", buf.String())
	}
}

func TestKeyMatchingIsCaseInsensitiveAndBySuffix(t *testing.T) {
	var buf bytes.Buffer
	logTo(&buf).Info("x", "Authorization", "Bearer abc", "refresh_token", "r1", "webhook_secret", "w1", "DB_PASSWORD", "p1")
	for _, leak := range []string{"Bearer abc", "r1", "w1", "p1"} {
		if strings.Contains(buf.String(), leak) {
			t.Fatalf("leaked %q", leak)
		}
	}
}

func TestParseLevel(t *testing.T) {
	if l, err := logx.ParseLevel("warn"); err != nil || l != slog.LevelWarn {
		t.Fatalf("got %v %v", l, err)
	}
	if _, err := logx.ParseLevel("loud"); err == nil {
		t.Fatal("expected error")
	}
}

func TestRedactsUnderSensitiveWithGroup(t *testing.T) {
	var buf bytes.Buffer
	logTo(&buf).WithGroup("location").Info("x", "x", 52.1234, "y", 4.5678)
	out := buf.String()
	if strings.Contains(out, "52.1234") || strings.Contains(out, "4.5678") {
		t.Fatalf("leak: %s", out)
	}
}

func TestRedactsCompoundKeyNames(t *testing.T) {
	var buf bytes.Buffer
	logTo(&buf).Info("x",
		"customer_first_name", "v01", "courier_name", "v02", "dropoff_lat", "v03", "pickup_address", "v04",
		"recipient_phone", "v05", "customer_email", "v06", "apikey", "v07", "jwt", "v08", "bearer", "v09",
		"dsn", "v10", "database_url", "v11", "dropoff.lng", "v12", "x-api-key", "v13",
		"delivery_id", "kept")
	out := buf.String()
	for i := 1; i <= 13; i++ {
		if leak := fmt.Sprintf("v%02d", i); strings.Contains(out, leak) {
			t.Fatalf("leaked %s in %s", leak, out)
		}
	}
	if !strings.Contains(out, `"delivery_id":"kept"`) {
		t.Fatalf("over-redacted: %s", out)
	}
}

type coord struct {
	Lat float64
	Lng float64 `json:"lng"`
}

type stop struct {
	Kind     string `json:"kind"`
	Position coord  `json:"position"`
	Contact  map[string]any
}

func TestRedactsInsideStructAndMapValues(t *testing.T) {
	var buf bytes.Buffer
	logTo(&buf).Info("x",
		"dropoff", coord{Lat: 52.3711, Lng: 4.8911},
		"ptr", &coord{Lat: 52.3722, Lng: 4.8922},
		"stops", []stop{{Kind: "pickup", Contact: map[string]any{"first_name": "Sam", "notes": "ring", "floor": 3}}},
		"customer", map[string]any{"first_name": "Alex", "tier": "gold"},
		"err", errors.New("boom"))
	out := buf.String()
	for _, leak := range []string{"52.3711", "4.8911", "52.3722", "4.8922", "Sam", "ring", "Alex"} {
		if strings.Contains(out, leak) {
			t.Fatalf("leaked %q in %s", leak, out)
		}
	}
	for _, want := range []string{`"Lat":"[REDACTED]"`, `"kind":"pickup"`, `"floor":3`, `"tier":"gold"`, `"err":"boom"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s in %s", want, out)
		}
	}
}
