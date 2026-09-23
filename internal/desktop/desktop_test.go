package desktop

import (
	"strings"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/localapi"
	"github.com/parthh37/nodehoster/internal/model"
)

func TestBindingText(t *testing.T) {
	for _, tc := range []struct {
		b    model.Binding
		text string
		url  string
	}{
		{model.Binding{Protocol: "http", Port: 80}, "http *:80", "http://localhost/"},
		{model.Binding{Protocol: "https", IP: "*", Port: 443, Host: "shop.example.com"}, "https *:443 shop.example.com", "https://shop.example.com/"},
		{model.Binding{Protocol: "http", IP: "10.0.0.5", Port: 8080}, "http 10.0.0.5:8080", "http://10.0.0.5:8080/"},
		{model.Binding{Protocol: "https", IP: "::1", Port: 8443}, "https [::1]:8443", "https://[::1]:8443/"},
		{model.Binding{Protocol: "https", Port: 443, Host: "*.example.com"}, "https *:443 *.example.com", "https://localhost/"},
	} {
		if got := BindingText(tc.b); got != tc.text {
			t.Errorf("BindingText(%+v) = %q, want %q", tc.b, got, tc.text)
		}
		if got := BrowseURL(tc.b); got != tc.url {
			t.Errorf("BrowseURL(%+v) = %q, want %q", tc.b, got, tc.url)
		}
	}
}

func TestBytes(t *testing.T) {
	for n, want := range map[uint64]string{0: "0 B", 1023: "1023 B", 1536: "1.5 KB", 612 << 20: "612 MB", 3 << 30: "3.0 GB"} {
		if got := Bytes(n); got != want {
			t.Errorf("Bytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestUptime(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for d, want := range map[time.Duration]string{
		45 * time.Second: "45s", 125 * time.Second: "2m 5s", 125 * time.Minute: "2h 5m", 76 * time.Hour: "3d 4h",
	} {
		if got := Uptime(now.Add(-d), now); got != want {
			t.Errorf("Uptime(%v) = %q, want %q", d, got, want)
		}
	}
	if got := Uptime(time.Time{}, now); got != "–" {
		t.Errorf("Uptime(zero) = %q", got)
	}
}

func TestOverallServiceStates(t *testing.T) {
	for svc, want := range map[string]Level{
		"not installed": LevelNotInstalled, "stopped": LevelDown, "stopping": LevelDown, "starting": LevelWarning,
	} {
		if got := Overall(svc, nil).Level; got != want {
			t.Errorf("Overall(%q) = %v, want %v", svc, got, want)
		}
	}
	if h := Overall("running", nil); h.Level != LevelDown || !strings.Contains(h.Summary, "not responding") {
		t.Errorf("running without a status answer = %+v", h)
	}
}

func TestOverallSites(t *testing.T) {
	sum := &localapi.Summary{Sites: []localapi.SiteSummary{
		{Name: "a", State: model.StateRunning, AutoStart: true},
		{Name: "b", State: model.StateRunning, AutoStart: true},
	}}
	if h := Overall("running", sum); h.Level != LevelOK || h.Summary != "Running · 2 of 2 sites running" {
		t.Errorf("healthy = %+v", h)
	}

	// Whatever else counts, a site that rapid-fail protection stopped does.
	sum.Sites = append(sum.Sites, localapi.SiteSummary{Name: "c", State: model.StateFailed, AutoStart: true})
	h := Overall("running", sum)
	if h.Level != LevelWarning || len(h.Attention) != 1 || h.Attention[0].Name != "c" {
		t.Errorf("failed site = %+v", h)
	}
	if !strings.HasSuffix(h.Summary, "1 needs attention") {
		t.Errorf("summary = %q", h.Summary)
	}

	sum.Sites = sum.Sites[:2]
	sum.AdminError = "listen tcp :8484: address in use"
	if h := Overall("running", sum); h.Level != LevelWarning || !strings.Contains(h.Summary, "web console") {
		t.Errorf("console down = %+v", h)
	}
}

func TestConsoleURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://0.0.0.0:8484":   "https://localhost:8484/",
		"http://127.0.0.1:18484": "http://127.0.0.1:18484/",
		"https://[::]:8484":      "https://localhost:8484/",
		"https://web01:443":      "https://web01:443/",
		"":                       "",
	} {
		if got := ConsoleURL(in); got != want {
			t.Errorf("ConsoleURL(%q) = %q, want %q", in, got, want)
		}
	}
}
