package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/raduolivas/dt/backend/services/tracking/internal/domain"
)

// fakeRetention is a RetentionStore over in-memory trails, recording calls.
type fakeRetention struct {
	scopes      []Scope
	trails      map[string][]domain.LatLng // by delivery id, all older than the cutoff
	routes      map[string][]domain.LatLng
	failPoints  string // delivery whose trail cannot be read
	calls       []string
	droppedDay  time.Time
	finalBefore time.Time
}

func (f *fakeRetention) EnsurePartitions(_ context.Context, _ time.Time, ahead int) error {
	f.calls = append(f.calls, "ensure")
	if ahead != PartitionsAhead {
		return errors.New("wrong days ahead")
	}
	return nil
}

func (f *fakeRetention) RetentionScopes(context.Context) ([]Scope, error) { return f.scopes, nil }

func (f *fakeRetention) UnsimplifiedBefore(_ context.Context, _ Scope, _ time.Time) ([]string, error) {
	var ids []string
	for id := range f.trails {
		if _, ok := f.routes[id]; !ok {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (f *fakeRetention) TrailPoints(_ context.Context, _ Scope, id string) ([]domain.LatLng, error) {
	if id == f.failPoints {
		return nil, errors.New("read failed")
	}
	return f.trails[id], nil
}

func (f *fakeRetention) PutSimplifiedRoute(_ context.Context, _ Scope, id string, route []domain.LatLng, _ time.Time) error {
	f.calls = append(f.calls, "simplify")
	f.routes[id] = route
	return nil
}

func (f *fakeRetention) DropTrailPartitionsBefore(_ context.Context, day time.Time) (int, error) {
	f.calls = append(f.calls, "drop")
	f.droppedDay = day
	return 2, nil
}

func (f *fakeRetention) DeleteFinalBefore(_ context.Context, _ Scope, before time.Time) (int64, error) {
	f.calls = append(f.calls, "delete")
	f.finalBefore = before
	return 1, nil
}

// noTime drops the log time, whose digits could pass for a coordinate.
var noTime = &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
	if a.Key == slog.TimeKey {
		return slog.Attr{}
	}
	return a
}}

func newFakeRetention() *fakeRetention {
	line := []domain.LatLng{{Lat: 52.36, Lng: 4.9}, {Lat: 52.361, Lng: 4.9}, {Lat: 52.362, Lng: 4.9}}
	return &fakeRetention{
		scopes: []Scope{{TenantID: "ten_a", Env: "test"}},
		trails: map[string][]domain.LatLng{"del_1": line, "del_2": line},
		routes: map[string][]domain.LatLng{},
	}
}

func TestRetentionDropsAfterSimplifyingAndUsesTheCutoffs(t *testing.T) {
	f := newFakeRetention()
	var logs bytes.Buffer
	svc := &Service{Retention: f, Log: slog.New(slog.NewJSONHandler(&logs, noTime))}
	now := time.Date(2026, 10, 27, 2, 0, 0, 0, time.UTC)
	if err := svc.RunRetention(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.calls, ","); got != "ensure,simplify,simplify,drop,delete" {
		t.Fatalf("calls = %s", got)
	}
	// A straight line keeps only its ends.
	if r := f.routes["del_1"]; len(r) != 2 {
		t.Fatalf("route = %v", r)
	}
	if want := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC); !f.droppedDay.Equal(want) {
		t.Fatalf("dropped before %v, want %v", f.droppedDay, want)
	}
	if want := time.Date(2025, 9, 27, 2, 0, 0, 0, time.UTC); !f.finalBefore.Equal(want) {
		t.Fatalf("final before %v, want %v", f.finalBefore, want)
	}
	// Counts only: no delivery ids, tenant ids or coordinates.
	out := logs.String()
	if !strings.Contains(out, `"simplified":2`) || !strings.Contains(out, `"partitions_dropped":2`) ||
		!strings.Contains(out, `"deliveries_deleted":1`) {
		t.Fatalf("log = %s", out)
	}
	for _, leak := range []string{"del_", "ten_", "52.36", "4.9"} {
		if strings.Contains(out, leak) {
			t.Fatalf("log leaks %q: %s", leak, out)
		}
	}
}

func TestRetentionKeepsPartitionsWhenSimplifyingFails(t *testing.T) {
	f := newFakeRetention()
	f.failPoints = "del_2"
	var logs bytes.Buffer
	svc := &Service{Retention: f, Log: slog.New(slog.NewJSONHandler(&logs, noTime))}
	err := svc.RunRetention(context.Background(), time.Date(2026, 10, 27, 2, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("no error")
	}
	for _, c := range f.calls {
		if c == "drop" {
			t.Fatal("dropped partitions holding a trail that was not simplified")
		}
	}
	// The 13-month deletion still runs.
	if f.calls[len(f.calls)-1] != "delete" {
		t.Fatalf("calls = %v", f.calls)
	}
	if strings.Contains(err.Error(), "del_") || strings.Contains(logs.String(), "del_") {
		t.Fatalf("error or log names a delivery: %v / %s", err, logs.String())
	}
}
