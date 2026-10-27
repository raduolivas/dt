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
