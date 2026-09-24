package core

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/preview"
	"github.com/parthh37/nodehoster/internal/secrets"
	"golang.org/x/crypto/bcrypt"
)

// gitRepo is a local repository with a main and a fix/login branch, each
// with an index.html saying which it is. Tests using it need git.
type gitRepo struct {
	t   *testing.T
	dir string
}

func newGitRepo(t *testing.T) *gitRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	r := &gitRepo{t: t, dir: t.TempDir()}
	r.git("init", "-q", "-b", "main")
	r.commit("index.html", "production")
	r.git("checkout", "-q", "-b", "fix/login")
	r.commit("index.html", "pr content")
	r.git("checkout", "-q", "main")
	return r
}

func (r *gitRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = r.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *gitRepo) commit(file, content string) string {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.dir, file), []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
	r.git("add", file)
	r.git("commit", "-q", "-m", "set "+file+" to "+content)
	return r.git("rev-parse", "HEAD")
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// previewParentSite creates a static site deployed from repo with pull
// request previews on 127.0.0.1:port.
func previewParentSite(t *testing.T, c *Core, repo string, port int, mutate func(*model.Site)) *model.Site {
	t.Helper()
	s := &model.Site{
		Name: "shop", Type: model.SiteStatic, Static: &model.StaticConfig{Root: `C:\sites\shop`},
		Deploy: model.DeployConfig{
			Git: model.GitSource{Repo: repo, Branch: "main"}, WebhookSecret: "hook", KeepReleases: 5,
			SharedPaths: []string{"uploads"},
			Previews: model.PreviewConfig{
				Enabled: true, HostPattern: "pr-{number}.preview.test", PullRequests: true, Branches: []string{"feature/*"},
				Protocol: "http", IP: "127.0.0.1", Port: port, MaxPreviews: 5, ExpireDays: 7,
			},
		},
	}
	if mutate != nil {
		mutate(s)
	}
	created, err := c.CreateSite(context.Background(), s)
	if err != nil {
		t.Fatalf("CreateSite: %v", err)
	}
	return created
}

