package app_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/raduolivas/dt/backend/services/deliveries/internal/app"
)

func TestSweeperWarnsOnceWhenDue(t *testing.T) {
	r := newFakeRepo()
	r.due = []app.PickupWatch{{DeliveryID: "del_1"}, {DeliveryID: "del_2"}, {DeliveryID: "del_3"}}
	r.warnErrs["del_2"] = errors.New("boom")
	sw := &app.PickupSweeper{Repo: r, Clock: &fakeClock{now: t0}, Log: slog.New(slog.DiscardHandler)}

	sw.Sweep(context.Background())

	if got := r.warned; len(got) != 3 || got[0] != "del_1" || got[1] != "del_2" || got[2] != "del_3" {
		t.Fatalf("warned %v", got)
	}
}

func TestSweeperRunStopsOnCancel(t *testing.T) {
	r := newFakeRepo()
	r.due = []app.PickupWatch{{DeliveryID: "del_1"}}
	sw := &app.PickupSweeper{Repo: r, Clock: &fakeClock{now: t0}, Interval: time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sw.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if len(r.warned) == 0 {
		t.Fatal("no sweep ran")
	}
}
