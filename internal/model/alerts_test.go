package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAlertDefaults(t *testing.T) {
	var s AlertSettings // settings saved before alerts existed
	s.ApplyDefaults()
	if s.Enabled || len(s.SiteRules) == 0 || len(s.ServerRules) == 0 || s.RecoveryMinutes != 2 || s.EmailTo == nil {
		t.Fatalf("defaults = %+v", s)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	// A saved configuration keeps its choices: rules removed stay removed.
	s = AlertSettings{Enabled: true, SiteRules: []AlertRule{}}
	s.ApplyDefaults()
	if len(s.SiteRules) != 0 || len(s.ServerRules) != 0 || s.RecoveryMinutes != 2 {
		t.Fatalf("partial = %+v", s)
	}
	// Unnamed rules are named after their metric; windowed ones get a window.
	s = AlertSettings{Enabled: true, SiteRules: []AlertRule{
		{Metric: AlertErrorRate, Threshold: 5}, {Metric: AlertErrorRate, Threshold: 10}, {ID: "cpu", Metric: AlertCPU, Threshold: 80, WindowMinutes: 9},
	}}
	s.ApplyDefaults()
	r := s.SiteRules
	if r[0].ID != "errorrate" || r[1].ID != "errorrate-2" || r[0].WindowMinutes != 5 || r[0].MinRequests != 20 || r[0].Severity != SeverityWarning {
		t.Fatalf("named = %+v", r)
	}
	if r[2].WindowMinutes != 0 {
		t.Fatalf("a window on a non-rate metric was kept: %+v", r[2])
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestAlertValidation(t *testing.T) {
	cases := map[string]func(*AlertSettings){
		"alerts.siteRules[0].metric":        func(s *AlertSettings) { s.SiteRules[0].Metric = "load" },
		"alerts.siteRules[1].metric":        func(s *AlertSettings) { s.SiteRules[1].Metric = AlertDiskFree },
		"alerts.serverRules[0].metric":      func(s *AlertSettings) { s.ServerRules[0].Metric = AlertCPU },
		"alerts.siteRules[0].threshold":     func(s *AlertSettings) { s.SiteRules[0].Threshold = -1 },
		"alerts.serverRules[2].threshold":   func(s *AlertSettings) { s.ServerRules[2].Threshold = 101 },
		"alerts.siteRules[0].forMinutes":    func(s *AlertSettings) { s.SiteRules[0].ForMinutes = 2000 },
		"alerts.siteRules[0].severity":      func(s *AlertSettings) { s.SiteRules[0].Severity = "page" },
		"alerts.siteRules[3].windowMinutes": func(s *AlertSettings) { s.SiteRules[3].WindowMinutes = 90 },
		"alerts.siteRules[0].repeatHours":   func(s *AlertSettings) { s.SiteRules[0].RepeatHours = 1000 },
		"alerts.siteRules[1].id":            func(s *AlertSettings) { s.SiteRules[1].ID = "CPU" },
		"alerts.serverRules[0].id":          func(s *AlertSettings) { s.ServerRules[0].ID = "errors" },
		"alerts.siteRules[2].id":            func(s *AlertSettings) { s.SiteRules[2].ID = "has space" },
		"alerts.recoveryMinutes":            func(s *AlertSettings) { s.RecoveryMinutes = 61 },
		"alerts.emailTo[1]":                 func(s *AlertSettings) { s.EmailTo = []string{"ops@example.com", "ops"} },
	}
	for field, mutate := range cases {
		s := DefaultAlerts()
		mutate(&s)
		s.ApplyDefaults()
		err := s.Validate()
		ve, ok := err.(*ValidationError)
		if !ok || ve.Field != field {
			t.Errorf("%s: got %v", field, err)
		}
	}
}

func TestSiteAlertValidation(t *testing.T) {
	static := &Site{Name: "files", Type: SiteStatic, Static: &StaticConfig{Root: `C:\www`}}
	static.Alerts.Rules = []AlertRule{{Metric: AlertEventLoopLag, Threshold: 100}}
	static.Alerts.applyDefaults()
	if static.Alerts.Rules[0].ID != "site-eventlooplag" {
		t.Fatalf("id = %q", static.Alerts.Rules[0].ID)
	}
	err := static.Alerts.validate(static)
	if ve, ok := err.(*ValidationError); !ok || ve.Field != "alerts.rules[0].metric" || !strings.Contains(ve.Message, "Node.js") {
		t.Fatalf("lag on a static site: %v", err)
	}
	// Turning a server-wide rule off is fine whatever its metric.
	static.Alerts.Rules[0].Disabled = true
	if err := static.Alerts.validate(static); err != nil {
		t.Fatal(err)
	}

	worker := &Site{Name: "queue", Type: SiteWorker, Node: &NodeConfig{}}
	worker.Alerts.Rules = []AlertRule{{ID: "e", Metric: AlertErrorRate, Threshold: 5, Severity: SeverityCritical, WindowMinutes: 5}}
	if err := worker.Alerts.validate(worker); err == nil || !strings.Contains(err.Error(), "workers") {
		t.Fatalf("error rate on a worker: %v", err)
	}
	worker.Alerts.Rules[0] = AlertRule{ID: "disk", Metric: AlertDiskFree, Threshold: 5, Severity: SeverityWarning}
	if err := worker.Alerts.validate(worker); err == nil || !strings.Contains(err.Error(), "server metric") {
		t.Fatalf("server metric on a site: %v", err)
	}
}

func TestEffectiveAlertRules(t *testing.T) {
	defaults := DefaultAlerts().SiteRules
	node := &Site{Name: "api", Type: SiteNode, Node: &NodeConfig{Instances: 2}}
	ids := func(rules []AlertRule) string {
		var out []string
		for _, r := range rules {
			out = append(out, r.ID)
		}
		return strings.Join(out, ",")
	}
	// No memory limit and no agent: those rules cannot be measured.
	if got := ids(EffectiveAlertRules(defaults, node)); got != "cpu,errors,latency-p95,instances-down" {
		t.Fatalf("node = %s", got)
	}
	node.Node.AgentEnabled = true
	node.Node.Recycle.MemoryLimitMB = 512
	if got := ids(EffectiveAlertRules(defaults, node)); got != "cpu,memory-limit,event-loop-lag,errors,latency-p95,instances-down" {
		t.Fatalf("node with agent and limit = %s", got)
	}
	// Overrides replace in place, off turns off, own rules follow.
	node.Alerts.Rules = []AlertRule{
		{ID: "site-memory", Metric: AlertMemory, Threshold: 800},
		{ID: "ERRORS", Metric: AlertErrorRate, Threshold: 1},
		{ID: "latency-p95", Metric: AlertLatencyP95, Disabled: true},
	}
	eff := EffectiveAlertRules(defaults, node)
	if got := ids(eff); got != "cpu,memory-limit,event-loop-lag,ERRORS,instances-down,site-memory" {
		t.Fatalf("overridden = %s", got)
	}
	if eff[3].Threshold != 1 {
		t.Fatalf("override = %+v", eff[3])
	}
	node.Alerts.Disabled = true
	if len(EffectiveAlertRules(defaults, node)) != 0 {
		t.Fatal("an opted-out site has rules")
	}

	static := &Site{Name: "files", Type: SiteStatic}
	if got := ids(EffectiveAlertRules(defaults, static)); got != "errors,latency-p95" {
		t.Fatalf("static = %s", got)
	}
	worker := &Site{Name: "queue", Type: SiteWorker, Node: &NodeConfig{}}
	if got := ids(EffectiveAlertRules(defaults, worker)); got != "cpu,instances-down" {
		t.Fatalf("worker = %s", got)
	}
}

func TestMemoryLimit(t *testing.T) {
	n := NodeConfig{}
	if n.MemoryLimitMB() != 0 {
		t.Fatal("no limit")
	}
	n.Recycle.MemoryLimitMB = 1024
	n.Limits.MemoryLimitMB = 2048
	if n.MemoryLimitMB() != 1024 {
		t.Fatalf("limit = %d", n.MemoryLimitMB())
	}
	n.Recycle.MemoryLimitMB = 0
	if n.MemoryLimitMB() != 2048 {
		t.Fatalf("limit = %d", n.MemoryLimitMB())
	}
}

// A site saved before alerts existed decodes with no alert configuration
// and stays valid.
func TestSiteWithoutAlerts(t *testing.T) {
	var s Site
	if err := json.Unmarshal([]byte(`{"name":"old","type":"redirect","redirect":{"targetUrl":"https://example.com","statusCode":301}}`), &s); err != nil {
		t.Fatal(err)
	}
	s.ApplyDefaults()
	if s.Alerts.Disabled || len(s.Alerts.Rules) != 0 {
		t.Fatalf("alerts = %+v", s.Alerts)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}