func prEvent(action string, number int, branch string) *preview.Event {
	ev := &preview.Event{Kind: model.PreviewPR, Number: number, Branch: branch, Ref: "refs/heads/" + branch,
		Action: action, Reason: action, Title: "Fix login", Author: "dev"}
	return ev
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// idle waits until no preview operation is queued or running.
func (c *Core) idleForTest(t *testing.T) {
	t.Helper()
	waitFor(t, "the preview workers to finish", func() bool {
		c.previews.mu.Lock()
		defer c.previews.mu.Unlock()
		return len(c.previews.active) == 0
	})
}

func eventTypes(t *testing.T, c *Core, siteID string) []string {
	t.Helper()
	list, err := c.Store.ListEvents(context.Background(), siteID, 200)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range list {
		out = append(out, e.Type)
	}
	return out
}

func get(t *testing.T, port int, host string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/", nil)
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", host, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// TestPreviewLifecycle follows a pull request: opened (a preview is
// created, deployed and started), pushed to (redeployed), closed
// (deleted with its files).
func TestPreviewLifecycle(t *testing.T) {
	repo := newGitRepo(t)
	c := testCore(t)
	port := freeTCPPort(t)
	parent := previewParentSite(t, c, repo.dir, port, nil)

	d, err := c.PreviewWebhook(parent, prEvent(preview.ActionDeploy, 42, "fix/login"))
	if err != nil || !d.Handled || d.Action != preview.ActionDeploy || d.Key != "pr:42" {
		t.Fatalf("decision = %+v, %v", d, err)
	}
	c.idleForTest(t)
	list := c.Previews(parent.ID)
	if len(list) != 1 {
		t.Fatalf("previews = %d", len(list))
	}
	p := list[0]
	v := c.PreviewView(context.Background(), p)
	if v.State != model.PreviewReady {
		dl, _ := c.Deploy.Log(p.ID, v.LastDeployment.ID)
		t.Fatalf("state = %s (%+v)\n%s", v.State, v.LastDeployment, dl)
	}
	if p.Name != "shop pr-42" || p.PreviewOf != parent.ID || !p.AutoStart || !p.Preview.Ready {
		t.Errorf("preview = %+v", p)
	}
	if len(p.Bindings) != 1 || p.Bindings[0].Host != "pr-42.preview.test" || p.Bindings[0].Port != port {
		t.Errorf("bindings = %+v", p.Bindings)
	}
	if p.Preview.URL != "http://pr-42.preview.test:"+strconv.Itoa(port) || p.Preview.Title != "Fix login" {
		t.Errorf("info = %+v", p.Preview)
	}
	if p.Deploy.WebhookSecret != "" || p.Deploy.KeepReleases != model.PreviewKeepReleases || p.Deploy.Previews.Enabled || p.Deploy.Git.Branch != "fix/login" {
		t.Errorf("deploy = %+v", p.Deploy)
	}
	if p.Static.Root != "." {
		t.Errorf("static root = %q: a preview must never serve the parent's folder", p.Static.Root)
	}
	if !c.IsRunning(p) {
		t.Fatal("the preview was not started")
	}
	if got := get(t, port, "pr-42.preview.test"); !strings.Contains(got, "pr content") {
		t.Fatalf("preview serves %q", got)
	}
	// Its shared folder is its own, not the parent's.
	if _, err := os.Stat(model.SharedDir(c.Paths.Sites, p.ID)); err != nil {
		t.Errorf("the preview has no shared folder of its own: %v", err)
	}
	if _, err := os.Stat(model.SharedDir(c.Paths.Sites, parent.ID)); err == nil {
		t.Error("the preview wrote into the parent's shared folder")
	}

	// A push to the pull request redeploys it.
	repo.git("checkout", "-q", "fix/login")
	head := repo.commit("index.html", "pr v2")
	repo.git("checkout", "-q", "main")
	ev := prEvent(preview.ActionDeploy, 42, "fix/login")
	ev.Reason, ev.Commit = "synchronize", head
	if _, err := c.PreviewWebhook(parent, ev); err != nil {
		t.Fatal(err)
	}
	c.idleForTest(t)
	p, _ = c.Site(p.ID)
	if p.Preview.Commit != head {
		t.Errorf("commit = %s, want %s", p.Preview.Commit, head)
	}
	waitFor(t, "the new release to be served", func() bool { return strings.Contains(get(t, port, "pr-42.preview.test"), "pr v2") })
	types := eventTypes(t, c, parent.ID)
	if !slices.Contains(types, events.PreviewCreated) || !slices.Contains(types, events.PreviewUpdated) {
		t.Errorf("events = %v", types)
	}

	// Closing it deletes the site, its releases and logs.
	if _, err := c.PreviewWebhook(parent, prEvent(preview.ActionDelete, 42, "fix/login")); err != nil {
		t.Fatal(err)
	}
	c.idleForTest(t)
	if _, err := c.Site(p.ID); err == nil {
		t.Fatal("the preview site still exists")
	}
	if _, err := os.Stat(filepath.Join(c.Paths.Sites, p.ID)); !os.IsNotExist(err) {
		t.Errorf("the preview's files were left: %v", err)
	}
	if !slices.Contains(eventTypes(t, c, parent.ID), events.PreviewDeleted) {
		t.Error("no preview.deleted event")
	}
	// The parent is untouched.
	if cur, _ := c.Site(parent.ID); cur.ActiveRelease != "" || len(cur.Bindings) != 0 {
		t.Errorf("the parent changed: %+v", cur)
	}
}

func TestPreviewForkPullRequestRefused(t *testing.T) {
	c := testCore(t)
	parent := previewParentSite(t, c, "https://git.example.invalid/org/app.git", freeTCPPort(t), nil)
	ev := prEvent(preview.ActionDeploy, 9, "evil")
	ev.Fork, ev.Ref = true, "refs/pull/9/head"
	d, err := c.PreviewWebhook(parent, ev)
	if err != nil || d.Action != preview.ActionIgnore || !strings.Contains(d.Reason, "fork") {
		t.Fatalf("decision = %+v, %v", d, err)
	}
	c.idleForTest(t)
	if n := len(c.Previews(parent.ID)); n != 0 {
		t.Fatalf("a fork's pull request got %d previews", n)
	}
}

// TestPreviewEvictsLeastRecentlyPushed: beyond the maximum, a new preview
// evicts the one pushed to least recently. The deployments fail (the
// repository does not exist); the sites exist all the same.
func TestPreviewEvictsLeastRecentlyPushed(t *testing.T) {
	c := testCore(t)
	parent := previewParentSite(t, c, filepath.Join(t.TempDir(), "missing"), freeTCPPort(t), func(s *model.Site) {
		s.Deploy.Previews.MaxPreviews = 2
	})
	for _, n := range []int{1, 2} {
		c.PreviewWebhook(parent, prEvent(preview.ActionDeploy, n, "b"+strconv.Itoa(n)))
		c.idleForTest(t)
	}
	// #1 is pushed to again: #2 is now the least recent.
	c.PreviewWebhook(parent, prEvent(preview.ActionDeploy, 1, "b1"))
	c.idleForTest(t)
	c.PreviewWebhook(parent, prEvent(preview.ActionDeploy, 3, "b3"))
	c.idleForTest(t)
	var keys []string
	for _, p := range c.Previews(parent.ID) {
		keys = append(keys, p.Preview.Key)
	}
	slices.Sort(keys)
	if strings.Join(keys, ",") != "pr:1,pr:3" {
		t.Fatalf("previews = %v, want pr:1 and pr:3", keys)
	}
	if !slices.Contains(eventTypes(t, c, parent.ID), events.PreviewFailed) {
		t.Error("the failed deployments raised no preview.failed")
	}
	v := c.PreviewView(context.Background(), c.Previews(parent.ID)[0])
	if v.State != model.PreviewFailed {
		t.Errorf("state = %s", v.State)
	}
}

func TestPreviewExpiryAndParentDeletion(t *testing.T) {
	c := testCore(t)
	parent := previewParentSite(t, c, filepath.Join(t.TempDir(), "missing"), freeTCPPort(t), nil)
	c.PreviewWebhook(parent, prEvent(preview.ActionDeploy, 1, "a"))
	c.PreviewWebhook(parent, prEvent(preview.ActionDeploy, 2, "b"))
	c.idleForTest(t)
	if n := len(c.Previews(parent.ID)); n != 2 {
		t.Fatalf("previews = %d", n)
	}
	c.expirePreviews(time.Now().Add(6 * 24 * time.Hour))
	c.idleForTest(t)
	if n := len(c.Previews(parent.ID)); n != 2 {
		t.Fatalf("expired too early: %d left", n)
	}
	c.expirePreviews(time.Now().Add(8 * 24 * time.Hour))
	c.idleForTest(t)
	if n := len(c.Previews(parent.ID)); n != 0 {
		t.Fatalf("%d previews survived expiry", n)
	}

	c.PreviewWebhook(parent, prEvent(preview.ActionDeploy, 3, "c"))
	c.idleForTest(t)
	p := c.Previews(parent.ID)[0]
	if err := c.DeleteSite(context.Background(), parent.ID, false); err != nil {
		t.Fatal(err)
	}
	c.idleForTest(t)
	if _, err := c.Site(p.ID); err == nil {
		t.Fatal("deleting the parent left its preview")
	}
}

func TestPreviewConfigDerived(t *testing.T) {
	c := testCore(t)
	sealed, _ := c.Box.Seal("prod-db")
	parent := &model.Site{
		ID: "parent", Name: "api", Type: model.SiteNode, AutoStart: true,
		Bindings: []model.Binding{{Protocol: "http", Port: 80, Host: "api.example.com"}},
		Node: &model.NodeConfig{AppRoot: `D:\apps\api`, Script: "server.js", Instances: 4, PortMode: "fixed", FixedPort: 3000,
			Env:          []model.EnvVar{{Name: "DATABASE_URL", Value: sealed, Secret: true}, {Name: "LOG", Value: "info"}},
			LoadBalancer: model.LoadBalancerConfig{Enabled: true, Servers: []model.Upstream{{URL: "http://10.0.0.2"}}}},
		Tasks: []model.ScheduledTask{{ID: "t", Name: "cleanup", Schedule: "@daily", Script: "x.js"}},
		Routing: model.RoutingConfig{HTTPSRedirect: true, HSTS: model.HSTSConfig{Enabled: true},
			Maintenance: model.MaintenanceConfig{Enabled: true}, IP: model.IPRestrictions{Allow: []string{"0.0.0.0/0"}}},
		Deploy: model.DeployConfig{Git: model.GitSource{Repo: "https://github.com/org/api.git", Branch: "main"}, WebhookSecret: "x", KeepReleases: 10,
			Previews: model.PreviewConfig{Enabled: true, HostPattern: "{branch}.preview.example.com", Protocol: "https", Port: 443, CertMode: "auto",
				Env:       []model.EnvVar{{Name: "database_url", Value: "sealed-preview-db", Secret: true}},
				BasicAuth: model.BasicAuthConfig{Enabled: true, Realm: "Preview", Users: []model.BasicAuthUser{{Username: "qa", PasswordHash: "$2a$hash"}}},
				AllowIPs:  []string{"10.0.0.0/8"}}},
	}
	info := model.PreviewInfo{Key: "branch:feature/x", Kind: model.PreviewBranch, Branch: "feature/x", Host: "feature-x.preview.example.com", URL: "https://feature-x.preview.example.com"}
	s := derivePreview(parent, nil, info, "")
	n := s.Node
	if n.Instances != 1 || n.PortMode != "auto" || n.FixedPort != 0 || n.LoadBalancer.Enabled || n.AppRoot != "." {
		t.Errorf("node = %+v", n)
	}
	env := map[string]model.EnvVar{}
	for _, e := range n.Env {
		env[e.Name] = e
	}
	if _, dup := env["DATABASE_URL"]; dup || env["database_url"].Value != "sealed-preview-db" || !env["database_url"].Secret {
		t.Errorf("override did not replace the parent's variable: %+v", n.Env)
	}
	if env["LOG"].Value != "info" || env["PREVIEW"].Value != "1" || env["PREVIEW_BRANCH"].Value != "feature/x" ||
		env["PREVIEW_PR"].Value != "" || env["PREVIEW_URL"].Value != info.URL {
		t.Errorf("env = %+v", n.Env)
	}
	if len(s.Tasks) != 0 || s.AutoStart || s.ID != "" || s.PreviewOf != "parent" {
		t.Errorf("tasks=%d autoStart=%v id=%q previewOf=%q", len(s.Tasks), s.AutoStart, s.ID, s.PreviewOf)
	}
	if b := s.Bindings; len(b) != 1 || b[0].Host != info.Host || b[0].CertMode != model.CertModeAuto || b[0].Protocol != "https" {
		t.Errorf("bindings = %+v", b)
	}
	r := s.Routing
	if r.Maintenance.Enabled || r.HTTPSRedirect || !r.BasicAuth.Enabled || r.BasicAuth.Users[0].Username != "qa" || r.IP.Allow[0] != "10.0.0.0/8" {
		t.Errorf("routing = %+v", r)
	}
	// The parent is not modified.
	if parent.Node.Instances != 4 || len(parent.Node.Env) != 2 || parent.Routing.IP.Allow[0] != "0.0.0.0/0" || len(parent.Tasks) != 1 {
		t.Error("derivePreview modified the parent")
	}
	// With a certificate, and for an existing preview: its identity stays.
	cur := &model.Site{ID: "p1", Name: "api feature-x", AutoStart: true, ActiveRelease: "r1", Bindings: []model.Binding{{ID: "b1"}}}
	s = derivePreview(parent, cur, info, "cert-1")
	if s.ID != "p1" || s.Name != "api feature-x" || !s.AutoStart || s.ActiveRelease != "r1" || s.Bindings[0].ID != "b1" ||
		s.Bindings[0].CertMode != model.CertModeManual || s.Bindings[0].CertificateID != "cert-1" {
		t.Errorf("refreshed = %+v", s)
	}
}

// TestPreviewSecretsSealedAndMasked: preview variables and the status
// token are encrypted at rest and masked in the API; masked values sent
// back keep them; basic auth passwords are hashed.
func TestPreviewSecretsSealedAndMasked(t *testing.T) {
	c := testCore(t)
	parent := previewParentSite(t, c, "https://github.com/org/app.git", freeTCPPort(t), func(s *model.Site) {
		p := &s.Deploy.Previews
		p.Env = []model.EnvVar{{Name: "DATABASE_URL", Value: "postgres://preview", Secret: true}, {Name: "FLAG", Value: "on"}}
		p.StatusToken = "ghp_statustoken"
		p.ReportStatus = true
		p.BasicAuth = model.BasicAuthConfig{Enabled: true, Users: []model.BasicAuthUser{{Username: "qa", Password: "letmein"}}}
	})
	p := parent.Deploy.Previews
	if !secrets.IsSealed(p.Env[0].Value) || !secrets.IsSealed(p.StatusToken) || p.Env[1].Value != "on" {
		t.Fatalf("stored = %+v", p)
	}
	if bcrypt.CompareHashAndPassword([]byte(p.BasicAuth.Users[0].PasswordHash), []byte("letmein")) != nil || p.BasicAuth.Users[0].Password != "" {
		t.Fatalf("basic auth user = %+v", p.BasicAuth.Users[0])
	}
	m := Masked(parent)
	raw, _ := json.Marshal(m)
	for _, leak := range []string{"postgres://preview", "ghp_statustoken", p.Env[0].Value, p.StatusToken, p.BasicAuth.Users[0].PasswordHash} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("the API view contains %q", leak)
		}
	}
	mp := m.Deploy.Previews
	if mp.Env[0].Value != secrets.Mask || mp.StatusToken != secrets.Mask {
		t.Errorf("masked = %+v", mp)
	}
	// Saving the masked view back keeps every secret.
	updated, err := c.UpdateSite(context.Background(), parent.ID, m)
	if err != nil {
		t.Fatal(err)
	}
	up := updated.Deploy.Previews
	if up.Env[0].Value != p.Env[0].Value || up.StatusToken != p.StatusToken || up.BasicAuth.Users[0].PasswordHash != p.BasicAuth.Users[0].PasswordHash {
		t.Errorf("masked values were not kept: %+v", up)
	}
	// A hash sent by a client is never trusted.
	m = Masked(updated)
	m.Deploy.Previews.BasicAuth.Users = append(m.Deploy.Previews.BasicAuth.Users, model.BasicAuthUser{Username: "x", PasswordHash: "$2a$10$forged"})
	if _, err := c.UpdateSite(context.Background(), parent.ID, m); err == nil {
		t.Error("a client-supplied hash was accepted")
	}
}

