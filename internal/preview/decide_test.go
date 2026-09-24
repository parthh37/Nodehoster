package preview

import (
	"strings"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
)

func TestDecide(t *testing.T) {
	t.Parallel()
	on := model.PreviewConfig{Enabled: true, PullRequests: true, Branches: []string{"feature/*"}}
	pr := func(action string, fork bool) *Event {
		return &Event{Kind: model.PreviewPR, Number: 42, Branch: "fix", Action: action, Reason: action, Fork: fork}
	}
	branch := func(name, action string) *Event {
		return &Event{Kind: model.PreviewBranch, Branch: name, Action: action, Reason: "pushed"}
	}
	none := func(string) bool { return false }
	some := func(string) bool { return true }
	cases := []struct {
		name        string
		cfg         model.PreviewConfig
		ev          *Event
		exists      func(string) bool
		handled     bool
		wantAction  string
		wantReasonS string
	}{
		{"nil event", on, nil, none, false, "", ""},
		{"pr opened", on, pr(ActionDeploy, false), none, true, ActionDeploy, ""},
		{"pr from fork refused", on, pr(ActionDeploy, true), none, true, ActionIgnore, "fork"},
		{"pr from fork allowed", func() model.PreviewConfig { c := on; c.AllowForks = true; return c }(), pr(ActionDeploy, true), none, true, ActionDeploy, ""},
		{"pr labeled", on, pr(ActionIgnore, false), none, true, ActionIgnore, "nothing to deploy"},
		{"pr previews off", model.PreviewConfig{Enabled: true, Branches: []string{"x"}}, pr(ActionDeploy, false), none, true, ActionIgnore, "turned off"},
		{"previews disabled", model.PreviewConfig{}, pr(ActionDeploy, false), none, true, ActionIgnore, "not enabled"},
		// Closing still removes a preview after previews were turned off.
		{"pr closed, previews off", model.PreviewConfig{}, pr(ActionDelete, false), some, true, ActionDelete, ""},
		{"pr closed, no preview", on, pr(ActionDelete, false), none, true, ActionIgnore, "no preview"},
		{"push to production", on, branch("main", ActionDeploy), none, false, "", ""},
		{"production deleted", on, branch("main", ActionDelete), none, true, ActionIgnore, "production"},
		{"push to previewed branch", on, branch("feature/x", ActionDeploy), none, true, ActionDeploy, ""},
		{"push to other branch", on, branch("develop", ActionDeploy), none, false, "", ""},
		{"push with previews off", model.PreviewConfig{Branches: []string{"feature/*"}}, branch("feature/x", ActionDeploy), none, false, "", ""},
		{"previewed branch deleted", on, branch("feature/x", ActionDelete), some, true, ActionDelete, ""},
		{"previewed branch deleted, no preview", on, branch("feature/x", ActionDelete), none, true, ActionIgnore, "no preview"},
		{"unpreviewed branch deleted", on, branch("develop", ActionDelete), none, false, "", ""},
		// Patterns changed since: the preview still goes with its branch.
		{"old preview's branch deleted", on, branch("develop", ActionDelete), some, true, ActionDelete, ""},
	}
	for _, c := range cases {
		d := Decide(c.cfg, "main", c.ev, c.exists)
		if d.Handled != c.handled || d.Action != c.wantAction {
			t.Errorf("%s: decision = %+v, want handled=%v action=%q", c.name, d, c.handled, c.wantAction)
			continue
		}
		if c.wantReasonS != "" && !strings.Contains(d.Reason, c.wantReasonS) {
			t.Errorf("%s: reason %q lacks %q", c.name, d.Reason, c.wantReasonS)
		}
		if c.handled && c.ev != nil && d.Key != c.ev.Key() {
			t.Errorf("%s: key %q", c.name, d.Key)
		}
	}
}
