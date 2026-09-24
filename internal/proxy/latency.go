package proxy

import (
	"sync/atomic"
	"time"
)

// LatencyBounds are the upper bounds, in milliseconds, of the buckets of
// each site's response time histogram; a last bucket counts everything
// slower. Resource alerts take percentiles (p95) from the difference of
// two readings, so a request costs one atomic add here, nothing more.
var LatencyBounds = []float64{5, 10, 25, 50, 100, 250, 500, 750, 1000, 1500, 2000, 3000, 5000, 10000, 30000, 60000}

// latencyHist counts requests per LatencyBounds bucket since the site's
// counters were created.
type latencyHist struct {
	counts [17]atomic.Int64 // len(LatencyBounds) + 1
}

func (h *latencyHist) observe(d time.Duration) {
	ms := float64(d.Microseconds()) / 1000
	i := 0
	for i < len(LatencyBounds) && ms > LatencyBounds[i] {
		i++
	}
	h.counts[i].Add(1)
}

func (h *latencyHist) snapshot() []int64 {
	out := make([]int64, len(h.counts))
	for i := range h.counts {
		out[i] = h.counts[i].Load()
	}
	return out
}

// LatencyHistogram is a site's cumulative response time histogram: the
// requests answered within each of LatencyBounds (and slower, last).
func (s *Server) LatencyHistogram(id string) []int64 {
	return s.statsFor(id).latency.snapshot()
}
