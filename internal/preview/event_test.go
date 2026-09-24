package preview

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
)

func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

func mustParse(t *testing.T, h http.Header, body string) *Event {
	t.Helper()
	ev, err := Parse(h, []byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return ev
}

const githubPR = `{
  "action": "%s",
  "number": 42,
  "pull_request": {
    "number": 42, "title": "Fix the login", "html_url": "https://github.com/org/app/pull/42", "merged": %s,
    "user": {"login": "octocat"},
    "head": {"ref": "fix/login", "sha": "0123456789abcdef0123456789abcdef01234567", "repo": {"id": %s, "full_name": "%s"}},
    "base": {"ref": "main", "repo": {"id": 1, "full_name": "org/app"}}
  }
}`

func githubPRBody(action, merged, headID, headName string) string {
	return fmt.Sprintf(githubPR, action, merged, headID, headName)
}

func TestParseGitHubPullRequest(t *testing.T) {
	t.Parallel()
	h := hdr("X-GitHub-Event", "pull_request")
	ev := mustParse(t, h, githubPRBody("opened", "false", "1", "org/app"))
	want := Event{
		Provider: GitHub, Kind: model.PreviewPR, Action: ActionDeploy, Reason: "opened", Number: 42,
		Branch: "fix/login", Ref: "refs/heads/fix/login", Commit: "0123456789abcdef0123456789abcdef01234567",
		Title: "Fix the login", Author: "octocat", URL: "https://github.com/org/app/pull/42",
	}
	if *ev != want {
		t.Fatalf("event = %+v\nwant    %+v", *ev, want)
	}
	if ev.Key() != "pr:42" {
		t.Errorf("key = %q", ev.Key())
	}
	for action, want := range map[string]string{
		"synchronize": ActionDeploy, "reopened": ActionDeploy, "closed": ActionDelete,
		"labeled": ActionIgnore, "edited": ActionIgnore, "assigned": ActionIgnore,
	} {
		if got := mustParse(t, h, githubPRBody(action, "false", "1", "org/app")).Action; got != want {
			t.Errorf("action %s -> %s, want %s", action, got, want)
		}
	}
	merged := mustParse(t, h, githubPRBody("closed", "true", "1", "org/app"))
	if merged.Action != ActionDelete || merged.Reason != "merged" {
		t.Errorf("merged = %+v", merged)
	}
}

func TestParseGitHubForkPullRequest(t *testing.T) {
	t.Parallel()
	h := hdr("X-GitHub-Event", "pull_request")
	ev := mustParse(t, h, githubPRBody("opened", "false", "99", "someone/app"))
	if !ev.Fork || ev.Ref != "refs/pull/42/head" {
		t.Fatalf("fork = %v ref = %q", ev.Fork, ev.Ref)
	}
	// A deleted fork: no head repository.
	gone := `{"action":"synchronize","number":5,"pull_request":{"head":{"ref":"x","sha":"abc","repo":null},"base":{"ref":"main","repo":{"id":1,"full_name":"org/app"}}}}`
	if ev := mustParse(t, h, gone); !ev.Fork {
		t.Error("a pull request without a head repository is not treated as a fork")
	}
}

func TestParseGiteaPullRequest(t *testing.T) {
	t.Parallel()
	// Gitea sends GitHub's header too; its own wins.
	h := hdr("X-Gitea-Event", "pull_request", "X-GitHub-Event", "pull_request")
	body := `{"action":"synchronized","number":3,"pull_request":{"title":"T","html_url":"https://gitea.example/org/app/pulls/3",
	  "user":{"login":"dev"},"head":{"ref":"feat","sha":"abcdef1234567","repo_id":7,"repo":{"id":7,"full_name":"org/app"}},
	  "base":{"ref":"main","repo_id":7,"repo":{"id":7,"full_name":"org/app"}}}}`
	ev := mustParse(t, h, body)
	if ev.Provider != Gitea || ev.Action != ActionDeploy || ev.Fork || ev.Number != 3 || ev.Ref != "refs/heads/feat" {
		t.Fatalf("event = %+v", ev)
	}
}

func TestParseGitLabMergeRequest(t *testing.T) {
	t.Parallel()
	h := hdr("X-Gitlab-Event", "Merge Request Hook")
	mr := func(action, oldrev string, source int) string {
		return fmt.Sprintf(`{"object_kind":"merge_request","user":{"username":"dev"},"object_attributes":{"iid":12,"action":"%s","title":"MR",
		  "url":"https://gitlab.example/g/app/-/merge_requests/12","source_branch":"topic","target_branch":"main",
		  "source_project_id":%d,"target_project_id":5,"oldrev":"%s","last_commit":{"id":"fedcba9876543210fedcba9876543210fedcba98"}}}`, action, source, oldrev)
	}
	ev := mustParse(t, h, mr("open", "", 5))
	if ev.Provider != GitLab || ev.Kind != model.PreviewPR || ev.Action != ActionDeploy || ev.Number != 12 ||
		ev.Ref != "refs/heads/topic" || ev.Commit != "fedcba9876543210fedcba9876543210fedcba98" || ev.Author != "dev" {
		t.Fatalf("event = %+v", ev)
	}
	if got := mustParse(t, h, mr("update", "", 5)).Action; got != ActionIgnore {
		t.Errorf("update without new commits: %s", got)
	}
	if got := mustParse(t, h, mr("update", "1111111", 5)).Action; got != ActionDeploy {
		t.Errorf("update with new commits: %s", got)
	}
	for _, a := range []string{"close", "merge"} {
		if got := mustParse(t, h, mr(a, "", 5)); got.Action != ActionDelete {
			t.Errorf("%s: %+v", a, got)
		}
	}
	fork := mustParse(t, h, mr("open", "", 77))
	if !fork.Fork || fork.Ref != "refs/merge-requests/12/head" {
		t.Errorf("fork = %+v", fork)
	}
}

func TestParsePushes(t *testing.T) {
	t.Parallel()
	gh := mustParse(t, hdr("X-GitHub-Event", "push"),
		`{"ref":"refs/heads/feature/x","after":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","pusher":{"name":"dev"},"head_commit":{"message":"Add x\n\nbody"}}`)
	if gh.Kind != model.PreviewBranch || gh.Branch != "feature/x" || gh.Action != ActionDeploy || gh.Title != "Add x" || gh.Key() != "branch:feature/x" {
		t.Fatalf("push = %+v", gh)
	}
	del := mustParse(t, hdr("X-GitHub-Event", "push"), `{"ref":"refs/heads/feature/x","after":"`+zeroSHA+`","deleted":true}`)
	if del.Action != ActionDelete {
		t.Errorf("deletion push = %+v", del)
	}
	gl := mustParse(t, hdr("X-Gitlab-Event", "Push Hook"), `{"object_kind":"push","ref":"refs/heads/dev","after":"`+zeroSHA+`","checkout_sha":null}`)
	if gl.Action != ActionDelete || gl.Branch != "dev" {
		t.Errorf("GitLab deletion = %+v", gl)
	}
	if ev := mustParse(t, hdr("X-GitHub-Event", "push"), `{"ref":"refs/tags/v1.0"}`); ev != nil {
		t.Errorf("a tag push = %+v, want nil", ev)
	}
	delEv := mustParse(t, hdr("X-GitHub-Event", "delete"), `{"ref":"feature/x","ref_type":"branch"}`)
	if delEv == nil || delEv.Action != ActionDelete || delEv.Branch != "feature/x" {
		t.Errorf("delete event = %+v", delEv)
	}
	if ev := mustParse(t, hdr("X-Gitea-Event", "delete"), `{"ref":"v1","ref_type":"tag"}`); ev != nil {
		t.Errorf("tag deletion = %+v", ev)
	}
}

func TestParseIgnoresOtherDeliveries(t *testing.T) {
	t.Parallel()
	for _, h := range []http.Header{
		hdr("X-GitHub-Event", "ping"),
		hdr("X-GitHub-Event", "issue_comment"),
		hdr("X-Gitlab-Event", "Note Hook"),
		{}, // a script calling with ?secret=
	} {
		if ev := mustParse(t, h, `{"zen":"x","object_kind":"note","ref":"refs/heads/x"}`); ev != nil {
			t.Errorf("%v: event = %+v", h, ev)
		}
	}
}

func TestParseRejectsHostileBranchNames(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		`{"ref":"refs/heads/--upload-pack=touch /tmp/x","after":"a"}`,
		`{"ref":"refs/heads/a..b","after":"a"}`,
	} {
		if _, err := Parse(hdr("X-GitHub-Event", "push"), []byte(body)); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
	pr := githubPRBody("opened", "false", "1", "org/app")
	pr = strings.Replace(pr, `"ref": "fix/login"`, `"ref": "-x"`, 1)
	if _, err := Parse(hdr("X-GitHub-Event", "pull_request"), []byte(pr)); err == nil {
		t.Error("accepted a pull request whose branch starts with -")
	}
	if _, err := Parse(hdr("X-GitHub-Event", "pull_request"), []byte(`{not json`)); err == nil {
		t.Error("accepted invalid JSON")
	}
}

func TestIsPush(t *testing.T) {
	t.Parallel()
	cases := []struct {
		h    http.Header
		want bool
	}{
		{hdr("X-GitHub-Event", "push"), true},
		{hdr("X-Gitlab-Event", "Push Hook"), true},
		{hdr("X-Gitea-Event", "push", "X-GitHub-Event", "push"), true},
		{hdr("X-GitHub-Event", "pull_request"), false},
		{hdr("X-Gitlab-Event", "Merge Request Hook"), false},
		{http.Header{}, false},
	}
	for _, c := range cases {
		if got := IsPush(c.h); got != c.want {
			t.Errorf("IsPush(%v) = %v", c.h, got)
		}
	}
}
