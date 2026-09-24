package preview

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/parthh37/nodehoster/internal/model"
)

// Git hosts, as found in the webhook's headers.
const (
	GitHub = "github"
	GitLab = "gitlab"
	Gitea  = "gitea" // and Gogs and Forgejo, which send the same events
)

// What a delivery asks of a preview.
const (
	ActionDeploy = "deploy" // create the preview or deploy its new head
	ActionDelete = "delete" // closed, merged, branch deleted
	ActionIgnore = "ignore" // a pull request event that changes no code (labels, title...)
)

// Event is a webhook delivery read as a preview request.
type Event struct {
	Provider string `json:"provider"`
	Kind     string `json:"kind"` // model.PreviewPR | model.PreviewBranch
	Action   string `json:"action"`
	// Reason is the host's word for what happened: opened, synchronize,
	// closed, merged, pushed, branch deleted...
	Reason string `json:"reason"`
	Number int    `json:"number,omitempty"`
	Branch string `json:"branch"` // the pull request's head branch, or the pushed branch
	// Ref is what to fetch from the site's repository: the head branch,
	// or the host's pull request ref for a fork (whose branch is not in
	// the site's repository).
	Ref    string `json:"ref,omitempty"`
	Commit string `json:"commit,omitempty"`
	Title  string `json:"title,omitempty"`
	Author string `json:"author,omitempty"`
	URL    string `json:"url,omitempty"` // the pull request's page
	Fork   bool   `json:"fork,omitempty"`
}

// Key is the preview this event is about.
func (e *Event) Key() string { return Key(e.Kind, e.Number, e.Branch) }

// Provider names the git host that sent a webhook, from its headers ("" =
// unknown, such as a script calling the webhook with ?secret=). Gitea
// sends GitHub's and Gogs' headers too, so it is checked first.
func Provider(h http.Header) string {
	switch {
	case h.Get("X-Gitea-Event") != "", h.Get("X-Gogs-Event") != "", h.Get("X-Forgejo-Event") != "":
		return Gitea
	case h.Get("X-Gitlab-Event") != "":
		return GitLab
	case h.Get("X-GitHub-Event") != "":
		return GitHub
	}
	return ""
}

func eventName(provider string, h http.Header) string {
	switch provider {
	case Gitea:
		for _, k := range []string{"X-Gitea-Event", "X-Forgejo-Event", "X-Gogs-Event"} {
			if v := h.Get(k); v != "" {
				return v
			}
		}
	case GitLab:
		return h.Get("X-Gitlab-Event")
	case GitHub:
		return h.Get("X-GitHub-Event")
	}
	return ""
}

const zeroSHA = "0000000000000000000000000000000000000000"

// IsPush reports whether a delivery is a push, which the site's own push
// handling answers when it is not a preview matter (or cannot be read as
// one).
func IsPush(h http.Header) bool {
	p := Provider(h)
	name := strings.ToLower(eventName(p, h))
	return name == "push" || (p == GitLab && name == "push hook")
}

// Parse reads a webhook delivery. It returns nil (and no error) for
// deliveries that are not about a pull request or a branch: a ping, a
// tag, an issue comment, or a caller that did not say which host it is.
// A push to any branch is returned (Kind branch); whether it concerns a
// preview is for Decide.
func Parse(h http.Header, body []byte) (*Event, error) {
	provider := Provider(h)
	if provider == "" {
		return nil, nil
	}
	name := strings.ToLower(eventName(provider, h))
	var ev *Event
	var err error
	switch {
	case provider == GitLab:
		ev, err = parseGitLab(body)
	case name == "pull_request":
		ev, err = parsePullRequest(body)
	case name == "push":
		ev, err = parsePush(body)
	case name == "delete":
		ev, err = parseDelete(body)
	}
	if ev == nil || err != nil {
		return nil, err
	}
	ev.Provider = provider
	if ev.Branch != "" && !ValidRef(ev.Branch) {
		return nil, fmt.Errorf("branch name %q is not accepted", ev.Branch)
	}
	return ev, nil
}

