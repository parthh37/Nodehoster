package core

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/preview"
)

// onBranch commits content to index.html on a branch of the repository,
// as a push to it would.
func (r *gitRepo) onBranch(branch, content string) string {
	r.t.Helper()
	r.git("checkout", "-q", branch)
	defer r.git("checkout", "-q", "main")
	return r.commit("index.html", content)
}

func forkEvent(number int, branch, commit string) *preview.Event {
	ev := prEvent(preview.ActionDeploy, number, branch)
	ev.Fork, ev.Commit = true, commit
	return ev
}

func eventMessages(t *testing.T, c *Core, siteID, typ string) []string {
	t.Helper()
	list, err := c.Store.ListEvents(context.Background(), siteID, 200)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range list {
		if e.Type == typ {
			out = append(out, e.Message)
		}
	}
	return out
}

// TestPreviewForkNeedsApproval follows a fork's pull request with forks
// allowed: nothing is fetched until an operator approves the head commit,
// each new push waits for approval again while the approved build keeps
// serving, and a ref that moved after the approval is not built.
func TestPreviewForkNeedsApproval(t *testing.T) {
	repo := newGitRepo(t)
	c := testCore(t)
	ctx := context.Background()
	port := freeTCPPort(t)
	parent := previewParentSite(t, c, repo.dir, port, func(s *model.Site) { s.Deploy.Previews.AllowForks = true })
	head := repo.git("rev-parse", "fix/login")

	d, err := c.PreviewWebhook(parent, forkEvent(9, "fix/login", head))
	if err != nil || d.Action != preview.ActionDeploy {
		t.Fatalf("decision = %+v, %v", d, err)
	}
	c.idleForTest(t)
	list := c.Previews(parent.ID)
	if len(list) != 1 {
		t.Fatalf("previews = %d", len(list))
	}
	p := list[0]
	v := c.PreviewView(ctx, p)
	if v.State != model.PreviewAwaitingApproval || v.LastDeployment != nil || !p.Preview.AwaitingApproval || p.Preview.Commit != head {
		t.Fatalf("held preview = %+v, %+v", v, p.Preview)
	}
	if len(p.Bindings) != 0 || c.IsRunning(p) {
		t.Errorf("a preview never approved has bindings %+v or runs", p.Bindings)
	}
	if msgs := eventMessages(t, c, parent.ID, events.PreviewApproval); len(msgs) != 1 || !strings.Contains(msgs[0], "from a fork") || !strings.Contains(msgs[0], head[:7]) {
		t.Errorf("approval events = %q", msgs)
	}
	if err := c.RedeployPreview(p, "alice"); !errors.Is(err, ErrAwaitingApproval) {
		t.Errorf("redeploy while held: %v", err)
	}
	if err := c.ApprovePreview(p, "alice", "fedcba9876"); err == nil {
		t.Error("approved a commit the preview does not hold")
	}

	if err := c.ApprovePreview(p, "alice", head[:7]); err != nil {
		t.Fatal(err)
	}
	c.idleForTest(t)
	p, _ = c.Site(p.ID)
	if v := c.PreviewView(ctx, p); v.State != model.PreviewReady {
		t.Fatalf("after approval: %s (%+v)", v.State, v.LastDeployment)
	}
	if p.Preview.AwaitingApproval || p.Preview.ApprovedCommit != head || p.Preview.ApprovedBy != "alice" || len(p.Bindings) != 1 {
		t.Errorf("approved preview = %+v, bindings %+v", p.Preview, p.Bindings)
	}
	if got := get(t, port, "pr-9.preview.test"); !strings.Contains(got, "pr content") {
		t.Fatalf("preview serves %q", got)
	}
	if err := c.ApprovePreview(p, "alice", ""); !errors.Is(err, ErrNotAwaitingApproval) {
		t.Errorf("approving again: %v", err)
	}
	// Redeploying the approved commit needs no approval.
	if err := c.RedeployPreview(p, "alice"); err != nil {
		t.Fatal(err)
	}
	c.idleForTest(t)
	if v := c.PreviewView(ctx, p); v.State != model.PreviewReady {
		t.Fatalf("after redeploy: %s", v.State)
	}

	// A new push is held; the approved build keeps serving.
	second := repo.onBranch("fix/login", "pr v2")
	c.PreviewWebhook(parent, forkEvent(9, "fix/login", second))
	c.idleForTest(t)
	p, _ = c.Site(p.ID)
	if v := c.PreviewView(ctx, p); v.State != model.PreviewAwaitingApproval || p.Preview.Commit != second || p.Preview.ApprovedCommit != head {
		t.Fatalf("after a push: %s %+v", v.State, p.Preview)
	}
	if got := get(t, port, "pr-9.preview.test"); !strings.Contains(got, "pr content") {
		t.Fatalf("the held push was deployed: %q", got)
	}

	// The branch moves on before the approval is applied: nothing is
	// built, and the newest head waits for approval in turn.
	third := repo.onBranch("fix/login", "pr v3")
	if err := c.ApprovePreview(p, "bob", ""); err != nil {
		t.Fatal(err)
	}
	c.idleForTest(t)
	p, _ = c.Site(p.ID)
	if v := c.PreviewView(ctx, p); v.State != model.PreviewAwaitingApproval || p.Preview.Commit != third {
		t.Fatalf("after the ref moved: %s %+v", v.State, p.Preview)
	}
	if got := get(t, port, "pr-9.preview.test"); !strings.Contains(got, "pr content") || strings.Contains(got, "v3") {
		t.Fatalf("an unapproved commit was deployed: %q", got)
	}
	if err := c.ApprovePreview(p, "bob", third); err != nil {
		t.Fatal(err)
	}
	c.idleForTest(t)
	if got := get(t, port, "pr-9.preview.test"); !strings.Contains(got, "pr v3") {
		t.Fatalf("after approving the new head: %q", got)
	}
}

