package proxy

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// siteStats counts traffic for one site. Totals are atomics updated on the
// request path; the per-second ring backs the requests-per-second figure and
// the per-minute window feeds the metrics history.
type siteStats struct {
	requests, s2xx, s3xx, s4xx, s5xx atomic.Int64
	bytesIn, bytesOut                atomic.Int64
	latencyMicros                    atomic.Int64

	mu       sync.Mutex
	secs     [60]int64
	secEpoch [60]int64
	minReq   int64
	minErr   int64
	minLat   int64
}

func (s *siteStats) record(status int, in, out int64, d time.Duration) {
	s.requests.Add(1)
	switch {
	case status >= 500:
		s.s5xx.Add(1)
	case status >= 400:
		s.s4xx.Add(1)
	case status >= 300:
		s.s3xx.Add(1)
	default:
		s.s2xx.Add(1)
	}
	s.bytesIn.Add(in)
	s.bytesOut.Add(out)
	s.latencyMicros.Add(d.Microseconds())

	now := time.Now().Unix()
	i := now % 60
	s.mu.Lock()
	if s.secEpoch[i] != now {
		s.secEpoch[i], s.secs[i] = now, 0
	}
	s.secs[i]++
	s.minReq++
	if status >= 500 {
		s.minErr++
	}
	s.minLat += d.Microseconds()
	s.mu.Unlock()
}

func (s *siteStats) snapshot() model.TrafficStats {
	t := model.TrafficStats{
		Requests:  s.requests.Load(),
		Status2xx: s.s2xx.Load(), Status3xx: s.s3xx.Load(), Status4xx: s.s4xx.Load(), Status5xx: s.s5xx.Load(),
		BytesIn: s.bytesIn.Load(), BytesOut: s.bytesOut.Load(),
	}
	if t.Requests > 0 {
		t.AvgLatencyMs = float64(s.latencyMicros.Load()) / float64(t.Requests) / 1000
	}
	now := time.Now().Unix()
	var sum int64
	s.mu.Lock()
	for i := range s.secs {
		if now-s.secEpoch[i] < 60 {
			sum += s.secs[i]
		}
	}
	s.mu.Unlock()
	t.RPS = float64(sum) / 60
	return t
}

// takeMinute returns and resets the counters since the previous call.
func (s *siteStats) takeMinute() (req, errs int64, avgLatMs float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req, errs = s.minReq, s.minErr
	if req > 0 {
		avgLatMs = float64(s.minLat) / float64(req) / 1000
	}
	s.minReq, s.minErr, s.minLat = 0, 0, 0
	return
}
