package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"
)

type fakeRetention struct {
	scopes []Scope
	left   map[string]int64 // expired deliveries per tenant
	failOn string
	before time.Time
	limits []int
}

func (f *fakeRetention) RetentionScopes(context.Context) ([]Scope, error) { return f.scopes, nil }

func (f *fakeRetention) DeleteFinalBefore(_ context.Context, s Scope, before time.Time, limit int) (int64, error) {
	if s.TenantID == f.failOn {
		return 0, errors.New("delete failed")
	}
	f.before = before
	f.limits = append(f.limits, limit)
	n := min(f.left[s.TenantID], int64(limit))
	f.left[s.TenantID] -= n
	return n, nil
}

func TestRunRetentionDeletesInBatchesAndLogsCounts(t *testing.T) {
	f := &fakeRetention{
		scopes: []Scope{{TenantID: "ten_a", Env: "test"}, {TenantID: "ten_b", Env: "live"}, {TenantID: "ten_c", Env: "test"}},
		left:   map[string]int64{"ten_a": RetentionBatch + 3, "ten_b": 2},
		failOn: "ten_c",
	}
	var logs bytes.Buffer
	svc := &Service{Retention: f, Log: slog.New(slog.NewJSONHandler(&logs, nil))}
	now := time.Date(2026, 10, 27, 2, 0, 0, 0, time.UTC)
	err := svc.RunRetention(context.Background(), now)
	if err == nil {
		t.Fatal("a failed scope is not reported")
	}
	if f.left["ten_a"] != 0 || f.left["ten_b"] != 0 {
		t.Fatalf("left = %v", f.left)
	}
	if want := time.Date(2025, 9, 27, 2, 0, 0, 0, time.UTC); !f.before.Equal(want) {
		t.Fatalf("before = %v, want %v", f.before, want)
	}
	out := logs.String()
	if !strings.Contains(out, `"deleted":`+strconv.Itoa(RetentionBatch+5)) || strings.Contains(out, "ten_") {
		t.Fatalf("log = %s", out)
	}
}
