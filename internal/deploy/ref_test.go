package deploy

import (
	"context"
	"encoding/base64"
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
	dep, err := h.d.DeployRef(context.Background(), site, "refs/pull/42/head", "preview", "webhook", func(d *model.Deployment) { finished <- d })
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
	dep, err := h.d.DeployRef(context.Background(), site, "refs/heads/deleted", "preview", "webhook", func(d *model.Deployment) { finished <- d })
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
		if _, err := h.d.DeployRef(context.Background(), site, ref, "preview", "u", nil); err == nil {
			t.Errorf("DeployRef(%q) was accepted", ref)
		}
	}
	nogit := h.staticSite(t, "nogit", nil)
	if _, err := h.d.DeployRef(context.Background(), nogit, "refs/heads/x", "preview", "u", nil); err == nil || !strings.Contains(err.Error(), "no git repository") {
		t.Errorf("error = %v", err)
	}
}
