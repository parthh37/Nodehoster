package proxy

import (
	"slices"
	"testing"
	"time"
)

func TestLatencyHistogram(t *testing.T) {
	if len(LatencyBounds)+1 != len(latencyHist{}.counts) {
		t.Fatalf("%d bounds for %d buckets", len(LatencyBounds), len(latencyHist{}.counts))
	}
	if !slices.IsSorted(LatencyBounds) {
		t.Fatal("bounds are not ascending")
	}
	var s siteStats
	for _, d := range []time.Duration{time.Millisecond, 5 * time.Millisecond, 5100 * time.Microsecond, 400 * time.Millisecond, 2 * time.Minute} {
		s.record(200, 0, 0, d)
	}
	got := s.latency.snapshot()
	want := make([]int64, len(got))
	want[0] = 2  // 1 ms and exactly 5 ms: at most 5 ms
	want[1] = 1  // 5.1 ms
	want[6] = 1  // 400 ms: at most 500
	want[16] = 1 // slower than the last bound
	if !slices.Equal(got, want) {
		t.Fatalf("histogram = %v, want %v", got, want)
	}
}
