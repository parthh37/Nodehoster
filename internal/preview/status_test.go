package preview

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const sha = "0123456789abcdef0123456789abcdef01234567"

func TestStatusURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		provider, repo, want string
	}{
		{GitHub, "https://github.com/org/app.git", "https://api.github.com/repos/org/app/statuses/" + sha},
		{GitHub, "https://x-access-token:secret@github.com/org/app", "https://api.github.com/repos/org/app/statuses/" + sha},
		{GitHub, "git@github.com:org/app.git", "https://api.github.com/repos/org/app/statuses/" + sha},
		{GitHub, "https://ghe.corp.example/org/app.git", "https://ghe.corp.example/api/v3/repos/org/app/statuses/" + sha},
		{GitLab, "https://gitlab.com/group/sub/app.git", "https://gitlab.com/api/v4/projects/group%2Fsub%2Fapp/statuses/" + sha},
		{GitLab, "ssh://git@gitlab.corp:2222/group/app.git", "https://gitlab.corp/api/v4/projects/group%2Fapp/statuses/" + sha},
		{Gitea, "http://gitea.lan:3000/org/app.git", "http://gitea.lan:3000/api/v1/repos/org/app/statuses/" + sha},
	}
	for _, c := range cases {
		got, err := StatusURL(c.provider, c.repo, sha)
		if err != nil || got != c.want {
			t.Errorf("StatusURL(%s, %s) = %q, %v; want %q", c.provider, c.repo, got, err, c.want)
		}
	}
	for _, bad := range []struct{ provider, repo, commit string }{
		{GitHub, "https://github.com/org/app.git", "not-a-sha"},
		{GitHub, "https://github.com/app.git", sha}, // no owner
		{GitHub, `C:\repos\app`, sha},
		{"bitbucket", "https://bitbucket.org/org/app.git", sha},
		{GitHub, "https://github.com/org/app.git", "../../../etc"},
	} {
		if u, err := StatusURL(bad.provider, bad.repo, bad.commit); err == nil {
			t.Errorf("StatusURL(%s, %s, %s) = %q, want an error", bad.provider, bad.repo, bad.commit, u)
		}
	}
	// Credentials in the repository URL never reach an error message.
	if _, err := StatusURL(GitHub, "https://user:hunter2@host.example/app", sha); err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error = %v", err)
	}
}

// fakeHost records status requests.
type fakeHost struct {
	srv    *httptest.Server
	path   string
	header http.Header
	body   map[string]string
	code   int
}

func newFakeHost(t *testing.T, code int) *fakeHost {
	f := &fakeHost{code: code}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.path, f.header = r.URL.EscapedPath(), r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &f.body)
		w.WriteHeader(f.code)
		if f.code >= 300 {
			io.WriteString(w, `{"message":"Bad credentials"}`)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func TestReportGitea(t *testing.T) {
	t.Parallel()
	f := newFakeHost(t, http.StatusCreated)
	repo := f.srv.URL + "/org/app.git" // http://127.0.0.1:port/org/app.git
	err := NewReporter().Report(context.Background(), Gitea, repo, "tok-123", Status{
		Commit: sha, State: StateSuccess, Description: "Preview ready", TargetURL: "https://pr-1.preview.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.path != "/api/v1/repos/org/app/statuses/"+sha {
		t.Errorf("path = %s", f.path)
	}
	if got := f.header.Get("Authorization"); got != "token tok-123" {
		t.Errorf("Authorization = %q", got)
	}
	want := map[string]string{"state": "success", "description": "Preview ready", "context": StatusContext, "target_url": "https://pr-1.preview.example.com"}
	for k, v := range want {
		if f.body[k] != v {
			t.Errorf("body[%s] = %q, want %q (%v)", k, f.body[k], v, f.body)
		}
	}
}

func TestReportGitLabMapsStates(t *testing.T) {
	t.Parallel()
	f := newFakeHost(t, http.StatusCreated)
	repo := f.srv.URL + "/group/app.git"
	for state, want := range map[string]string{StatePending: "running", StateFailure: "failed", StateError: "failed", StateSuccess: "success"} {
		if err := NewReporter().Report(context.Background(), GitLab, repo, "glpat", Status{Commit: sha, State: state}); err != nil {
			t.Fatal(err)
		}
		if f.body["state"] != want || f.body["name"] != StatusContext {
			t.Errorf("%s: body = %v", state, f.body)
		}
		if f.header.Get("PRIVATE-TOKEN") != "glpat" || f.header.Get("Authorization") != "" {
			t.Errorf("headers = %v", f.header)
		}
		if f.path != "/api/v4/projects/group%2Fapp/statuses/"+sha {
			t.Errorf("path = %s", f.path)
		}
	}
}

func TestReportErrorsKeepTheTokenOut(t *testing.T) {
	t.Parallel()
	f := newFakeHost(t, http.StatusUnauthorized)
	const token = "ghp_supersecret"
	err := NewReporter().Report(context.Background(), Gitea, f.srv.URL+"/org/app", token, Status{Commit: sha, State: StatePending})
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "Bad credentials") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("the token is in the error: %v", err)
	}
	if err := NewReporter().Report(context.Background(), Gitea, f.srv.URL+"/org/app", "", Status{Commit: sha}); err == nil {
		t.Error("reported without a token")
	}
}

func TestReportTimesOut(t *testing.T) {
	t.Parallel()
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	t.Cleanup(func() { close(block); srv.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := NewReporter().Report(ctx, Gitea, srv.URL+"/org/app", "t", Status{Commit: sha, State: StatePending})
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("err = %v after %s", err, time.Since(start))
	}
}

func TestReportTruncatesDescription(t *testing.T) {
	t.Parallel()
	f := newFakeHost(t, http.StatusCreated)
	NewReporter().Report(context.Background(), Gitea, f.srv.URL+"/o/r", "t", Status{Commit: sha, State: StateFailure, Description: strings.Repeat("x", 500)})
	if n := len(f.body["description"]); n > 140 {
		t.Errorf("description is %d characters", n)
	}
}