type repoRef struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
}

// parsePullRequest reads GitHub's and Gitea's pull_request events, which
// share their shape.
func parsePullRequest(body []byte) (*Event, error) {
	var p struct {
		Action      string `json:"action"`
		Number      int    `json:"number"`
		PullRequest struct {
			Number  int    `json:"number"`
			Title   string `json:"title"`
			HTMLURL string `json:"html_url"`
			Merged  bool   `json:"merged"`
			User    struct {
				Login string `json:"login"`
			} `json:"user"`
			Head struct {
				Ref    string   `json:"ref"`
				SHA    string   `json:"sha"`
				RepoID int64    `json:"repo_id"` // Gitea
				Repo   *repoRef `json:"repo"`
			} `json:"head"`
			Base struct {
				Ref    string   `json:"ref"`
				RepoID int64    `json:"repo_id"`
				Repo   *repoRef `json:"repo"`
			} `json:"base"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("pull request event: %w", err)
	}
	pr := p.PullRequest
	n := p.Number
	if n == 0 {
		n = pr.Number
	}
	if n <= 0 {
		return nil, fmt.Errorf("pull request event without a number")
	}
	ev := &Event{
		Kind: model.PreviewPR, Number: n, Branch: pr.Head.Ref, Commit: pr.Head.SHA,
		Title: pr.Title, Author: pr.User.Login, URL: pr.HTMLURL, Reason: p.Action,
	}
	// A pull request is from a fork when its head is in another
	// repository. A deleted fork has no head repository: when it cannot
	// be told, assume the worst.
	headID, baseID := pr.Head.RepoID, pr.Base.RepoID
	var headName, baseName string
	if r := pr.Head.Repo; r != nil {
		headID, headName = cmp.Or(headID, r.ID), r.FullName
	}
	if r := pr.Base.Repo; r != nil {
		baseID, baseName = cmp.Or(baseID, r.ID), r.FullName
	}
	switch {
	case headID != 0 && baseID != 0:
		ev.Fork = headID != baseID
	case headName != "" && baseName != "":
		ev.Fork = !strings.EqualFold(headName, baseName)
	default:
		ev.Fork = true
	}
	ev.Ref = "refs/heads/" + ev.Branch
	if ev.Fork {
		ev.Ref = "refs/pull/" + strconv.Itoa(n) + "/head"
	}
	switch p.Action {
	case "opened", "reopened", "synchronize", "synchronized", "ready_for_review":
		ev.Action = ActionDeploy
	case "closed":
		ev.Action = ActionDelete
		if pr.Merged {
			ev.Reason = "merged"
		}
	default:
		ev.Action = ActionIgnore
	}
	return ev, nil
}

// parsePush reads a GitHub or Gitea push. A deleted branch comes as a
// push to the zero commit.
func parsePush(body []byte) (*Event, error) {
	var p struct {
		Ref        string `json:"ref"`
		After      string `json:"after"`
		Deleted    bool   `json:"deleted"`
		HeadCommit *struct {
			Message string `json:"message"`
		} `json:"head_commit"`
		Pusher struct {
			Name     string `json:"name"`
			Login    string `json:"login"`
			Username string `json:"username"`
		} `json:"pusher"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("push event: %w", err)
	}
	branch, ok := strings.CutPrefix(p.Ref, "refs/heads/")
	if !ok {
		return nil, nil // a tag
	}
	ev := &Event{Kind: model.PreviewBranch, Branch: branch, Ref: p.Ref, Commit: p.After, Action: ActionDeploy, Reason: "pushed"}
	ev.Author = firstNonEmpty(p.Pusher.Login, p.Pusher.Username, p.Pusher.Name)
	if p.HeadCommit != nil {
		ev.Title = firstLine(p.HeadCommit.Message)
	}
	if p.Deleted || p.After == zeroSHA {
		ev.Action, ev.Reason, ev.Commit = ActionDelete, "branch deleted", ""
	}
	return ev, nil
}

// parseDelete reads GitHub's and Gitea's delete event (a branch or tag
// was deleted).
func parseDelete(body []byte) (*Event, error) {
	var p struct {
		Ref     string `json:"ref"`
		RefType string `json:"ref_type"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("delete event: %w", err)
	}
	if p.RefType != "branch" {
		return nil, nil
	}
	branch := strings.TrimPrefix(p.Ref, "refs/heads/")
	return &Event{Kind: model.PreviewBranch, Branch: branch, Ref: "refs/heads/" + branch, Action: ActionDelete, Reason: "branch deleted"}, nil
}

// parseGitLab reads GitLab's Merge Request Hook and Push Hook.
func parseGitLab(body []byte) (*Event, error) {
	var p struct {
		ObjectKind  string `json:"object_kind"`
		Ref         string `json:"ref"`
		After       string `json:"after"`
		CheckoutSHA string `json:"checkout_sha"`
		UserName    string `json:"user_username"`
		User        struct {
			Username string `json:"username"`
		} `json:"user"`
		Commits []struct {
			Message string `json:"message"`
		} `json:"commits"`
		Attrs struct {
			IID             int    `json:"iid"`
			Action          string `json:"action"`
			Title           string `json:"title"`
			URL             string `json:"url"`
			SourceBranch    string `json:"source_branch"`
			SourceProjectID int64  `json:"source_project_id"`
			TargetProjectID int64  `json:"target_project_id"`
			OldRev          string `json:"oldrev"`
			LastCommit      struct {
				ID string `json:"id"`
			} `json:"last_commit"`
		} `json:"object_attributes"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("GitLab event: %w", err)
	}
	switch p.ObjectKind {
	case "push":
		branch, ok := strings.CutPrefix(p.Ref, "refs/heads/")
		if !ok {
			return nil, nil
		}
		ev := &Event{Kind: model.PreviewBranch, Branch: branch, Ref: p.Ref, Commit: firstNonEmpty(p.CheckoutSHA, p.After),
			Author: p.UserName, Action: ActionDeploy, Reason: "pushed"}
		if n := len(p.Commits); n > 0 {
			ev.Title = firstLine(p.Commits[n-1].Message)
		}
		if p.After == zeroSHA {
			ev.Action, ev.Reason, ev.Commit = ActionDelete, "branch deleted", ""
		}
		return ev, nil
	case "merge_request":
		a := p.Attrs
		if a.IID <= 0 {
			return nil, fmt.Errorf("merge request event without an IID")
		}
		ev := &Event{
			Kind: model.PreviewPR, Number: a.IID, Branch: a.SourceBranch, Commit: a.LastCommit.ID,
			Title: a.Title, Author: p.User.Username, URL: a.URL, Reason: a.Action,
			Fork: a.SourceProjectID != 0 && a.TargetProjectID != 0 && a.SourceProjectID != a.TargetProjectID,
		}
		ev.Ref = "refs/heads/" + ev.Branch
		if ev.Fork {
			ev.Ref = "refs/merge-requests/" + strconv.Itoa(a.IID) + "/head"
		}
		switch a.Action {
		case "open", "reopen":
			ev.Action = ActionDeploy
		case "update":
			// An update without oldrev changed the title, labels or
			// assignees, not the code.
			ev.Action = ActionIgnore
			if a.OldRev != "" {
				ev.Action, ev.Reason = ActionDeploy, "pushed"
			}
		case "close":
			ev.Action, ev.Reason = ActionDelete, "closed"
		case "merge":
			ev.Action, ev.Reason = ActionDelete, "merged"
		default:
			ev.Action = ActionIgnore
		}
		return ev, nil
	}
	return nil, nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(s)
}