func TestPreviewFieldsAreServerOwned(t *testing.T) {
	c := testCore(t)
	// A client cannot create a preview...
	s, err := c.CreateSite(context.Background(), &model.Site{Name: "r", Type: model.SiteRedirect, PreviewOf: "x", Preview: &model.PreviewInfo{Key: "pr:1"},
		Redirect: &model.RedirectConfig{TargetURL: "https://example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if s.IsPreview() || s.Preview != nil {
		t.Fatalf("created a preview through CreateSite: %+v", s)
	}
	// ...nor turn a site into one, or a preview into a site.
	in := clone(s)
	in.PreviewOf, in.Preview = "x", &model.PreviewInfo{Key: "pr:1"}
	up, err := c.UpdateSite(context.Background(), s.ID, in)
	if err != nil || up.IsPreview() {
		t.Fatalf("UpdateSite made a preview: %+v, %v", up, err)
	}
	c.sitesMu.Lock()
	marked := clone(up)
	marked.PreviewOf, marked.Preview = "parent", &model.PreviewInfo{Key: "pr:7"}
	if _, err := c.updateSite(context.Background(), s.ID, marked); err != nil {
		c.sitesMu.Unlock()
		t.Fatal(err)
	}
	c.sitesMu.Unlock()
	in = clone(marked)
	in.PreviewOf, in.Preview = "", nil
	up, err = c.UpdateSite(context.Background(), s.ID, in)
	if err != nil || up.PreviewOf != "parent" || up.Preview == nil || up.Preview.Key != "pr:7" {
		t.Fatalf("UpdateSite cleared the preview marks: %+v, %v", up, err)
	}
}

func TestDropPreviewCertificates(t *testing.T) {
	c := testCore(t)
	ctx := context.Background()
	now := time.Now()
	for _, cert := range []*model.Certificate{
		{ID: "mine", Name: "pr-1.preview.test", Managed: true, Domains: []string{"pr-1.preview.test"}, Status: "valid", CreatedAt: now},
		{ID: "shared", Name: "www", Managed: true, Domains: []string{"www.example.com"}, Status: "valid", CreatedAt: now},
		{ID: "wildcard", Name: "*.preview.test", Domains: []string{"*.preview.test"}, Status: "valid", CreatedAt: now},
	} {
		if err := c.Store.PutCertificate(ctx, cert); err != nil {
			t.Fatal(err)
		}
	}
	// Another site still has a binding for www.example.com.
	if _, err := c.CreateSite(ctx, &model.Site{Name: "www", Type: model.SiteRedirect, Redirect: &model.RedirectConfig{TargetURL: "https://example.org"},
		Bindings: []model.Binding{{Protocol: "http", Port: freeTCPPort(t), Host: "www.example.com"}}}); err != nil {
		t.Fatal(err)
	}
	p := &model.Site{ID: "p", Name: "shop pr-1", PreviewOf: "shop", Bindings: []model.Binding{
		{Protocol: "https", Host: "pr-1.preview.test", CertMode: model.CertModeAuto},
		{Protocol: "https", Host: "www.example.com", CertMode: model.CertModeAuto},
		{Protocol: "https", Host: "x.preview.test", CertMode: model.CertModeManual, CertificateID: "wildcard"},
	}}
	c.dropPreviewCertificates(ctx, p)
	list, _ := c.Store.ListCertificates(ctx)
	var left []string
	for _, cert := range list {
		left = append(left, cert.ID)
	}
	slices.Sort(left)
	if strings.Join(left, ",") != "shared,wildcard" {
		t.Errorf("certificates left = %v", left)
	}
}

func TestEnsureWildcardReusesOne(t *testing.T) {
	c := testCore(t)
	ctx := context.Background()
	c.Store.PutCertificate(ctx, &model.Certificate{ID: "old", Domains: []string{"*.preview.test"}, Status: "error", CreatedAt: time.Now()})
	c.Store.PutCertificate(ctx, &model.Certificate{ID: "good", Domains: []string{"example.com", "*.preview.test"}, Status: "valid", CreatedAt: time.Now()})
	id, err := c.ensureWildcard(ctx, "preview.test", "dns")
	if err != nil || id != "good" {
		t.Fatalf("ensureWildcard = %q, %v", id, err)
	}
}

// TestPreviewReportsCommitStatus: with status reporting on, the git host
// hears that the preview is deploying and then that it failed (the
// repository is the fake host, which is not a git server), with the status
// token and nothing else.
func TestPreviewReportsCommitStatus(t *testing.T) {
	var mu sync.Mutex
	var states []string
	var auth []string
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/api/v1/repos/org/app/statuses/") {
			http.NotFound(w, r) // git's fetch
			return
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		states = append(states, body["state"])
		auth = append(auth, r.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	defer host.Close()
	c := testCore(t)
	parent := previewParentSite(t, c, host.URL+"/org/app.git", freeTCPPort(t), func(s *model.Site) {
		s.Deploy.Previews.ReportStatus = true
		s.Deploy.Previews.StatusToken = "gitea-token"
	})
	ev := prEvent(preview.ActionDeploy, 5, "topic")
	ev.Provider, ev.Commit = preview.Gitea, "0123456789abcdef0123456789abcdef01234567"
	if _, err := c.PreviewWebhook(parent, ev); err != nil {
		t.Fatal(err)
	}
	c.idleForTest(t)
	mu.Lock()
	defer mu.Unlock()
	if len(states) != 2 || states[0] != preview.StatePending || (states[1] != preview.StateFailure && states[1] != preview.StateError) {
		t.Fatalf("states = %v", states)
	}
	for _, a := range auth {
		if a != "token gitea-token" {
			t.Errorf("Authorization = %q", a)
		}
	}
}

func TestDeployBranchPreviewChecks(t *testing.T) {
	c := testCore(t)
	parent := previewParentSite(t, c, "https://github.com/org/app.git", freeTCPPort(t), nil)
	for _, branch := range []string{"main", "--x", "a..b", ""} {
		if _, err := c.DeployBranchPreview(parent, branch, "alice"); err == nil {
			t.Errorf("DeployBranchPreview(%q) accepted", branch)
		}
	}
	d, err := c.DeployBranchPreview(parent, "refs/heads/develop", "alice")
	if err != nil || d.Key != "branch:develop" {
		t.Fatalf("decision = %+v, %v", d, err)
	}
	c.idleForTest(t)
	list := c.Previews(parent.ID)
	if len(list) != 1 || list[0].Preview.Branch != "develop" || list[0].Bindings[0].Host != "develop.preview.test" {
		t.Fatalf("previews = %+v", list)
	}
}

// TestPreviewQueueCoalesces: requests for a preview arriving while its
// worker is busy collapse into the latest one.
func TestPreviewQueueCoalesces(t *testing.T) {
	c := testCore(t)
	st := &c.previews
	// Start the queue with an operation on a missing parent (a no-op).
	c.queuePreview(&previewOp{parentID: "none", key: "pr:1", ev: &preview.Event{}})
	c.idleForTest(t)
	st.mu.Lock()
	st.active[opKey("p", "k")] = model.PreviewDeploying // a worker is (pretend) running
	st.mu.Unlock()
	for i := 0; i < 5; i++ {
		c.queuePreview(&previewOp{parentID: "p", key: "k", reason: strconv.Itoa(i)})
	}
	c.queuePreview(&previewOp{parentID: "p", key: "k", del: true, reason: "last"})
	st.mu.Lock()
	op := st.pending[opKey("p", "k")]
	n := len(st.pending)
	st.mu.Unlock()
	if n != 1 || !op.del || op.reason != "last" {
		t.Fatalf("pending = %d, op = %+v", n, op)
	}
	if got := c.queued("p", "k"); got != model.PreviewDeleting {
		t.Errorf("queued = %q", got)
	}
	st.mu.Lock()
	delete(st.active, opKey("p", "k"))
	delete(st.pending, opKey("p", "k"))
	st.mu.Unlock()
	// After Shutdown, nothing is queued.
	c.closePreviews()
	if err := c.queuePreview(&previewOp{parentID: "p", key: "k"}); err == nil {
		t.Error("queued after shutdown")
	}
}

// TestPreviewStatusTokenFromStore: with the git token in a secret store,
// the commit status is reported with it.
func TestPreviewStatusTokenFromStore(t *testing.T) {
	vault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") == "tok" && r.URL.Path == "/v1/secret/data/git" {
			w.Write([]byte(`{"data":{"data":{"TOKEN":"store-token"}}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"errors":[]}`))
	}))
	defer vault.Close()
	var mu sync.Mutex
	var auth []string
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/statuses/") {
			http.NotFound(w, r)
			return
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		auth = append(auth, r.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	defer host.Close()
	c := testCore(t)
	set := c.Settings()
	set.SecretStores = []model.SecretStore{{Name: "vault", Type: model.SecretStoreVault, URL: vault.URL, Vault: &model.VaultStore{Token: "tok"}}}
	if _, err := c.UpdateSettings(context.Background(), set); err != nil {
		t.Fatal(err)
	}
	parent := previewParentSite(t, c, host.URL+"/org/app.git", freeTCPPort(t), func(s *model.Site) {
		s.Deploy.Git.TokenFrom = &model.SecretRef{Store: "vault", Ref: "git#TOKEN"}
		s.Deploy.Previews.ReportStatus = true
	})
	ev := prEvent(preview.ActionDeploy, 5, "topic")
	ev.Provider, ev.Commit = preview.Gitea, "0123456789abcdef0123456789abcdef01234567"
	c.PreviewWebhook(parent, ev)
	c.idleForTest(t)
	mu.Lock()
	defer mu.Unlock()
	if len(auth) != 2 {
		t.Fatalf("%d statuses reported", len(auth))
	}
	for _, a := range auth {
		if a != "token store-token" {
			t.Errorf("Authorization = %q", a)
		}
	}
}

