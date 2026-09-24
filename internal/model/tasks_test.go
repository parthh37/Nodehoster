package model

import (
	"strings"
	"testing"
)

func workerSite() *Site {
	s := &Site{ID: "w", Name: "queue", Type: SiteWorker, Node: &NodeConfig{AppRoot: `C:\apps\queue`, Script: "worker.js"}}
	s.ApplyDefaults()
	return s
}

func TestWorkerDefaultsAndValidation(t *testing.T) {
	s := workerSite()
	if !s.RunsNode() || s.Node.Instances != 1 || s.Node.RestartPolicy != "always" || s.Deploy.InstallCommand == "" {
		t.Fatalf("worker defaults not applied: %+v", s.Node)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("plain worker rejected: %v", err)
	}

	cases := map[string]func(*Site){
		"bindings":                 func(s *Site) { s.Bindings = []Binding{{Protocol: "http", Port: 80}} },
		"node.portMode":            func(s *Site) { s.Node.PortMode = "fixed"; s.Node.FixedPort = 3000 },
		"node.healthCheck.enabled": func(s *Site) { s.Node.HealthCheck.Enabled = true },
		"node.loadBalancer":        func(s *Site) { s.Node.LoadBalancer.Enabled = true },
		"node.recycle.maxRequests": func(s *Site) { s.Node.Recycle.MaxRequests = 1000 },
		"routing.locations":        func(s *Site) { s.Routing.Locations = []Location{{Path: "/a", Kind: "url", URL: "http://x"}} },
		"routing.basicAuth":        func(s *Site) { s.Routing.BasicAuth.Enabled = true },
		"routing.maintenance":      func(s *Site) { s.Routing.Maintenance.Enabled = true },
		"routing.httpsRedirect":    func(s *Site) { s.Routing.HTTPSRedirect = true },
		"routing.affinity":         func(s *Site) { s.Routing.Affinity.Enabled = true },
		"routing.cache":            func(s *Site) { s.Routing.Cache.Enabled = true },
		"node.script":              func(s *Site) { s.Node.Script = "" },
		"node.restartPolicy":       func(s *Site) { s.Node.RestartPolicy = "sometimes" },
	}
	for field, mutate := range cases {
		s := workerSite()
		mutate(s)
		s.ApplyDefaults()
		err := s.Validate()
		ve, ok := err.(*ValidationError)
		if !ok || !strings.HasPrefix(ve.Field, field) {
			t.Errorf("%s: got %v", field, err)
		}
	}

	// What a node site keeps by default (compression, access log…) is
	// harmless on a worker and must not be refused.
	s = workerSite()
	s.Routing.Compression, s.Routing.WebSockets, s.Routing.AccessLog = true, true, true
	s.Node.Recycle = RecycleConfig{MemoryLimitMB: 512, PeriodicMinutes: 60, ScheduleTimes: []string{"03:00"}}
	s.Node.WatchFiles = true
	if err := s.Validate(); err != nil {
		t.Fatalf("worker with recycling rejected: %v", err)
	}
}

func TestTaskValidation(t *testing.T) {
	task := func() ScheduledTask {
		return ScheduledTask{ID: "t1", Name: "cleanup", Schedule: "0 3 * * *", Script: "jobs/cleanup.js", Enabled: true}
	}
	s := nodeSite()
	s.Tasks = []ScheduledTask{task(), {Name: "manual", NpmScript: "migrate"}}
	s.ApplyDefaults()
	if err := s.Validate(); err != nil {
		t.Fatalf("valid tasks rejected: %v", err)
	}
	if s.Tasks[0].TimeoutSec != DefaultTaskTimeoutSec || s.Tasks[0].Overlap != OverlapSkip {
		t.Fatalf("task defaults not applied: %+v", s.Tasks[0])
	}

	cases := map[string]func(*Site){
		"tasks[0].name":       func(s *Site) { s.Tasks[0].Name = "" },
		"tasks[1].name":       func(s *Site) { s.Tasks = append(s.Tasks, task()); s.Tasks[1].ID = "t2"; s.Tasks[1].Name = "CLEANUP" },
		"tasks[1].id":         func(s *Site) { s.Tasks = append(s.Tasks, task()); s.Tasks[1].Name = "other" },
		"tasks[0].schedule":   func(s *Site) { s.Tasks[0].Schedule = "every day" },
		"tasks[0].schedule/":  func(s *Site) { s.Tasks[0].Schedule = "0 0 30 2 *" }, // never runs
		"tasks[0].script":     func(s *Site) { s.Tasks[0].Script = " " },
		"tasks[0].overlap":    func(s *Site) { s.Tasks[0].Overlap = "sometimes" },
		"tasks[0].timeoutSec": func(s *Site) { s.Tasks[0].TimeoutSec = MaxTaskTimeoutSec + 1 },
		"tasks[0].env[0].name": func(s *Site) {
			s.Tasks[0].Env = []EnvVar{{Name: "1BAD", Value: "x"}}
		},
		"tasks": func(s *Site) {
			s.Type, s.Node, s.Static = SiteStatic, nil, &StaticConfig{Root: `C:\www`}
		},
	}
	for name, mutate := range cases {
		field := strings.TrimSuffix(name, "/")
		s := nodeSite()
		s.Tasks = []ScheduledTask{task()}
		mutate(s)
		s.ApplyDefaults()
		err := s.Validate()
		ve, ok := err.(*ValidationError)
		if !ok || ve.Field != field {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

func TestSiteTaskLookup(t *testing.T) {
	s := nodeSite()
	s.Tasks = []ScheduledTask{{ID: "a1", Name: "Nightly"}, {ID: "nightly", Name: "Other"}}
	if tk, ok := s.Task("nightly"); !ok || tk.ID != "nightly" {
		t.Fatalf("an id must win over a name: %+v", tk)
	}
	if tk, ok := s.Task("NIGHTLY"); !ok || tk.ID != "a1" {
		t.Fatalf("lookup by name: %+v %v", tk, ok)
	}
	if _, ok := s.Task("missing"); ok {
		t.Fatal("found a missing task")
	}
}