// TestPreviewRequireApprovalAll: every pull request waits for approval,
// branch previews do not.
func TestPreviewRequireApprovalAll(t *testing.T) {
	repo := newGitRepo(t)
	c := testCore(t)
	parent := previewParentSite(t, c, repo.dir, freeTCPPort(t), func(s *model.Site) {
		s.Deploy.Previews.RequireApproval = model.PreviewApproveAll
		s.Deploy.Previews.Branches = []string{"fix/*"}
	})
	ev := prEvent(preview.ActionDeploy, 3, "fix/login")
	ev.Commit = repo.git("rev-parse", "fix/login")
	c.PreviewWebhook(parent, ev)
	c.idleForTest(t)
	push := &preview.Event{Kind: model.PreviewBranch, Branch: "fix/login", Ref: "refs/heads/fix/login", Action: preview.ActionDeploy, Reason: "pushed"}
	c.PreviewWebhook(parent, push)
	c.idleForTest(t)
	states := map[string]string{}
	for _, p := range c.Previews(parent.ID) {
		states[p.Preview.Key] = c.PreviewView(context.Background(), p).State
	}
	if states["pr:3"] != model.PreviewAwaitingApproval || states["branch:fix/login"] != model.PreviewReady {
		t.Fatalf("states = %v", states)
	}
}

// TestPreviewForksTurnedOff: turning forks off deletes their previews at
// once; a redeploy the settings no longer allow is refused.
func TestPreviewForksTurnedOff(t *testing.T) {
	repo := newGitRepo(t)
	c := testCore(t)
	ctx := context.Background()
	parent := previewParentSite(t, c, repo.dir, freeTCPPort(t), func(s *model.Site) { s.Deploy.Previews.AllowForks = true })
	head := repo.git("rev-parse", "fix/login")
	c.PreviewWebhook(parent, forkEvent(9, "fix/login", head))
	c.PreviewWebhook(parent, prEvent(preview.ActionDeploy, 4, "fix/login"))
	c.idleForTest(t)
	for _, p := range c.Previews(parent.ID) {
		if p.Preview.Fork {
			c.ApprovePreview(p, "alice", "")
		}
	}
	c.idleForTest(t)
	if n := len(c.Previews(parent.ID)); n != 2 {
		t.Fatalf("previews = %d", n)
	}

	cur, _ := c.Site(parent.ID)
	in := Masked(cur)
	in.Deploy.Previews.AllowForks = false
	if _, err := c.UpdateSite(ctx, parent.ID, in); err != nil {
		t.Fatal(err)
	}
	c.idleForTest(t)
	list := c.Previews(parent.ID)
	if len(list) != 1 || list[0].Preview.Key != "pr:4" {
		t.Fatalf("previews left = %d", len(list))
	}
	if msgs := eventMessages(t, c, parent.ID, events.PreviewDeleted); len(msgs) != 1 || !strings.Contains(msgs[0], "forks disabled") {
		t.Errorf("deleted events = %q", msgs)
	}

	// Pull requests turned off: the redeploy of one is refused.
	cur, _ = c.Site(parent.ID)
	in = Masked(cur)
	in.Deploy.Previews.PullRequests, in.Deploy.Previews.Branches = false, []string{"feature/*"}
	if _, err := c.UpdateSite(ctx, parent.ID, in); err != nil {
		t.Fatal(err)
	}
	before, _ := c.Store.ListDeployments(ctx, list[0].ID, 10)
	if err := c.RedeployPreview(list[0], "alice"); err != nil {
		t.Fatal(err)
	}
	c.idleForTest(t)
	after, _ := c.Store.ListDeployments(ctx, list[0].ID, 10)
	if len(after) != len(before) {
		t.Error("a pull request was redeployed with pull request previews off")
	}
	failed := eventMessages(t, c, parent.ID, events.PreviewFailed)
	if len(failed) == 0 || !strings.Contains(failed[0], "pull requests are turned off") {
		t.Errorf("failed events = %q", failed)
	}
}

