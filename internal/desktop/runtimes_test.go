package desktop

import (
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
)

func TestRuntimeTexts(t *testing.T) {
	site := func(typ model.SiteType, n *model.NodeConfig) *model.Site {
		return &model.Site{Type: typ, Node: n}
	}
	for _, c := range []struct {
		site          *model.Site
		kind, runtime string
	}{
		{site(model.SiteNode, &model.NodeConfig{}), "Node.js application", "Node.js (server default)"},
		{site(model.SiteNode, &model.NodeConfig{Runtime: "node", NodeVersion: "22.11.0"}), "Node.js application", "Node.js 22.11.0"},
		{site(model.SiteNode, &model.NodeConfig{Runtime: "python", RuntimeVersion: "3.12"}), "Python application", "Python 3.12"},
		{site(model.SiteWorker, &model.NodeConfig{Runtime: "bun"}), "Background worker (Bun)", "Bun (server default)"},
		{site(model.SiteWorker, &model.NodeConfig{}), "Background worker", "Node.js (server default)"},
		{site(model.SiteNode, &model.NodeConfig{Runtime: "dotnet"}), ".NET application", ".NET (server default)"},
		{site(model.SiteNode, &model.NodeConfig{Runtime: "custom", Script: "php.exe"}), "Custom command", "Custom command"},
		{&model.Site{Type: model.SiteStatic}, "Static site", "–"},
	} {
		if got := SiteKindText(c.site); got != c.kind {
			t.Errorf("SiteKindText(%+v) = %q, want %q", c.site.Node, got, c.kind)
		}
		if got := RuntimeText(c.site); got != c.runtime {
			t.Errorf("RuntimeText(%+v) = %q, want %q", c.site.Node, got, c.runtime)
		}
	}
	if opts := RuntimeOptions(); len(opts) != len(model.Runtimes) || opts[0] != "Node.js" || opts[4] != ".NET" {
		t.Errorf("RuntimeOptions = %v", opts)
	}
	if l, ex := EntryHint(model.RuntimeDotnet); l == "" || ex == "" {
		t.Error("EntryHint")
	}
}
