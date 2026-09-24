package alerts

import (
	"math"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

var testBounds = []float64{10, 100, 1000}

func nodeSite(instances int) *model.Site {
	return &model.Site{ID: "s1", Name: "api.example.com", Type: model.SiteNode, Node: &model.NodeConfig{Instances: instances, AgentEnabled: true}}
}

func ready(index int, cpu float64, memMB uint64) model.InstanceStatus {
	return model.InstanceStatus{Index: index, PID: 100 + index, State: "ready", Healthy: true, CPUPercent: cpu, MemoryBytes: memMB << 20}
}

func TestMeasureSiteStates(t *testing.T) {
	r := model.AlertRule{ID: "cpu", Metric: model.AlertCPU, Threshold: 50}
	in := SiteSample{Site: nodeSite(1), Status: model.SiteStatus{Instances: []model.InstanceStatus{ready(0, 90, 100)}}}
	for state, want := range map[model.SiteState]Observation{
		model.StateRunning: Breach, model.StateDegraded: Breach,
		model.StateStopped: Clear, model.StateStopping: Clear, model.StateFailed: Clear,
		model.StateStarting: NoData,
	} {
		in.Status.State = state
		if got, _ := measureSite(r, in, &window{}, testBounds); got != want {
			t.Errorf("%s: %v, want %v", state, got, want)
		}
	}
	// Instances down is what a failed site is about.
	in.Status.State = model.StateFailed
	in.Status.Instances = []model.InstanceStatus{{Index: 0, State: "exited"}}
	down := model.AlertRule{ID: "down", Metric: model.AlertInstancesDown, Threshold: 0}
	if got, rd := measureSite(down, in, &window{}, testBounds); got != Breach || rd.Value != 1 || rd.Detail != "of 1" {
		t.Fatalf("failed site: %v %+v", got, rd)
	}
}

func TestMeasureSiteProcesses(t *testing.T) {
	s := nodeSite(3)
	s.Node.Limits.MemoryLimitMB = 1000
	unhealthy := ready(2, 5, 950)
	unhealthy.Healthy = false
	unhealthy.EventLoopLagMs = 400
	st := model.SiteStatus{State: model.StateDegraded, Instances: []model.InstanceStatus{ready(0, 70, 300), ready(1, 30, 200), unhealthy}}
	in := SiteSample{Site: s, Status: st}
	cases := []struct {
		metric string
		value  float64
		detail string
	}{
		{model.AlertCPU, 105, ""},
		{model.AlertInstanceCPU, 70, "instance 0"},
		{model.AlertMemory, 1450, ""},
		{model.AlertMemoryPercent, 95, "instance 2: 950 of 1000 MB"},
		{model.AlertEventLoopLag, 400, "instance 2"},
		{model.AlertInstancesDown, 1, "of 3"},
	}
	for _, c := range cases {
		_, rd := measureSite(model.AlertRule{Metric: c.metric, Threshold: 1e6}, in, &window{}, testBounds)
		if math.Abs(rd.Value-c.value) > 0.001 || rd.Detail != c.detail {
			t.Errorf("%s: %+v, want %v %q", c.metric, rd, c.value, c.detail)
		}
	}
	// Starting instances are not judged yet.
	st.Instances = []model.InstanceStatus{{Index: 0, PID: 5, State: "starting", CPUPercent: 99}}
	if obs, _ := measureSite(model.AlertRule{Metric: model.AlertInstanceCPU, Threshold: 50}, SiteSample{Site: s, Status: st}, &window{}, testBounds); obs != NoData {
		t.Fatalf("starting instance: %v", obs)
	}
}

func TestMeasureRates(t *testing.T) {
	t0 := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	w := &window{}
	site := &model.Site{ID: "s", Name: "web", Type: model.SiteStatic}
	sample := func(req, errs int64, avgMs float64, hist []int64) SiteSample {
		return SiteSample{Site: site, Status: model.SiteStatus{State: model.StateRunning,
			Traffic: model.TrafficStats{Requests: req, Status5xx: errs, AvgLatencyMs: avgMs}}, Latency: hist}
	}
	// Ten minutes of history: 1000 requests with 10 errors before the
	// window, then 100 requests with 20 errors in the last 5 minutes.
	w.add(sampleCounters(t0, sample(0, 0, 0, []int64{0, 0, 0, 0})))
	w.add(sampleCounters(t0.Add(5*time.Minute), sample(1000, 10, 20, []int64{900, 100, 0, 0})))
	now := t0.Add(10 * time.Minute)
	// 100 more at 520 ms each: the average over 1100 is (1000*20 + 100*520)/1100.
	in := sample(1100, 30, (1000*20+100*520)/1100.0, []int64{900, 100, 90, 10})
	w.add(sampleCounters(now, in))

	rule := func(metric string, threshold float64, minReq int) model.AlertRule {
		return model.AlertRule{Metric: metric, Threshold: threshold, WindowMinutes: 5, MinRequests: minReq}
	}
	obs, rd := measureSite(rule(model.AlertErrorRate, 5, 20), in, w, testBounds)
	if obs != Breach || rd.Value != 20 || rd.Detail != "20 of 100 requests in 5 min" {
		t.Fatalf("error rate: %v %+v", obs, rd)
	}
	_, rd = measureSite(rule(model.AlertLatency, 1000, 20), in, w, testBounds)
	if math.Abs(rd.Value-520) > 0.001 {
		t.Fatalf("latency: %+v", rd)
	}
	// p95 of 90 requests in 100-1000 ms and 10 slower: rank 95 is the 5th
	// of the last bucket, reported as its lower bound.
	obs, rd = measureSite(rule(model.AlertLatencyP95, 500, 20), in, w, testBounds)
	if obs != Breach || rd.Value != 1000 {
		t.Fatalf("p95: %v %+v", obs, rd)
	}
	// Too few requests to judge counts as clear, however bad.
	obs, rd = measureSite(rule(model.AlertErrorRate, 5, 500), in, w, testBounds)
	if obs != Clear || rd.Detail != "only 20 of 100 requests in 5 min" {
		t.Fatalf("few requests: %v %+v", obs, rd)
	}
	// A window longer than the history uses what there is.
	obs, rd = measureSite(model.AlertRule{Metric: model.AlertErrorRate, Threshold: 1, WindowMinutes: 30, MinRequests: 1}, in, w, testBounds)
	if obs != Breach || math.Abs(rd.Value-30.0/1100*100) > 0.001 {
		t.Fatalf("long window: %v %+v", obs, rd)
	}
}

func TestWindow(t *testing.T) {
	t0 := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	w := &window{}
	if _, ok := w.delta(time.Minute); ok {
		t.Fatal("delta of nothing")
	}
	for i := range 200 { // 50 minutes every 15 s
		w.add(counters{at: t0.Add(time.Duration(i) * 15 * time.Second), requests: int64(i * 10)})
	}
	if d := w.buf[len(w.buf)-1].at.Sub(w.buf[0].at); d > keepWindow || d < keepWindow-15*time.Second {
		t.Fatalf("keeps %s", d)
	}
	d, _ := w.delta(5 * time.Minute)
	if d.requests != 200 { // 20 evaluations of 10
		t.Fatalf("5 min delta = %d", d.requests)
	}
	// Counters that go backwards (the site was recreated) start over.
	w.add(counters{at: t0.Add(51 * time.Minute), requests: 3})
	if len(w.buf) != 1 {
		t.Fatalf("after a reset: %d entries", len(w.buf))
	}
}

func TestPercentile(t *testing.T) {
	cases := []struct {
		counts []int64
		p      float64
		want   float64
		ok     bool
	}{
		{[]int64{0, 0, 0, 0}, 0.95, 0, false},
		{[]int64{100, 0, 0, 0}, 0.95, 9.5, true},   // within the first bucket (0-10)
		{[]int64{50, 50, 0, 0}, 0.95, 91, true},    // rank 95: 45 of 50 into 10-100
		{[]int64{0, 0, 0, 7}, 0.5, 1000, true},     // slower than every bound
		{[]int64{10, 0, 0, 0}, 1, 10, true},        // the maximum is the bucket's bound
		{[]int64{0, 0, 100, 0}, 0.01, 109, true},   // 1 of 100 into 100-1000
		{[]int64{1, 1, 1, 1}, 0.95, 1000, true},    // rank 3.8: the last bucket
		{[]int64{0, 2, 0, 0}, 0.5, 55, true},       // halfway into 10-100
		{[]int64{3, 0, 0, 0, 5}, 0.99, 1000, true}, // more counts than bounds+1: tolerated
	}
	for _, c := range cases {
		got, ok := Percentile(testBounds, c.counts, c.p)
		if ok != c.ok || math.Abs(got-c.want) > 0.001 {
			t.Errorf("%v p%.0f = %v %v, want %v %v", c.counts, c.p*100, got, ok, c.want, c.ok)
		}
	}
}

func TestMeasureServer(t *testing.T) {
	in := ServerSample{CPUPercent: 97, MemoryPercent: -1, Disks: []Disk{{`C:\`, 40}, {`D:\`, 6}}}
	if obs, rd := measureServer(model.AlertRule{Metric: model.AlertServerCPU, Threshold: 90}, in); obs != Breach || rd.Value != 97 {
		t.Fatalf("cpu: %v %+v", obs, rd)
	}
	if obs, _ := measureServer(model.AlertRule{Metric: model.AlertServerMemory, Threshold: 90}, in); obs != NoData {
		t.Fatalf("memory unknown: %v", obs)
	}
	if obs, rd := measureServer(model.AlertRule{Metric: model.AlertDiskFree, Threshold: 10}, in); obs != Breach || rd.Value != 6 || rd.Detail != `D:\` {
		t.Fatalf("disk: %v %+v", obs, rd)
	}
	if obs, _ := measureServer(model.AlertRule{Metric: model.AlertDiskFree, Threshold: 5}, in); obs != Clear {
		t.Fatalf("disk within the limit: %v", obs)
	}
}
