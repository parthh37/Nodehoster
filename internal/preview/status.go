package preview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Commit status states, as GitHub names them. GitLab's names are mapped.
const (
	StatePending = "pending"
	StateSuccess = "success"
	StateFailure = "failure"
	StateError   = "error"
)

// StatusContext is the name the status is shown under in the pull
// request.
const StatusContext = "nodehoster/preview"

// Status is one commit status report.
type Status struct {
	Commit      string
	State       string
	Description string
	TargetURL   string // the preview's address
}

// Reporter sets commit statuses on the git host, so that a pull request
// shows its preview's state with a link to it.
type Reporter struct {
	Client *http.Client
}

// NewReporter returns a reporter whose requests time out after 15 seconds.
func NewReporter() *Reporter {
	return &Reporter{Client: &http.Client{Timeout: 15 * time.Second}}
}

// repoPath splits a repository URL into its scheme, host and path
// ("org/app"): https://host/org/app.git, ssh://git@host:22/org/app.git and
// scp-like git@host:org/app.git. The API is reached over https unless the
// repository itself is plain http.
func repoPath(repo string) (scheme, host, path string, err error) {
	repo = strings.TrimSpace(repo)
	if !strings.Contains(repo, "://") {
		// scp-like: [user@]host:path
		at := strings.LastIndex(repo, "@")
		h, p, ok := strings.Cut(repo[at+1:], ":")
		if !ok || h == "" {
			return "", "", "", fmt.Errorf("cannot tell the git host from %q", redactURL(repo))
		}
		scheme, host, path = "https", h, p
	} else {
		u, perr := url.Parse(repo)
		if perr != nil || u.Host == "" {
			return "", "", "", fmt.Errorf("cannot tell the git host from %q", redactURL(repo))
		}
		scheme, host, path = "https", u.Host, u.Path
		switch u.Scheme {
		case "http":
			scheme = "http"
		case "ssh", "git":
			host = u.Hostname() // the SSH port is not the API's
		}
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if strings.Count(path, "/") < 1 {
		return "", "", "", fmt.Errorf("cannot tell the repository from %q", redactURL(repo))
	}
	return scheme, host, path, nil
}

// StatusURL is the commit status endpoint for a commit of a repository.
// It is derived from the site's configured repository, never from the
// webhook's payload, so the token only ever goes to the host the site
// deploys from. GitHub Enterprise is https://<host>/api/v3; GitLab and
// Gitea installed under a sub-path are not supported.
func StatusURL(provider, repo, commit string) (string, error) {
	if !isHex(commit) {
		return "", fmt.Errorf("%q is not a commit", commit)
	}
	scheme, host, path, err := repoPath(repo)
	if err != nil {
		return "", err
	}
	switch provider {
	case GitHub:
		api := scheme + "://" + host + "/api/v3"
		if strings.EqualFold(host, "github.com") {
			api = "https://api.github.com"
		}
		return api + "/repos/" + escapePath(path) + "/statuses/" + commit, nil
	case GitLab:
		return scheme + "://" + host + "/api/v4/projects/" + url.PathEscape(path) + "/statuses/" + commit, nil
	case Gitea:
		return scheme + "://" + host + "/api/v1/repos/" + escapePath(path) + "/statuses/" + commit, nil
	}
	return "", fmt.Errorf("commit statuses are not supported for %q", provider)
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

func isHex(s string) bool {
	if len(s) < 7 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

// Report sets a commit status. The token is sent only to the endpoint
// StatusURL derives from repo and never appears in an error.
func (r *Reporter) Report(ctx context.Context, provider, repo, token string, st Status) error {
	if token == "" {
		return errors.New("no token to report the status with")
	}
	endpoint, err := StatusURL(provider, repo, st.Commit)
	if err != nil {
		return err
	}
	desc := st.Description
	if len(desc) > 140 { // GitHub's limit
		desc = desc[:137] + "..."
	}
	state := st.State
	body := map[string]string{"state": state, "description": desc, "context": StatusContext}
	if st.TargetURL != "" {
		body["target_url"] = st.TargetURL
	}
	if provider == GitLab {
		switch state {
		case StatePending:
			state = "running"
		case StateFailure, StateError:
			state = "failed"
		}
		body["state"], body["name"] = state, StatusContext
		delete(body, "context")
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "NodeHoster-Preview")
	switch provider {
	case GitHub:
		req.Header.Set("Authorization", "Bearer "+token)
	case GitLab:
		req.Header.Set("PRIVATE-TOKEN", token)
	case Gitea:
		req.Header.Set("Authorization", "token "+token)
	}
	resp, err := r.Client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return fmt.Errorf("%s: %w", provider, ue.Err)
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 300 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	var m struct {
		Message string `json:"message"`
	}
	text := strings.TrimSpace(string(msg))
	if json.Unmarshal(msg, &m) == nil && m.Message != "" {
		text = m.Message
	}
	return fmt.Errorf("%s answered HTTP %d: %s", provider, resp.StatusCode, text)
}

// redactURL hides credentials a repository URL may carry.
func redactURL(repo string) string {
	u, err := url.Parse(repo)
	if err != nil || u.User == nil {
		if at := strings.LastIndex(repo, "@"); at >= 0 && !strings.Contains(repo, "://") {
			return "***" + repo[at:]
		}
		return repo
	}
	u.User = url.User("***")
	return u.String()
}
