//go:build !windows

package cli

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

func TestPreviewCommands(t *testing.T) {
	s := newServer(t)
	parent := s.createSite(&model.Site{
		Name: "shop", Type: model.SiteStatic, Static: &model.StaticConfig{Root: "."},
		Deploy: model.DeployConfig{
			// No such repository: deployments fail, the previews exist.
			Git: model.GitSource{Repo: filepath.Join(s.root, "missing"), Branch: "main"}, WebhookSecret: "x",
			Previews: model.PreviewConfig{Enabled: true, HostPattern: "{branch}.preview.test", PullRequests: true,
				Protocol: "http", IP: "127.0.0.1", Port: freePort(t)},
		},
	})
	s.run("preview", "list", "shop").expect(t, ExitOK)
	s.run("preview", "deploy", "shop", "main").expect(t, ExitError) // the production branch
	s.run("preview", "deploy", "shop", "feature/login").expect(t, ExitOK)

	var out string
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		out = s.run("preview", "list", "shop").expect(t, ExitOK).stdout
		if strings.Contains(out, "failed") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(out, "branch feature/login") || !strings.Contains(out, "http://feature-login.preview.test:") {
		t.Fatalf("preview list:\n%s", out)
	}
	// The sites list leaves previews out and says so.
	sites := s.run("site", "list").expect(t, ExitOK).stdout
	if strings.Contains(sites, "shop feature-login") || !strings.Contains(sites, "1 preview deployment(s) not shown") {
		t.Errorf("site list:\n%s", sites)
	}
	s.run("preview", "redeploy", "shop", "feature-login").expect(t, ExitOK)
	s.run("preview", "delete", "shop", "nope", "--yes").expect(t, ExitError)
	// Deleting asks first; without a terminal it needs --yes.
	s.run("preview", "delete", "shop", "feature/login").expect(t, ExitUsage)
	s.runWith(strings.NewReader("n\n"), true, "preview", "delete", "shop", "feature/login").expect(t, ExitError)
	s.run("preview", "delete", "shop", "feature/login", "--yes").expect(t, ExitOK)
	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && len(s.c.Previews(parent.ID)) > 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if n := len(s.c.Previews(parent.ID)); n != 0 {
		t.Fatalf("%d previews left", n)
	}
}
