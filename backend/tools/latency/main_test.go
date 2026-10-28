package main

import (
	"testing"
	"time"
)

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func TestLatencyPercentiles(t *testing.T) {
	var sample []time.Duration
	for i := 100; i >= 1; i-- { // unsorted on purpose
		sample = append(sample, ms(i))
	}
	got, err := summarize(sample)
	if err != nil {
		t.Fatal(err)
	}
	if got.P50 != ms(50) || got.P95 != ms(95) || got.P99 != ms(99) || got.N != 100 {
		t.Fatalf("1..100 ms: got %+v", got)
	}
	// The input is left as it was.
	if sample[0] != ms(100) {
		t.Fatal("summarize reordered its input")
	}

	small, err := summarize([]time.Duration{ms(30), ms(10), ms(20)})
	if err != nil {
		t.Fatal(err)
	}
	if small.P50 != ms(20) || small.P95 != ms(30) || small.P99 != ms(30) {
		t.Fatalf("3 samples: got %+v", small)
	}

	one, err := summarize([]time.Duration{ms(7)})
	if err != nil || one.P50 != ms(7) || one.P99 != ms(7) {
		t.Fatalf("1 sample: got %+v, %v", one, err)
	}

	if _, err := summarize(nil); err == nil {
		t.Fatal("no samples must be an error")
	}
}

func TestPassesThreshold(t *testing.T) {
	if !(percentiles{P95: ms(999)}).ok() || (percentiles{P95: ms(1000)}).ok() {
		t.Fatal("p95 must be under 1000 ms")
	}
}

func TestClockResult(t *testing.T) {
	base := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	k := newClock()
	k.watch("w")
	fix := func(i int) time.Time { return base.Add(time.Duration(i) * time.Second) }
	// Watched: fix 0 seen 40 ms after the answer, fix 1 seen 10 ms before it
	// (negative), fix 2 never seen (lost), fix 3 seen twice (first sight counts).
	for i := 0; i < 4; i++ {
		k.answer("w", fix(i), fix(i).Add(100*time.Millisecond))
	}
	k.see("w", fix(0), fix(0).Add(140*time.Millisecond))
	k.see("w", fix(1), fix(1).Add(90*time.Millisecond))
	k.see("w", fix(3), fix(3).Add(150*time.Millisecond))
	k.see("w", fix(3), fix(3).Add(900*time.Millisecond))
	// An unwatched delivery's fixes are not counted, seen or not.
	k.answer("u", fix(0), fix(0))
	// A snapshot for a fix the relay never accepted is ignored.
	k.see("w", fix(9), fix(9))

	o := k.result()
	if o.Answered != 4 || o.Lost != 1 || o.Negative != 1 || len(o.Latencies) != 3 {
		t.Fatalf("got %+v", o)
	}
	want := map[time.Duration]int{ms(40): 1, 0: 1, ms(50): 1}
	for _, d := range o.Latencies {
		want[d]--
	}
	for d, n := range want {
		if n != 0 {
			t.Fatalf("latency %v: off by %d in %v", d, n, o.Latencies)
		}
	}
	if got := o.LossRate(); got != 0.25 {
		t.Fatalf("loss rate %v", got)
	}
	if (outcome{}).LossRate() != 0 {
		t.Fatal("no answers is no loss")
	}
}
