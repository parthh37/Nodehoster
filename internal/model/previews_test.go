package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// previewParent is a git-deployed node site with previews on.
func previewParent() *Site {
	s := nodeSite()
	s.Deploy.Git = GitSource{Repo: "https://github.com/org/app.git", Branch: "main"}
	s.Deploy.WebhookSecret = "hook"
	s.Deploy.Previews = PreviewConfig{Enabled: true, HostPattern: "PR-{number}.Preview.Example.com ", PullRequests: true}
	s.ApplyDefaults()
	return s
}

func TestPreviewDefaults(t *testing.T) {
	s := previewParent()
	p := s.Deploy.Previews
	if p.HostPattern != "pr-{number}.preview.example.com" || p.Protocol != "http" || p.Port != 80 || p.MaxPreviews != DefaultMaxPreviews {
		t.Fatalf("defaults = %+v", p)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	s.Deploy.Previews.Protocol, s.Deploy.Previews.Port = "https", 0
	s.ApplyDefaults()
	if p := s.Deploy.Previews; p.Port != 443 || p.CertMode != PreviewCertAuto {
		t.Fatalf("https defaults = %+v", p)
	}
}

// TestPreviewsAbsentIsCurrentBehaviour: a site stored before previews
// existed decodes with them off and validates as before.
func TestPreviewsAbsentIsCurrentBehaviour(t *testing.T) {
	var s Site
	if err := json.Unmarshal([]byte(`{"id":"a","name":"app","type":"node","node":{"appRoot":"C:\\app","script":"s.js"},"deploy":{"keepReleases":3}}`), &s); err != nil {
		t.Fatal(err)
	}
	s.ApplyDefaults()
	if s.Deploy.Previews.Enabled || s.IsPreview() || s.Preview != nil {
		t.Fatalf("previews = %+v", s.Deploy.Previews)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(&s)
	if strings.Contains(string(raw), "previewOf") || strings.Contains(string(raw), `"preview":`) {
		t.Errorf("document = %s", raw)
	}
}

func TestPreviewValidation(t *testing.T) {
	ca := testCAPEM(t)
	cases := map[string]func(*Site){
		"deploy.previews.enabled":         func(s *Site) { s.Deploy.Git.Repo = "" },
		"deploy.git.branch":               func(s *Site) { s.Deploy.Git.Branch = "" },
		"deploy.webhookSecret":            func(s *Site) { s.Deploy.WebhookSecret = "" },
		"deploy.previews.pullRequests":    func(s *Site) { s.Deploy.Previews.PullRequests = false },
		"deploy.previews.hostPattern":     func(s *Site) { s.Deploy.Previews.HostPattern = "preview.example.com" },
		"deploy.previews.branches[1]":     func(s *Site) { s.Deploy.Previews.Branches = []string{"feature/*", "--evil"} },
		"deploy.previews.maxPreviews":     func(s *Site) { s.Deploy.Previews.MaxPreviews = 1000 },
		"deploy.previews.expireDays":      func(s *Site) { s.Deploy.Previews.ExpireDays = -1 },
		"deploy.previews.port":            func(s *Site) { s.Deploy.Previews.Port = 70000 },
		"deploy.previews.ip":              func(s *Site) { s.Deploy.Previews.IP = "nope" },
		"deploy.previews.certificateId":   func(s *Site) { s.Deploy.Previews.Protocol = "https"; s.Deploy.Previews.CertMode = PreviewCertManual },
		"deploy.previews.dnsProviderId":   func(s *Site) { s.Deploy.Previews.Protocol = "https"; s.Deploy.Previews.CertMode = PreviewCertWildcard },
		"deploy.previews.certMode":        func(s *Site) { s.Deploy.Previews.Protocol = "https"; s.Deploy.Previews.CertMode = "magic" },
		"deploy.previews.env[0].name":     func(s *Site) { s.Deploy.Previews.Env = []EnvVar{{Name: "PREVIEW_URL", Value: "x"}} },
		"deploy.previews.basicAuth.users": func(s *Site) { s.Deploy.Previews.BasicAuth.Enabled = true },
		"deploy.previews.allowIps[0]":     func(s *Site) { s.Deploy.Previews.AllowIPs = []string{"not-an-ip"} },
		"deploy.previews.protocol":        func(s *Site) { s.Deploy.Previews.Protocol = "ftp" },
		"deploy.previews.requireApproval": func(s *Site) { s.Deploy.Previews.RequireApproval = "none" },
		"deploy.previews.protocol (mTLS)": func(s *Site) {
			s.Bindings = []Binding{{Protocol: "https", Port: 443, Host: "app.example.com", CertMode: CertModeManual, CertificateID: "c",
				ClientCert: &ClientCertPolicy{Mode: ClientCertRequire, CAPEM: ca}}}
		},
		"deploy.previews.enabled (redirect)": func(s *Site) {
			s.Type, s.Node = SiteRedirect, nil
			s.Redirect = &RedirectConfig{TargetURL: "https://x.example", StatusCode: 301}
		},
	}
	for field, mutate := range cases {
		s := previewParent()
		mutate(s)
		s.ApplyDefaults()
		want, _, _ := strings.Cut(field, " ")
		err := s.Validate()
		ve, ok := err.(*ValidationError)
		if !ok || ve.Field != want {
			t.Errorf("%s: got %v", field, err)
		}
	}
	// A preview cannot have previews of its own.
	s := previewParent()
	s.PreviewOf = "parent"
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "preview") {
		t.Errorf("preview with previews: %v", err)
	}
	// Approval of forks is the default; "all" is accepted.
	s = previewParent()
	if s.Deploy.Previews.RequireApproval != PreviewApproveForks || s.Deploy.Previews.InheritSecrets {
		t.Errorf("defaults = %+v", s.Deploy.Previews)
	}
	s.Deploy.Previews.RequireApproval = " ALL "
	s.ApplyDefaults()
	if err := s.Validate(); err != nil || s.Deploy.Previews.RequireApproval != PreviewApproveAll {
		t.Errorf("requireApproval all: %v (%q)", err, s.Deploy.Previews.RequireApproval)
	}
	// Turned off, nothing else is checked.
	s = previewParent()
	s.Deploy.Previews.Enabled = false
	s.Deploy.Previews.HostPattern = "garbage"
	if err := s.Validate(); err != nil {
		t.Errorf("disabled previews: %v", err)
	}
}

// TestPreviewClientCert: http previews of a site that requires client
// certificates need basic auth or an IP allow list; https previews take
// the parent's policy.
func TestPreviewClientCert(t *testing.T) {
	mtls := func(mode string, paths ...string) *ClientCertPolicy {
		return &ClientCertPolicy{Mode: mode, CAPEM: "ca", AllowedSubjects: []string{"qa"}, RequirePaths: paths}
	}
	s := previewParent()
	s.Bindings = []Binding{
		{Protocol: "http", Port: 80, Host: "app.example.com"},
		{Protocol: "https", Port: 8443, Host: "app.example.com", ClientCert: mtls(ClientCertAccept)},
		{Protocol: "https", Port: 443, IP: "10.0.0.1", Host: "app.example.com", ClientCert: mtls(ClientCertRequire)},
	}
	if !s.RequiresClientCert() {
		t.Error("require not counted")
	}
	// accept without required paths lets everyone in: no lock needed.
	accept := *s
	accept.Bindings = s.Bindings[:2]
	if accept.RequiresClientCert() {
		t.Error("accept counted as required")
	}
	s.Bindings[1].ClientCert.RequirePaths = []string{"/admin"}
	if !accept.RequiresClientCert() {
		t.Error("accept with required paths not counted")
	}
	// A slot's binding is not production's.
	slot := Site{Bindings: []Binding{{Protocol: "https", Port: 443, Slot: "staging", ClientCert: mtls(ClientCertRequire)}}}
	if slot.RequiresClientCert() {
		t.Error("a slot's binding counted")
	}
	if err := s.validatePreviews(); err == nil {
		t.Error("open http previews of an mTLS site accepted")
	}
	for _, lock := range []func(p *PreviewConfig){
		func(p *PreviewConfig) { p.AllowIPs = []string{"10.0.0.0/8"} },
		func(p *PreviewConfig) {
			p.BasicAuth = BasicAuthConfig{Enabled: true, Users: []BasicAuthUser{{Username: "qa"}}}
		},
	} {
		c := *s
		c.Deploy.Previews = s.Deploy.Previews
		lock(&c.Deploy.Previews)
		if err := c.validatePreviews(); err != nil {
			t.Errorf("locked http previews: %v", err)
		}
	}
	// The policy of the binding on the same address and port, else the first.
	if p := s.PreviewClientCert("10.0.0.1", 443); p == nil || p.Mode != ClientCertRequire {
		t.Errorf("same address: %+v", p)
	}
	p := s.PreviewClientCert("", 443)
	if p == nil || p.Mode != ClientCertAccept || p.RequirePaths[0] != "/admin" {
		t.Fatalf("first: %+v", p)
	}
	p.AllowedSubjects[0] = "changed"
	if s.Bindings[1].ClientCert.AllowedSubjects[0] != "qa" {
		t.Error("the copy shares the parent's lists")
	}
	s.Bindings = s.Bindings[:1]
	if p := s.PreviewClientCert("", 443); p != nil {
		t.Errorf("no policy: %+v", p)
	}
}

func TestValidateHostPattern(t *testing.T) {
	for _, ok := range []string{"pr-{number}.preview.example.com", "{branch}.preview.example.com", "pr-{number}-{branch}.x.io", "{branch}.localhost"} {
		if err := ValidateHostPattern(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "{branch}", "preview.example.com", "pr-{number}.{branch}.example.com", "*.example.com",
		"pr_{number}.example.com", "{number}.bad_host.com", strings.Repeat("a", 41) + "{number}.example.com"} {
		if err := ValidateHostPattern(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