// TestPreviewEvictionPrefersBrokenPreviews: a new preview beyond the
// maximum evicts one that never deployed before a working one, and a pull
// request held for approval never evicts a working preview.
func TestPreviewEvictionPrefersBrokenPreviews(t *testing.T) {
	repo := newGitRepo(t)
	c := testCore(t)
	parent := previewParentSite(t, c, repo.dir, freeTCPPort(t), func(s *model.Site) {
		s.Deploy.Previews.MaxPreviews = 2
		s.Deploy.Previews.AllowForks = true
	})
	c.PreviewWebhook(parent, prEvent(preview.ActionDeploy, 1, "fix/login"))
	c.idleForTest(t)
	c.PreviewWebhook(parent, prEvent(preview.ActionDeploy, 2, "no-such-branch")) // fails
	c.idleForTest(t)
	c.PreviewWebhook(parent, prEvent(preview.ActionDeploy, 3, "fix/login"))
	c.idleForTest(t)
	keys := func() string {
		var k []string
		for _, p := range c.Previews(parent.ID) {
			k = append(k, p.Preview.Key)
		}
		slices.Sort(k)
		return strings.Join(k, ",")
	}
	if got := keys(); got != "pr:1,pr:3" {
		t.Fatalf("previews = %s, want the failed pr:2 evicted", got)
	}
	// Both work: a fork's pull request waiting for approval is refused.
	c.PreviewWebhook(parent, forkEvent(5, "fix/login", repo.git("rev-parse", "fix/login")))
	c.idleForTest(t)
	if got := keys(); got != "pr:1,pr:3" {
		t.Fatalf("previews = %s: a held pull request evicted a working preview", got)
	}
	if msgs := eventMessages(t, c, parent.ID, events.PreviewFailed); len(msgs) == 0 || !strings.Contains(msgs[0], "waiting for approval") {
		t.Errorf("failed events = %q", msgs)
	}
}

// TestPreviewBuildLimit: preview deployments wait for one of the server's
// build slots.
func TestPreviewBuildLimit(t *testing.T) {
	repo := newGitRepo(t)
	c := testCore(t)
	parent := previewParentSite(t, c, repo.dir, freeTCPPort(t), nil)
	c.queuePreview(&previewOp{parentID: "none", key: "x", ev: &preview.Event{}}) // starts the queue
	c.idleForTest(t)
	for range cap(c.previews.builds) {
		c.previews.builds <- struct{}{} // every slot taken
	}
	c.PreviewWebhook(parent, prEvent(preview.ActionDeploy, 1, "fix/login"))
	waitFor(t, "the preview to be created", func() bool { return len(c.Previews(parent.ID)) == 1 })
	time.Sleep(300 * time.Millisecond)
	p := c.Previews(parent.ID)[0]
	if deps, _ := c.Store.ListDeployments(context.Background(), p.ID, 5); len(deps) != 0 {
		t.Fatalf("deployed without a build slot: %+v", deps)
	}
	if v := c.PreviewView(context.Background(), p); v.State != model.PreviewDeploying {
		t.Errorf("waiting preview state = %s", v.State)
	}
	for range cap(c.previews.builds) {
		<-c.previews.builds
	}
	c.idleForTest(t)
	if v := c.PreviewView(context.Background(), p); v.State != model.PreviewReady {
		t.Errorf("state = %s", v.State)
	}
	if len(c.previews.builds) != 0 {
		t.Errorf("%d build slots still held", len(c.previews.builds))
	}
}

