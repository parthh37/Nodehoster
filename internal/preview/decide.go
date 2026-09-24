package preview

import "github.com/parthh37/nodehoster/internal/model"

// Decision is what a webhook delivery means for a site's previews.
type Decision struct {
	// Handled is false when the delivery is not a preview matter: a push
	// to the production branch, or to a branch that is not previewed. The
	// site's usual push handling applies to it.
	Handled bool   `json:"-"`
	Action  string `json:"action"` // ActionDeploy | ActionDelete | ActionIgnore
	Key     string `json:"key,omitempty"`
	Reason  string `json:"reason"`
}

// Decide applies a site's preview settings to a delivery. production is
// the site's own branch, which never gets a preview; exists says whether
// the site has a preview for a key, so that closing a pull request or
// deleting a branch still removes a preview after previews were turned
// off or the branch patterns changed.
func Decide(cfg model.PreviewConfig, production string, ev *Event, exists func(key string) bool) Decision {
	if ev == nil {
		return Decision{}
	}
	key := ev.Key()
	d := Decision{Handled: true, Key: key, Reason: ev.Reason}
	ignore := func(reason string) Decision {
		return Decision{Handled: true, Action: ActionIgnore, Key: key, Reason: reason}
	}
	switch ev.Kind {
	case model.PreviewPR:
		// A pull request event never deploys the site itself.
		switch {
		case ev.Action == ActionDelete:
			if !exists(key) {
				return ignore("pull request " + ev.Reason + ": it has no preview")
			}
			d.Action = ActionDelete
		case !cfg.Enabled:
			return ignore("previews are not enabled for this site")
		case !cfg.PullRequests:
			return ignore("previews of pull requests are turned off")
		case ev.Action == ActionIgnore:
			return ignore("pull request " + ev.Reason + ": nothing to deploy")
		case ev.Fork && !cfg.AllowForks:
			return ignore("the pull request comes from a fork: previews of forks are turned off, their code would run on this server")
		default:
			d.Action = ActionDeploy
		}
		return d
	case model.PreviewBranch:
		if ev.Branch == production {
			if ev.Action == ActionDelete {
				return ignore("the production branch was deleted")
			}
			return Decision{}
		}
		previewed := cfg.Enabled && MatchBranch(cfg.Branches, ev.Branch)
		switch {
		case ev.Action == ActionDelete && exists(key):
			d.Action = ActionDelete
		case ev.Action == ActionDelete && previewed:
			return ignore("branch deleted: it has no preview")
		case ev.Action == ActionDeploy && previewed:
			d.Action = ActionDeploy
		default:
			return Decision{}
		}
		return d
	}
	return Decision{}
}
