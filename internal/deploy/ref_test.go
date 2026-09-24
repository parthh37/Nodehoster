package deploy

import (
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// gitCalls splits the fake git's record into argument lists and the GIT_
// environment of the last call.
func gitCalls(t *testing.T, record string) (calls [][]string, env map[string]string) {
	t.Helper()
	env = map[string]string{}
	for _, line := range strings.Split(readFile(t, record), "\n") {
		parts := strings.Split(line, "\x00")
		switch parts[0] {
		case "ARGS":
			calls = append(calls, parts[1:])
		case "ENV":
			if k, v, ok := strings.Cut(parts[1], "="); ok {
				env[k] = v
			}
		}
	}
	return calls, env
}

func TestDeployRefFetchesTheRef(t *testing.T) {
	// Not parallel: it changes PATH to put a fake git first.
	const token = "fake-git-token-for-ref-7c1e"
	h := newHarness(t)
	record := installFakeGit(t)
	sealed, err := h.box.Seal(token)
	if err != nil {
		t.Fatal(err)
	}
	repo := "https://git.example.invalid/org/app.git"
	site := h.staticSite(t, "pr", func(s *model.Site) {
		s.Deploy.Git = model.GitSource{Repo: repo, Branch: "fix/login", Token: sealed}
	})
	finished := make(chan *model.Deployment, 1)
	dep, err := h.d.DeployRef(context.Background(), site, "refs/pull/42/head", "", "preview", "webhook", func(d *model.Deployment) { finished <- d })
	if err != nil {
		t.Fatal(err)
	}
	got := h.wait(t, dep)
	if got.Status != "succeeded" || got.Source != "preview" {
		log, _ := h.d.Log(site.ID, dep.ID)
		t.Fatalf("status = %s source = %s (%s)\n%s", got.Status, got.Source, got.Message, log)
	}
	select {
	case d := <-finished:
		if d.Status != "succeeded" || d.Commit != "0123456789abcdef0123456789abcdef01234567" {
			t.Errorf("done got %+v", d)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("done was not called")
	}

	calls, env := gitCalls(t, record)
	var fetch []string
	for _, c := range calls {
		if len(c) > 2 && c[0] == "-C" && c[2] == "fetch" {
			fetch = c
		}
		for _, a := range c {
			if strings.Contains(a, token) {
				t.Errorf("the token appears in git's arguments: %q", c)
			}
		}
	}
	want := []string{"-C", got.ReleaseDir, "fetch", "--depth", "1", "--no-tags", "--", repo, "refs/pull/42/head"}
	if strings.Join(fetch, " ") != strings.Join(want, " ") {
		t.Errorf("fetch = %q\nwant    %q", fetch, want)
	}
	if len(calls) == 0 || calls[0][0] != "init" {
		t.Errorf("first call = %q, want git init", calls)
	}
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	if env["GIT_CONFIG_VALUE_0"] != "Authorization: Basic "+basic || env["GIT_TERMINAL_PROMPT"] != "0" {
		t.Errorf("git environment = %v", env)
	}
	if exists(filepath.Join(got.ReleaseDir, ".git")) {
		t.Error(".git was left in the release")
	}
	if b := readFile(t, filepath.Join(got.ReleaseDir, "index.html")); b != "from ref" {
		t.Errorf("index.html = %q", b)
	}
	log, _ := h.d.Log(site.ID, dep.ID)
	if strings.Contains(string(log), token) || !strings.Contains(string(log), "refs/pull/42/head") {
		t.Errorf("log:\n%s", log)
	}
}

func TestDeployRefFailureCallsDone(t *testing.T) {
	h := newHarness(t)
	installFakeGit(t)
	t.Setenv("NH_FAKE_GIT_FAIL_FETCH", "1")
	site := h.staticSite(t, "gone", func(s *model.Site) {
		s.Deploy.Git = model.GitSource{Repo: "https://git.example.invalid/org/app.git", Branch: "x"}
	})
	finished := make(chan *model.Deployment, 1)
	dep, err := h.d.DeployRef(context.Background(), site, "refs/heads/deleted", "", "preview", "webhook", func(d *model.Deployment) { finished <- d })
	if err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-finished:
		if d.Status != "failed" || !strings.Contains(d.Message, "git fetch failed") {
			t.Errorf("done got %+v", d)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("done was not called")
	}
	if got := h.wait(t, dep); got.ReleaseDir != "" || exists(dep.ReleaseDir) {
		t.Errorf("a failed release was kept: %+v", got)
	}
	if len(h.act.calls()) != 0 {
		t.Error("a failed deployment was activated")
	}
}

func TestDeployRefRefusesBadRefs(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	site := h.staticSite(t, "bad", func(s *model.Site) {
		s.Deploy.Git = model.GitSource{Repo: "https://git.example.invalid/org/app.git"}
	})
	for _, ref := range []string{"", "main", "--upload-pack=x", "refs/heads/a..b", "refs/heads/-x/../y"} {
		if _, err := h.d.DeployRef(context.Background(), site, ref, "", "preview", "u", nil); err == nil {
			t.Errorf("DeployRef(%q) was accepted", ref)
		}
	}
	nogit := h.staticSite(t, "nogit", nil)
	if _, err := h.d.DeployRef(context.Background(), nogit, "refs/heads/x", "", "preview", "u", nil); err == nil || !strings.Contains(err.Error(), "no git repository") {
		t.Errorf("error = %v", err)
	}
}

// TestDeployRefTokenFromSecretStore: a preview of a private repository
// whose token is held in a secret store fetches with that token, read at
// the deployment, as DeployGit does.
func TestDeployRefTokenFromSecretStore(t *testing.T) {
	// Not parallel: it changes PATH to put a fake git first.
	const token = "store-held-token-for-ref-51ad"
	h := newHarness(t)
	record := installFakeGit(t)
	var asked []model.SecretRef
	h.d.opts.SecretToken = func(_ *model.Site, ref model.SecretRef) (string, error) {
		asked = append(asked, ref)
		return token, nil
	}
	ref := model.SecretRef{Store: "vault", Ref: "git#TOKEN"}
	site := h.staticSite(t, "private", func(s *model.Site) {
		s.Deploy.Git = model.GitSource{Repo: "https://git.example.invalid/org/app.git", Branch: "fix/login", TokenFrom: &ref}
	})
	dep, err := h.d.DeployRef(context.Background(), site, "refs/heads/fix/login", "", "preview", "webhook", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := h.wait(t, dep); got.Status != "succeeded" {
		t.Fatalf("status = %s (%s)", got.Status, got.Message)
	}
	if len(asked) != 1 || asked[0] != ref {
		t.Errorf("secret store asked for %v", asked)
	}
	_, env := gitCalls(t, record)
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	if env["GIT_CONFIG_VALUE_0"] != "Authorization: Basic "+basic {
		t.Errorf("git environment = %v: the store's token was not used", env)
	}
	log, _ := h.d.Log(site.ID, dep.ID)
	if strings.Contains(string(log), token) {
		t.Errorf("the token is in the log:\n%s", log)
	}

	// A store that cannot be read fails the deployment before git runs.
	h.d.opts.SecretToken = func(*model.Site, model.SecretRef) (string, error) { return "", errors.New("vault is sealed") }
	dep, err = h.d.DeployRef(context.Background(), site, "refs/heads/fix/login", "", "preview", "webhook", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := h.wait(t, dep); got.Status != "failed" || !strings.Contains(got.Message, "vault is sealed") {
		t.Errorf("deployment = %s (%s)", got.Status, got.Message)
	}
}

// TestDeployRefPinnedCommit: a deployment pinned to a commit (an approved
// preview) fails before anything is built when the ref has moved on.
func TestDeployRefPinnedCommit(t *testing.T) {
	h := newHarness(t)
	installFakeGit(t)
	site := h.staticSite(t, "pinned", func(s *model.Site) {
		s.Deploy.Git = model.GitSource{Repo: "https://git.example.invalid/org/app.git", Branch: "x"}
	})
	// The fake git's head is 0123456789abcdef...
	for _, tc := range []struct {
		commit string
		ok     bool
	}{
		{"0123456789ABCDEF0123456789abcdef01234567", true},
		{"0123456", true},
		{"fedcba9876543210fedcba9876543210fedcba98", false},
	} {
		finished := make(chan *model.Deployment, 1)
		dep, err := h.d.DeployRef(context.Background(), site, "refs/pull/7/head", tc.commit, "preview", "u", func(d *model.Deployment) { finished <- d })
		if err != nil {
			t.Fatal(err)
		}
		d := <-finished
		h.wait(t, dep)
		if tc.ok && d.Status != "succeeded" || !tc.ok && (d.Status != "failed" || !strings.Contains(d.Message, "approved commit")) {
			t.Errorf("pinned to %s: %s (%s)", tc.commit, d.Status, d.Message)
		}
		if !tc.ok && d.Commit != "0123456789abcdef0123456789abcdef01234567" {
			t.Errorf("the commit found is not reported: %q", d.Commit)
		}
	}
	if _, err := h.d.DeployRef(context.Background(), site, "refs/pull/7/head", "not-a-commit", "preview", "u", nil); err == nil {
		t.Error("a malformed commit was accepted")
	}
}