// TestPreviewSecretsNotInherited: previews get the parent's plain
// variables only, unless inheritSecrets is on for a same-repository
// preview; a fork's never gets them.
func TestPreviewSecretsNotInherited(t *testing.T) {
	parent := &model.Site{
		ID: "parent", Name: "api", Type: model.SiteNode,
		Node: &model.NodeConfig{AppRoot: `D:\apps\api`, Script: "server.js", Env: []model.EnvVar{
			{Name: "DATABASE_URL", Value: "sealed-prod", Secret: true},
			{Name: "STRIPE_KEY", From: &model.SecretRef{Store: "vault", Ref: "app#STRIPE"}},
			{Name: "SMTP_HOST", Value: "mail.internal", SlotSetting: true},
			{Name: "API_KEY", Value: "sealed-api", Secret: true},
			{Name: "LOG", Value: "info"},
		}},
		Deploy: model.DeployConfig{Git: model.GitSource{Repo: "https://github.com/org/api.git", Branch: "main", Token: "sealed-git"},
			Previews: model.PreviewConfig{Enabled: true, HostPattern: "pr-{number}.preview.example.com", Protocol: "http", Port: 80,
				Env: []model.EnvVar{{Name: "api_key", Value: "sealed-preview", Secret: true}}}},
	}
	names := func(s *model.Site) string {
		var n []string
		for _, e := range s.Node.Env {
			if !slices.Contains(model.PreviewVars, e.Name) {
				n = append(n, e.Name+"="+e.Value)
			}
		}
		return strings.Join(n, ",")
	}
	info := model.PreviewInfo{Key: "pr:1", Kind: model.PreviewPR, Number: 1, Branch: "x", Host: "pr-1.preview.example.com"}
	if got := names(derivePreview(parent, nil, info, "")); got != "LOG=info,api_key=sealed-preview" {
		t.Errorf("default: %s", got)
	}
	parent.Deploy.Previews.InheritSecrets = true
	if got := names(derivePreview(parent, nil, info, "")); got != "DATABASE_URL=sealed-prod,STRIPE_KEY=,SMTP_HOST=mail.internal,api_key=sealed-preview,LOG=info" {
		t.Errorf("inherited: %s", got)
	}
	info.Fork = true
	s := derivePreview(parent, nil, info, "")
	if got := names(s); got != "LOG=info,api_key=sealed-preview" {
		t.Errorf("fork: %s", got)
	}
	// NodeHoster keeps using the git token to fetch; it is no variable.
	if s.Deploy.Git.Token != "sealed-git" || strings.Contains(names(s), "sealed-git") {
		t.Errorf("git = %+v", s.Deploy.Git)
	}
	if len(parent.Node.Env) != 5 {
		t.Error("derivePreview modified the parent")
	}
}

// TestPreviewDerivesProtection: an https preview keeps the parent's
// client certificate policy, its effective firewall mode, and none of its
// deployment slots.
func TestPreviewDerivesProtection(t *testing.T) {
	policy := &model.ClientCertPolicy{Mode: model.ClientCertRequire, CAPEM: "ca", AllowedSubjects: []string{"ops"}}
	parent := &model.Site{
		ID: "parent", Name: "api", Type: model.SiteNode,
		Bindings: []model.Binding{
			{Protocol: "https", Port: 443, Host: "api.example.com", CertMode: model.CertModeAuto, ClientCert: policy},
			{Protocol: "http", Port: 80, Host: "staging.example.com", Slot: "staging"},
		},
		Node:  &model.NodeConfig{AppRoot: `D:\apps\api`, Script: "server.js", Instances: 2},
		Slots: []model.DeploymentSlot{{Name: "staging", ActiveRelease: "r1"}},
		Deploy: model.DeployConfig{Git: model.GitSource{Repo: "https://github.com/org/api.git", Branch: "main"},
			Previews: model.PreviewConfig{Enabled: true, HostPattern: "pr-{number}.preview.example.com", Protocol: "https", Port: 443, CertMode: "auto"}},
	}
	info := model.PreviewInfo{Key: "pr:1", Kind: model.PreviewPR, Number: 1, Branch: "x", Host: "pr-1.preview.example.com"}
	s := derivePreview(parent, nil, info, "")
	if len(s.Bindings) != 1 || s.Bindings[0].ClientCert == nil || s.Bindings[0].ClientCert.Mode != model.ClientCertRequire ||
		s.Bindings[0].ClientCert.AllowedSubjects[0] != "ops" || s.Bindings[0].Slot != "" {
		t.Errorf("binding = %+v", s.Bindings)
	}
	if len(s.Slots) != 0 {
		t.Errorf("slots = %+v", s.Slots)
	}
	if s.Routing.WAF.Mode != model.WAFOff {
		t.Errorf("a pre-firewall parent's preview has firewall mode %q, want off", s.Routing.WAF.Mode)
	}
	parent.Routing.WAF.Mode = model.WAFBlock
	if s := derivePreview(parent, nil, info, ""); s.Routing.WAF.Mode != model.WAFBlock {
		t.Errorf("firewall mode = %q", s.Routing.WAF.Mode)
	}
	// Held since created: no binding at all.
	info.AwaitingApproval = true
	if s := derivePreview(parent, nil, info, ""); len(s.Bindings) != 0 {
		t.Errorf("held preview bindings = %+v", s.Bindings)
	}
}

// TestPreviewWAFModeStable: the preview of a site from before the
// firewall stays off across deployments, whatever the defaults for new
// sites say.
func TestPreviewWAFModeStable(t *testing.T) {
	c := testCore(t)
	ctx := context.Background()
	set := c.Settings()
	set.WAF.DefaultMode = model.WAFBlock
	if _, err := c.UpdateSettings(ctx, set); err != nil {
		t.Fatal(err)
	}
	parent := previewParentSite(t, c, filepath.Join(t.TempDir(), "missing"), freeTCPPort(t), nil)
	c.sitesMu.Lock()
	old := clone(parent)
	old.Routing.WAF = model.WAFConfig{} // as stored before the firewall existed
	parent, err := c.updateSite(ctx, parent.ID, old)
	c.sitesMu.Unlock()
	if err != nil || parent.Routing.WAF.Mode != "" {
		t.Fatalf("parent = %+v, %v", parent.Routing.WAF, err)
	}
	for range 2 {
		c.PreviewWebhook(parent, prEvent(preview.ActionDeploy, 1, "b"))
		c.idleForTest(t)
		if p := c.Previews(parent.ID)[0]; p.Routing.WAF.Mode != model.WAFOff {
			t.Fatalf("preview firewall mode = %q", p.Routing.WAF.Mode)
		}
	}
}

// TestPreviewAutoCertLimit: previews stop asking Let's Encrypt for
// certificates of their own beyond the weekly limit per domain.
func TestPreviewAutoCertLimit(t *testing.T) {
	c := testCore(t)
	ctx := context.Background()
	defer func(n int) { maxAutoPreviewCerts = n }(maxAutoPreviewCerts)
	maxAutoPreviewCerts = 2
	cfg := model.PreviewConfig{HostPattern: "pr-{number}.preview.test", Protocol: "https", CertMode: model.PreviewCertAuto}
	site := func(host, mode string) *model.Site {
		return &model.Site{Bindings: []model.Binding{{Protocol: "https", Host: host, CertMode: mode}}}
	}
	for _, h := range []string{"pr-1.preview.test", "pr-2.preview.test"} {
		if err := c.countAutoCert(ctx, cfg, nil, site(h, model.CertModeAuto)); err != nil {
			t.Fatal(err)
		}
	}
	// A preview that has its certificate, one with a wildcard, one held
	// without a binding: none counts.
	if err := c.countAutoCert(ctx, cfg, site("pr-1.preview.test", model.CertModeAuto), site("pr-1.preview.test", model.CertModeAuto)); err != nil {
		t.Errorf("refresh: %v", err)
	}
	if err := c.countAutoCert(ctx, cfg, nil, site("pr-3.preview.test", model.CertModeManual)); err != nil {
		t.Errorf("wildcard: %v", err)
	}
	if err := c.countAutoCert(ctx, cfg, nil, &model.Site{}); err != nil {
		t.Errorf("held: %v", err)
	}
	err := c.countAutoCert(ctx, cfg, nil, site("pr-3.preview.test", model.CertModeAuto))
	if err == nil || !strings.Contains(err.Error(), "wildcard") {
		t.Fatalf("third certificate: %v", err)
	}
	// Another domain has its own count.
	other := cfg
	other.HostPattern = "pr-{number}.qa.test"
	if err := c.countAutoCert(ctx, other, nil, site("pr-3.qa.test", model.CertModeAuto)); err != nil {
		t.Errorf("other domain: %v", err)
	}
	// A week later the count is free again.
	var ledger map[string][]time.Time
	c.Store.GetDoc(ctx, autoPreviewCertsDoc, &ledger)
	for d := range ledger {
		for i := range ledger[d] {
			ledger[d][i] = ledger[d][i].Add(-8 * 24 * time.Hour)
		}
	}
	c.Store.PutDoc(ctx, autoPreviewCertsDoc, ledger)
	if err := c.countAutoCert(ctx, cfg, nil, site("pr-4.preview.test", model.CertModeAuto)); err != nil {
		t.Errorf("after a week: %v", err)
	}
}
