package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parthh37/nodehoster/internal/deploy"
	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/preview"
	"github.com/parthh37/nodehoster/internal/secrets"
	"github.com/parthh37/nodehoster/internal/secretstore"
	"github.com/parthh37/nodehoster/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// Preview deployments (model.PreviewConfig). A preview is an ordinary
// site with PreviewOf set, made from its parent's configuration: one
// binding at the preview's host, one instance, the parent's variables
// that are not secrets with the preview overrides and PREVIEW_* on top,
// its own shared folder, no scheduled tasks or deployment slots and one
// release kept. Its configuration is made again from the parent's at
// every deployment, so a change to the parent reaches its previews with
// their next push.
//
// Everything done to one preview (create, deploy, delete) goes through a
// queue of its own, drained by one worker: operations on a preview never
// overlap, and pushes arriving while it deploys collapse into one
// deployment of the latest. At most maxPreviewBuilds previews of the
// whole server deploy at once; the others wait their turn.
//
// Pull requests from forks (and, with requireApproval "all", every pull
// request) are held: the preview is recorded, awaiting approval of its
// head commit, and nothing is fetched until an operator approves that
// commit. Only the approved commit is then built, and a new push needs a
// new approval.

// previewOp is one thing to do to a preview.
type previewOp struct {
	parentID string
	key      string // model.PreviewInfo.Key
	del      bool
	reason   string         // why, for events: "closed", "no push for 7 days"...
	ev       *preview.Event // what to deploy; nil = redeploy what the preview shows
	user     string         // who asked, for the deployment record
	// approved: user approved commit, the head the preview was holding.
	approved bool
	commit   string
}

// maxPreviewBuilds is how many preview deployments run at once on the
// server: a burst of pushes to many branches queues instead of running
// as many installs and builds side by side.
var maxPreviewBuilds = 2

// maxAutoPreviewCerts is how many automatic (Let's Encrypt, per host)
// certificates previews may ask for under one domain in a week. Let's
// Encrypt allows 50 new certificates per registered domain a week, shared
// with the production sites under it; beyond this, a new preview is
// refused and a wildcard certificate is the way (certMode wildcard or
// certificate).
var maxAutoPreviewCerts = 20

const autoPreviewCertsDoc = "previews.autoCerts"

var (
	// ErrNotAwaitingApproval is ApprovePreview's answer for a preview with
	// nothing to approve.
	ErrNotAwaitingApproval = errors.New("the preview is not waiting for approval")
	// ErrAwaitingApproval is RedeployPreview's answer for a preview whose
	// head waits for approval: approving it deploys it.
	ErrAwaitingApproval = errors.New("the preview is waiting for approval")
)

type previewState struct {
	mu      sync.Mutex
	started bool
	closed  bool
	ctx     context.Context // ends at Shutdown
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	pending map[string]*previewOp // parent|key -> the next operation (the latest wins)
	active  map[string]string     // parent|key -> what its worker does now; present while it runs

	admit    sync.Mutex // counts, evicts and creates one preview at a time
	wildcard sync.Mutex // finds or requests a wildcard certificate once
	certs    sync.Mutex // reads and writes the automatic certificate count
	reporter *preview.Reporter
	builds   chan struct{} // a token per running preview deployment
}

var errPreviewsClosed = errors.New("the server is shutting down")

// previewPollInterval is how often a preview deployment waiting for one
// started by hand checks again.
var previewPollInterval = 5 * time.Second

func opKey(parentID, key string) string { return parentID + "|" + key }

// queuePreview schedules an operation on a preview. It replaces one that
// has not started yet: only the latest request matters.
func (c *Core) queuePreview(op *previewOp) error {
	st := &c.previews
	k := opKey(op.parentID, op.key)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.closed {
		return errPreviewsClosed
	}
	if !st.started {
		st.ctx, st.cancel = context.WithCancel(context.Background())
		st.pending, st.active = map[string]*previewOp{}, map[string]string{}
		st.reporter = preview.NewReporter()
		st.builds = make(chan struct{}, max(maxPreviewBuilds, 1))
		st.started = true
	}
	st.pending[k] = op
	if _, running := st.active[k]; running {
		return nil
	}
	st.active[k] = ""
	st.wg.Add(1)
	go c.previewWorker(k)
	return nil
}

func (c *Core) previewWorker(k string) {
	st := &c.previews
	defer st.wg.Done()
	for {
		st.mu.Lock()
		op := st.pending[k]
		delete(st.pending, k)
		if op == nil || st.ctx.Err() != nil {
			delete(st.active, k)
			st.mu.Unlock()
			return
		}
		doing := model.PreviewDeploying
		if op.del {
			doing = model.PreviewDeleting
		}
		st.active[k] = doing
		ctx := st.ctx
		st.mu.Unlock()
		if op.del {
			c.removePreview(ctx, op)
		} else {
			c.deployPreview(ctx, op)
		}
	}
}

// closePreviews stops the preview workers at shutdown. One waiting for a
// deployment leaves it to finish on its own; queued operations are
// dropped (the next push or the expiry sweep catches up).
func (c *Core) closePreviews() {
	st := &c.previews
	st.mu.Lock()
	st.closed = true
	if st.cancel != nil {
		st.cancel()
	}
	st.mu.Unlock()
	st.wg.Wait()
}

// queued is what is happening to a preview: deploying or deleting when
// its worker is at it or has it queued, "" otherwise.
func (c *Core) queued(parentID, key string) string {
	st := &c.previews
	k := opKey(parentID, key)
	st.mu.Lock()
	defer st.mu.Unlock()
	if op := st.pending[k]; op != nil {
		if op.del {
			return model.PreviewDeleting
		}
		if st.active[k] != model.PreviewDeleting {
			return model.PreviewDeploying
		}
	}
	return st.active[k]
}

// ---- listing

// Previews lists a site's previews, the most recently pushed first.
func (c *Core) Previews(parentID string) []*model.Site {
	var out []*model.Site
	for _, s := range c.Sites() {
		if s.PreviewOf == parentID && s.Preview != nil {
			out = append(out, s)
		}
	}
	slices.SortStableFunc(out, func(a, b *model.Site) int { return b.Preview.LastPush.Compare(a.Preview.LastPush) })
	return out
}

// PreviewIDs lists the IDs of a site's previews (none for a preview).
// Access to a site extends to them (see auth.Access.WithPreviews).
func (c *Core) PreviewIDs(parentID string) []string {
	c.cacheMu.RLock()
	defer c.cacheMu.RUnlock()
	var ids []string
	for id, s := range c.sites {
		if s.PreviewOf == parentID && parentID != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func (c *Core) findPreview(parentID, key string) *model.Site {
	for _, s := range c.Previews(parentID) {
		if s.Preview.Key == key {
			return s
		}
	}
	return nil
}

// PreviewView is a preview as the API lists it, with its state: deleting
// or deploying while its worker is at it, otherwise what its last
// deployment says.
func (c *Core) PreviewView(ctx context.Context, s *model.Site) model.PreviewView {
	v := model.PreviewView{ID: s.ID, Name: s.Name, CreatedAt: s.CreatedAt, SiteState: c.Status(s).State}
	if s.Preview != nil {
		v.Preview = *s.Preview
	}
	if list, err := c.Store.ListDeployments(ctx, s.ID, 1); err == nil && len(list) > 0 {
		v.LastDeployment = list[0]
	}
	v.State = c.queued(s.PreviewOf, v.Preview.Key)
	if v.State == "" && v.Preview.AwaitingApproval {
		v.State = model.PreviewAwaitingApproval
	}
	if v.State == "" {
		v.State = model.PreviewPending
		if d := v.LastDeployment; d != nil {
			switch d.Status {
			case "running":
				v.State = model.PreviewDeploying
			case "failed":
				v.State = model.PreviewFailed
			default:
				v.State = model.PreviewReady
			}
		}
	}
	return v
}

// ---- requests

// PreviewWebhook applies a verified webhook delivery to a site's
// previews. A decision that is not Handled leaves the delivery to the
// site's own push handling.
func (c *Core) PreviewWebhook(parent *model.Site, ev *preview.Event) (preview.Decision, error) {
	d := preview.Decide(parent.Deploy.Previews, parent.Deploy.Git.Branch, ev, func(key string) bool {
		return c.findPreview(parent.ID, key) != nil
	})
	if !d.Handled || d.Action == preview.ActionIgnore {
		return d, nil
	}
	op := &previewOp{parentID: parent.ID, key: d.Key, del: d.Action == preview.ActionDelete, reason: ev.Reason, ev: ev, user: "webhook"}
	return d, c.queuePreview(op)
}

// DeployBranchPreview creates or redeploys the preview of a branch on
// request, whatever the branch patterns say (not the production branch).
func (c *Core) DeployBranchPreview(parent *model.Site, branch, user string) (preview.Decision, error) {
	branch = strings.TrimPrefix(strings.TrimSpace(branch), "refs/heads/")
	switch {
	case parent.IsPreview():
		return preview.Decision{}, errors.New("a preview has no previews")
	case !parent.Deploy.Previews.Enabled:
		return preview.Decision{}, &model.ValidationError{Field: "deploy.previews.enabled", Message: "previews are not enabled for this site"}
	case !preview.ValidRef(branch):
		return preview.Decision{}, &model.ValidationError{Field: "branch", Message: fmt.Sprintf("%q is not a branch name", branch)}
	case branch == parent.Deploy.Git.Branch:
		return preview.Decision{}, &model.ValidationError{Field: "branch", Message: "the production branch is the site itself; it has no preview"}
	}
	ev := &preview.Event{Kind: model.PreviewBranch, Branch: branch, Ref: "refs/heads/" + branch, Action: preview.ActionDeploy, Reason: "requested"}
	op := &previewOp{parentID: parent.ID, key: ev.Key(), ev: ev, user: user, reason: "requested by " + user}
	return preview.Decision{Handled: true, Action: preview.ActionDeploy, Key: ev.Key(), Reason: "requested"}, c.queuePreview(op)
}

// RedeployPreview deploys a preview's head again. A preview awaiting
// approval stays so: it has to be approved (ApprovePreview).
func (c *Core) RedeployPreview(p *model.Site, user string) error {
	if !p.IsPreview() || p.Preview == nil {
		return store.ErrNotFound
	}
	if p.Preview.AwaitingApproval {
		return fmt.Errorf("%w: approve commit %s first", ErrAwaitingApproval, shortCommit(p.Preview.Commit))
	}
	return c.queuePreview(&previewOp{parentID: p.PreviewOf, key: p.Preview.Key, user: user, reason: "redeploy requested by " + user})
}

// ApprovePreview approves the commit a preview is holding: it is fetched,
// built and deployed, and only that commit (the pull request's ref is
// checked before anything runs). commit, when set, is the commit the
// operator reviewed: a push since then is refused, to be reviewed too.
func (c *Core) ApprovePreview(p *model.Site, user, commit string) error {
	if !p.IsPreview() || p.Preview == nil {
		return store.ErrNotFound
	}
	info := p.Preview
	if !info.AwaitingApproval {
		return ErrNotAwaitingApproval
	}
	commit = strings.TrimSpace(commit)
	if commit != "" && !sameCommit(commit, info.Commit) {
		return &model.ValidationError{Field: "commit", Message: fmt.Sprintf("the pull request is now at %s: review that commit and approve again", shortCommit(info.Commit))}
	}
	return c.queuePreview(&previewOp{parentID: p.PreviewOf, key: info.Key, user: user, approved: true, commit: info.Commit,
		reason: "approved by " + user})
}

// sameCommit compares commit IDs, either possibly abbreviated.
func sameCommit(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	a, b = strings.ToLower(a), strings.ToLower(b)
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

func shortCommit(c string) string {
	if c == "" {
		return "(unknown)"
	}
	return c[:min(7, len(c))]
}

// needsApproval reports whether a deployment of ev waits for an operator:
// pull requests from forks, and every pull request with
// requireApproval "all".
func needsApproval(cfg model.PreviewConfig, ev *preview.Event) bool {
	return ev.Kind == model.PreviewPR && (ev.Fork || cfg.RequireApproval == model.PreviewApproveAll)
}

// previewRefused says why the settings refuse a deployment ("" = they
// do not). A redeployment is refused as the webhook would be: turning
// forks or pull requests off stops their previews.
func previewRefused(cfg model.PreviewConfig, ev *preview.Event) string {
	switch {
	case !cfg.Enabled:
		return "previews are turned off"
	case ev.Kind == model.PreviewPR && !cfg.PullRequests:
		return "previews of pull requests are turned off"
	case ev.Fork && !cfg.AllowForks:
		return "previews of forks are turned off"
	}
	return ""
}

// RemovePreview deletes a preview with its releases, logs and
// certificate, after anything in progress on it.
func (c *Core) RemovePreview(p *model.Site, user string) error {
	if !p.IsPreview() || p.Preview == nil {
		return store.ErrNotFound
	}
	return c.queuePreview(&previewOp{parentID: p.PreviewOf, key: p.Preview.Key, del: true, user: user, reason: "deleted by " + user})
}

// ---- deploy

func (c *Core) deployPreview(ctx context.Context, op *previewOp) {
	parent, err := c.Site(op.parentID)
	if err != nil {
		return // the parent is gone: its previews go with it
	}
	cfg := parent.Deploy.Previews
	existing := c.findPreview(parent.ID, op.key)
	ev := op.ev
	if ev == nil {
		if existing == nil {
			return
		}
		ev = eventOf(existing.Preview)
	}
	what := describePreview(ev)
	reason := previewRefused(cfg, ev)
	if reason == "" && cfg.Protocol == "http" && parent.RequiresClientCert() && !cfg.BasicAuth.Enabled && len(cfg.AllowIPs) == 0 {
		// Settings saved before this was refused (see validatePreviews).
		reason = "the site requires client certificates, which http previews cannot ask for; protect them with basic authentication or an IP allow list, or use https"
	}
	if reason != "" {
		c.Bus.Warn(events.PreviewFailed, parent.ID, "the preview of %s for %s was not deployed: %s", parent.Name, what, reason)
		if why := disallowedFork(cfg); existing != nil && ev.Fork && why != "" {
			c.queuePreview(&previewOp{parentID: parent.ID, key: op.key, del: true, reason: why, user: "previews"})
		}
		return
	}

	// Approval: hold the push, or deploy exactly the approved commit.
	pin, hold := "", false
	if op.approved {
		if existing == nil || !existing.Preview.AwaitingApproval || !sameCommit(existing.Preview.Commit, op.commit) {
			c.Bus.Warn(events.PreviewApproval, parent.ID, "the approval of %s for %s by %s was not applied: the pull request has moved on since; review it and approve again", shortCommit(op.commit), what, op.user)
			return
		}
		pin = op.commit
	} else if needsApproval(cfg, ev) {
		approved := ""
		if existing != nil {
			approved = existing.Preview.ApprovedCommit
		}
		// The approved commit may be deployed again (a redeploy, a pull
		// request reopened at the same head); anything else waits.
		if !sameCommit(ev.Commit, approved) {
			hold = true
		}
		pin = approved
	}
	adjust := func(info *model.PreviewInfo) {
		info.AwaitingApproval = hold
		if op.approved {
			info.ApprovedCommit, info.ApprovedBy = op.commit, op.user
		}
	}

	var site *model.Site
	if existing == nil {
		site, err = c.createPreview(ctx, parent, ev, adjust)
	} else {
		site, err = c.refreshPreview(ctx, parent, existing.ID, ev, adjust)
	}
	if err != nil {
		verb := "updated"
		if existing == nil {
			verb = "created"
		}
		c.Bus.Error(events.PreviewFailed, parent.ID, "the preview of %s for %s could not be %s: %v", parent.Name, what, verb, err)
		return
	}
	info := *site.Preview
	if hold {
		if existing == nil || !existing.Preview.AwaitingApproval || !sameCommit(existing.Preview.Commit, info.Commit) {
			c.announceApproval(parent, &info, what)
		}
		c.reportPreview(ctx, parent, info, info.Commit, preview.StatePending, "Waiting for an operator to approve the preview")
		return
	}
	c.reportPreview(ctx, parent, info, info.Commit, preview.StatePending, "Deploying the preview")

	// One of the server's few build slots, for the whole deployment.
	select {
	case <-ctx.Done():
		return
	case c.previews.builds <- struct{}{}:
	}
	defer func() { <-c.previews.builds }()

	done := make(chan *model.Deployment, 1)
	for {
		_, err = c.Deploy.DeployRef(context.Background(), site, info.Ref, pin, "preview", op.user, func(d *model.Deployment) { done <- d })
		if !errors.Is(err, deploy.ErrBusy) {
			break
		}
		// A deployment started by hand is running: this one follows it.
		select {
		case <-ctx.Done():
			return
		case <-time.After(previewPollInterval):
		}
	}
	if err != nil {
		c.Bus.Error(events.PreviewFailed, parent.ID, "the preview of %s for %s could not be deployed: %v", parent.Name, what, err)
		c.reportPreview(ctx, parent, info, info.Commit, preview.StateError, "The preview could not be deployed")
		return
	}
	var dep *model.Deployment
	select {
	case <-ctx.Done():
		return
	case dep = <-done:
	}
	commit := cmpOr(dep.Commit, info.Commit)
	if dep.Status != "succeeded" && pin != "" && dep.Commit != "" && !sameCommit(dep.Commit, pin) {
		// The pull request moved on between the approval and the fetch:
		// nothing was built; the new head waits for approval in turn.
		c.Bus.Warn(events.PreviewFailed, parent.ID, "the preview of %s for %s was not deployed: %s", parent.Name, what, strings.TrimLeft(dep.Message, "— "))
		held, err := c.setPreviewInfo(ctx, site.ID, func(p *model.PreviewInfo) { p.AwaitingApproval, p.Commit = true, dep.Commit })
		if err == nil {
			c.announceApproval(parent, held.Preview, what)
			c.reportPreview(ctx, parent, *held.Preview, dep.Commit, preview.StatePending, "Waiting for an operator to approve the preview")
		}
		return
	}
	if dep.Status != "succeeded" {
		// The message is "<commit subject> — <error>", the subject empty
		// when nothing was fetched.
		c.Bus.Error(events.PreviewFailed, parent.ID, "the preview of %s for %s failed to deploy: %s", parent.Name, what, strings.TrimLeft(dep.Message, "— "))
		c.reportPreview(ctx, parent, info, commit, preview.StateFailure, "The preview failed to deploy")
		return
	}
	site, first, err := c.previewDeployed(ctx, site.ID, dep)
	if err != nil {
		c.Bus.Error(events.PreviewFailed, parent.ID, "the preview of %s for %s was deployed but could not be started: %v", parent.Name, what, err)
		c.reportPreview(ctx, parent, info, commit, preview.StateError, "The preview could not be started")
		return
	}
	short := commit[:min(7, len(commit))]
	if first {
		c.Bus.Info(events.PreviewCreated, parent.ID, "preview of %s for %s is up at %s (%s)", parent.Name, what, site.Preview.URL, short)
	} else {
		c.Bus.Info(events.PreviewUpdated, parent.ID, "preview of %s for %s updated to %s at %s", parent.Name, what, short, site.Preview.URL)
	}
	c.reportPreview(ctx, parent, *site.Preview, commit, preview.StateSuccess, "Preview ready")
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// eventOf rebuilds the request that made a preview, to deploy it again.
func eventOf(info *model.PreviewInfo) *preview.Event {
	return &preview.Event{
		Provider: info.Provider, Kind: info.Kind, Action: preview.ActionDeploy, Reason: "redeploy",
		Number: info.Number, Branch: info.Branch, Ref: info.Ref, Commit: info.Commit,
		Title: info.Title, Author: info.Author, URL: info.PRURL, Fork: info.Fork,
	}
}

// describePreview says what a preview shows: "pull request #42 (fix/login)".
func describePreview(ev *preview.Event) string {
	if ev.Kind == model.PreviewPR {
		return fmt.Sprintf("pull request #%d (%s)", ev.Number, ev.Branch)
	}
	return "branch " + ev.Branch
}

// createPreview makes the preview site, first evicting previews beyond
// the site's maximum: those never deployed successfully first (a burst
// of pushes then replaces its own previews rather than working ones),
// then those pushed to least recently. A preview held for approval only
// evicts others held since their creation: unreviewed pull requests
// cannot push out working previews.
func (c *Core) createPreview(ctx context.Context, parent *model.Site, ev *preview.Event, adjust func(*model.PreviewInfo)) (*model.Site, error) {
	st := &c.previews
	st.admit.Lock()
	defer st.admit.Unlock()
	cfg := parent.Deploy.Previews
	info := infoOf(ev, nil)
	adjust(&info)
	var live []*model.Site
	for _, p := range c.Previews(parent.ID) {
		if c.queued(parent.ID, p.Preview.Key) != model.PreviewDeleting {
			live = append(live, p)
		}
	}
	if over := len(live) - max(cfg.MaxPreviews, 1) + 1; over > 0 {
		// Held since created, then never deployed, then the others.
		rank := func(p *model.PreviewInfo) int {
			switch {
			case neverApproved(p):
				return 0
			case !p.Ready:
				return 1
			}
			return 2
		}
		var victims []*model.Site
		for r := range 3 {
			if info.AwaitingApproval && r > 0 {
				break
			}
			for i := len(live) - 1; i >= 0; i-- { // pushed to least recently first
				if rank(live[i].Preview) == r {
					victims = append(victims, live[i])
				}
			}
		}
		if len(victims) < over {
			return nil, fmt.Errorf("%s has %d previews, the most it keeps, and a pull request waiting for approval only replaces others waiting since they were opened: delete a preview or raise maxPreviews", parent.Name, len(live))
		}
		for _, victim := range victims[:over] {
			reason := fmt.Sprintf("evicted for %s: %s has at most %d previews", describePreview(ev), parent.Name, cfg.MaxPreviews)
			if err := c.queuePreview(&previewOp{parentID: parent.ID, key: victim.Preview.Key, del: true, reason: reason, user: "previews"}); err != nil {
				return nil, err
			}
		}
	}
	certID, err := c.previewCertificate(ctx, cfg)
	if err != nil {
		return nil, err
	}
	info.Host = preview.Host(cfg.HostPattern, ev.Kind, ev.Number, ev.Branch, func(h string) bool { return c.hostTaken(h, "") })
	info.URL = preview.URL(cfg.Protocol, info.Host, cfg.Port)
	s := derivePreview(parent, nil, info, certID)
	if err := c.countAutoCert(ctx, cfg, nil, s); err != nil {
		return nil, err
	}
	created, err := c.createSite(ctx, s, false)
	if errors.Is(err, store.ErrDuplicateName) {
		s.Name = preview.Unique(s.Name, info.Key)
		created, err = c.createSite(ctx, s, false)
	}
	return created, err
}

// neverApproved reports whether a preview has been held for approval
// since it was created (it has no binding yet).
func neverApproved(info *model.PreviewInfo) bool {
	return info.AwaitingApproval && info.ApprovedCommit == ""
}

// refreshPreview records a new push and makes the preview's
// configuration again from its parent's.
func (c *Core) refreshPreview(ctx context.Context, parent *model.Site, id string, ev *preview.Event, adjust func(*model.PreviewInfo)) (*model.Site, error) {
	certID, err := c.previewCertificate(ctx, parent.Deploy.Previews)
	if err != nil {
		return nil, err
	}
	c.sitesMu.Lock()
	defer c.sitesMu.Unlock()
	cur, err := c.Site(id)
	if err != nil {
		return nil, err
	}
	info := infoOf(ev, cur.Preview)
	adjust(&info)
	cfg := parent.Deploy.Previews
	info.URL = preview.URL(cfg.Protocol, info.Host, cfg.Port)
	s := derivePreview(parent, cur, info, certID)
	if err := c.countAutoCert(ctx, cfg, cur, s); err != nil {
		return nil, err
	}
	return c.updateSite(ctx, id, s)
}

// setPreviewInfo changes what the server records about a preview.
func (c *Core) setPreviewInfo(ctx context.Context, id string, change func(*model.PreviewInfo)) (*model.Site, error) {
	c.sitesMu.Lock()
	defer c.sitesMu.Unlock()
	cur, err := c.Site(id)
	if err != nil {
		return nil, err
	}
	if cur.Preview == nil {
		return nil, store.ErrNotFound
	}
	s := clone(cur)
	change(s.Preview)
	return c.updateSite(ctx, id, s)
}

// announceApproval tells the operators a preview waits for them.
func (c *Core) announceApproval(parent *model.Site, info *model.PreviewInfo, what string) {
	from := ""
	if info.Fork {
		from = " from a fork"
	}
	by := ""
	if info.Author != "" {
		by = " by " + info.Author
	}
	c.Bus.Warn(events.PreviewApproval, parent.ID, "the preview of %s for %s%s waits for approval of commit %s%s: review the change, then approve it to build it on this server",
		parent.Name, what, from, shortCommit(info.Commit), by)
}

// infoOf is a preview's description after an event: what the event says,
// over what was known (a redeploy or a push event says less than the
// pull request event that created the preview).
func infoOf(ev *preview.Event, old *model.PreviewInfo) model.PreviewInfo {
	var info model.PreviewInfo
	if old != nil {
		info = *old
	}
	info.Key, info.Kind, info.Number, info.Branch, info.Ref = ev.Key(), ev.Kind, ev.Number, ev.Branch, ev.Ref
	info.Fork = ev.Fork
	info.Commit = cmpOr(ev.Commit, info.Commit)
	info.Title = cmpOr(ev.Title, info.Title)
	info.Author = cmpOr(ev.Author, info.Author)
	info.PRURL = cmpOr(ev.URL, info.PRURL)
	info.Provider = cmpOr(ev.Provider, info.Provider)
	if ev.Reason != "redeploy" || info.LastPush.IsZero() {
		info.LastPush = time.Now()
	}
	return info
}

// previewDeployed records a successful deployment on the preview; the
// first one makes it start (and start with the server from then on).
func (c *Core) previewDeployed(ctx context.Context, id string, dep *model.Deployment) (*model.Site, bool, error) {
	c.sitesMu.Lock()
	cur, err := c.Site(id)
	if err != nil {
		c.sitesMu.Unlock()
		return nil, false, err
	}
	s := clone(cur)
	first := !s.Preview.Ready
	s.Preview.Ready = true
	s.Preview.Commit = cmpOr(dep.Commit, s.Preview.Commit)
	s.AutoStart = true
	updated, err := c.updateSite(ctx, id, s)
	c.sitesMu.Unlock()
	if err != nil {
		return nil, false, err
	}
	if first && !c.IsRunning(updated) {
		if err := c.StartSite(id); err != nil {
			return nil, false, err
		}
	}
	return updated, first, nil
}

// derivePreview makes a preview's configuration from its parent's. cur
// is the preview as it is (nil when creating it): its identity, host,
// state and release are kept.
func derivePreview(parent, cur *model.Site, info model.PreviewInfo, certID string) *model.Site {
	cfg := parent.Deploy.Previews
	s := clone(parent)
	label, _, _ := strings.Cut(info.Host, ".")
	s.ID, s.Name = "", preview.SiteName(parent.Name, label)
	s.AutoStart, s.ActiveRelease, s.CreatedAt = false, "", time.Time{}
	if cur != nil {
		s.ID, s.Name, s.AutoStart, s.ActiveRelease, s.CreatedAt = cur.ID, cur.Name, cur.AutoStart, cur.ActiveRelease, cur.CreatedAt
	}
	s.Description = fmt.Sprintf("Preview of %s for %s", parent.Name, describePreview(&preview.Event{Kind: info.Kind, Number: info.Number, Branch: info.Branch}))
	s.PreviewOf, s.Preview = parent.ID, &info

	b := model.Binding{Protocol: cfg.Protocol, IP: cfg.IP, Port: cfg.Port, Host: info.Host}
	if cur != nil && len(cur.Bindings) > 0 {
		b.ID = cur.Bindings[0].ID
	}
	if b.Protocol == "https" {
		b.CertMode = model.CertModeAuto
		if certID != "" {
			b.CertMode, b.CertificateID = model.CertModeManual, certID
		}
		// The parent's clients need a certificate: so do the preview's.
		b.ClientCert = parent.PreviewClientCert(cfg.IP, cfg.Port)
	}
	s.Bindings = []model.Binding{b}
	if neverApproved(&info) {
		// Nothing to serve, and no certificate to ask for, before an
		// operator has looked at the pull request.
		s.Bindings = nil
	}
	s.Tasks = nil // scheduled tasks would run against the preview's data twice
	s.Slots = nil // a preview is one deployment: none of the parent's slots

	// The parent's firewall mode, as it is in effect: a site from before
	// the firewall has none, which is off; new-site defaults do not apply.
	if s.Routing.WAF.Mode == "" {
		s.Routing.WAF.Mode = model.WAFOff
	}

	d := &s.Deploy
	d.Git.Branch = info.Branch
	d.WebhookSecret = "" // pushes reach a preview through its parent's webhook
	d.KeepReleases = model.PreviewKeepReleases
	d.Previews = model.PreviewConfig{}

	r := &s.Routing
	r.Maintenance.Enabled = false
	r.HTTPSRedirect = false // the preview has one binding: nothing to redirect to
	if cfg.Protocol != "https" {
		r.HSTS.Enabled = false
	}
	if cfg.BasicAuth.Enabled {
		r.BasicAuth = cfg.BasicAuth
		r.BasicAuth.Users = slices.Clone(cfg.BasicAuth.Users)
		r.BasicAuth.ExcludePaths = slices.Clone(cfg.BasicAuth.ExcludePaths)
	}
	if len(cfg.AllowIPs) > 0 {
		r.IP.Allow = slices.Clone(cfg.AllowIPs)
	}
	if n := s.Node; n != nil {
		n.Instances, n.PortMode, n.FixedPort = 1, "auto", 0
		n.LoadBalancer.Enabled = false
		if isAbsPath(n.AppRoot) {
			n.AppRoot = "." // the release, once deployed (see Site.ResolveRoot)
		}
		n.Env = previewEnv(n.Env, cfg.Env, info, cfg.InheritSecrets && !info.Fork)
	}
	if st := s.Static; st != nil && isAbsPath(st.Root) {
		st.Root = "."
	}
	return s
}

func isAbsPath(p string) bool {
	return filepath.IsAbs(p) || strings.HasPrefix(p, `\\`) || len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}

// previewEnv is the parent's variables, the preview overrides on top and
// then PREVIEW, PREVIEW_BRANCH, PREVIEW_PR and PREVIEW_URL. Names match
// regardless of case, as Windows environment variables do. Production's
// secrets stay in production unless inherit is set: secret variables,
// those read from a secret store and slot settings are left out (the
// overrides give previews their own).
func previewEnv(base, overrides []model.EnvVar, info model.PreviewInfo, inherit bool) []model.EnvVar {
	var out []model.EnvVar
	for _, e := range base {
		if inherit || !e.Secret && e.From == nil && !e.SlotSetting {
			out = append(out, e)
		}
	}
	set := func(e model.EnvVar) {
		for i := range out {
			if strings.EqualFold(out[i].Name, e.Name) {
				out[i] = e
				return
			}
		}
		out = append(out, e)
	}
	for _, e := range overrides {
		set(e)
	}
	pr := ""
	if info.Kind == model.PreviewPR {
		pr = strconv.Itoa(info.Number)
	}
	set(model.EnvVar{Name: "PREVIEW", Value: "1"})
	set(model.EnvVar{Name: "PREVIEW_BRANCH", Value: info.Branch})
	set(model.EnvVar{Name: "PREVIEW_PR", Value: pr})
	set(model.EnvVar{Name: "PREVIEW_URL", Value: info.URL})
	return out
}

// hostTaken reports whether a site other than except has a binding for
// host.
func (c *Core) hostTaken(host, except string) bool {
	for _, s := range c.Sites() {
		if s.ID == except {
			continue
		}
		if s.Preview != nil && strings.EqualFold(s.Preview.Host, host) {
			return true // a preview waiting for approval has no binding yet
		}
		for _, b := range s.Bindings {
			if strings.EqualFold(b.Host, host) {
				return true
			}
		}
	}
	return false
}

// ---- certificates

// previewCertificate is the certificate preview bindings use: "" for a
// per-host automatic one (and for http), the chosen one, or the wildcard
// certificate for the pattern's suffix, found or requested.
func (c *Core) previewCertificate(ctx context.Context, cfg model.PreviewConfig) (string, error) {
	if cfg.Protocol != "https" {
		return "", nil
	}
	switch cfg.CertMode {
	case model.PreviewCertManual:
		return cfg.CertificateID, nil
	case model.PreviewCertWildcard:
		_, suffix := model.SplitHostPattern(cfg.HostPattern)
		return c.ensureWildcard(ctx, suffix, cfg.DNSProviderID)
	}
	return "", nil
}

// countAutoCert enforces maxAutoPreviewCerts: a preview binding that is
// to ask Let's Encrypt for a certificate of its own (https, certMode auto,
// a host that had none) counts against its domain's week, and beyond the
// limit the preview is refused rather than risk the production sites'
// certificates. The count survives restarts.
func (c *Core) countAutoCert(ctx context.Context, cfg model.PreviewConfig, cur, s *model.Site) error {
	if len(s.Bindings) == 0 || s.Bindings[0].Protocol != "https" || s.Bindings[0].CertMode != model.CertModeAuto {
		return nil
	}
	if cur != nil && len(cur.Bindings) > 0 && cur.Bindings[0].Protocol == "https" && cur.Bindings[0].CertMode == model.CertModeAuto &&
		strings.EqualFold(cur.Bindings[0].Host, s.Bindings[0].Host) {
		return nil // it has its certificate already
	}
	_, domain := model.SplitHostPattern(cfg.HostPattern)
	st := &c.previews
	st.certs.Lock()
	defer st.certs.Unlock()
	ledger := map[string][]time.Time{}
	if err := c.Store.GetDoc(ctx, autoPreviewCertsDoc, &ledger); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	now := time.Now()
	for d, list := range ledger {
		list = slices.DeleteFunc(list, func(t time.Time) bool { return now.Sub(t) > 7*24*time.Hour })
		if len(list) == 0 {
			delete(ledger, d)
		} else {
			ledger[d] = list
		}
	}
	if n := len(ledger[domain]); n >= maxAutoPreviewCerts {
		return fmt.Errorf("previews under %s have asked Let's Encrypt for %d certificates in the last 7 days, the most NodeHoster asks for previews (Let's Encrypt allows 50 a week per registered domain, shared with the sites under it): use a wildcard certificate for previews (certificate mode wildcard or certificate)", domain, n)
	}
	ledger[domain] = append(ledger[domain], now)
	return c.Store.PutDoc(ctx, autoPreviewCertsDoc, ledger)
}

// ensureWildcard returns a certificate for *.suffix: one the server has
// (a valid one first), or a new one obtained through DNS-01 and renewed
// automatically. It is not deleted with the previews: the next ones use it.
func (c *Core) ensureWildcard(ctx context.Context, suffix, dnsProviderID string) (string, error) {
	st := &c.previews
	st.wildcard.Lock()
	defer st.wildcard.Unlock()
	want := "*." + suffix
	list, err := c.Store.ListCertificates(ctx)
	if err != nil {
		return "", err
	}
	best, rank := "", 0
	for _, cert := range list {
		if !slices.Contains(cert.Domains, want) {
			continue
		}
		r := 1
		switch cert.Status {
		case "valid":
			r = 3
		case "pending":
			r = 2
		}
		if r > rank {
			best, rank = cert.ID, r
		}
	}
	if best != "" {
		return best, nil
	}
	cert, err := c.Certs.RequestACME(ctx, "Previews "+want, []string{want},
		model.ACMEOptions{Challenge: "dns-01", DNSProviderID: dnsProviderID}, true)
	if err != nil {
		return "", fmt.Errorf("wildcard certificate for %s: %w", want, err)
	}
	c.Log.Info("requesting a wildcard certificate for previews", "domain", want)
	return cert.ID, nil
}

// dropPreviewCertificates deletes the automatic certificates of a deleted
// preview's hosts, unless another site still has a binding for the host.
func (c *Core) dropPreviewCertificates(ctx context.Context, p *model.Site) {
	var hosts []string
	for _, b := range p.Bindings {
		if b.Protocol == "https" && b.CertMode == model.CertModeAuto && b.Host != "" && !c.hostTaken(b.Host, p.ID) {
			hosts = append(hosts, b.Host)
		}
	}
	if len(hosts) == 0 {
		return
	}
	list, err := c.Store.ListCertificates(ctx)
	if err != nil {
		c.Log.Warn("preview certificates were not deleted", "site", p.Name, "err", err)
		return
	}
	for _, cert := range list {
		if cert.Managed && len(cert.Domains) == 1 && slices.Contains(hosts, cert.Domains[0]) {
			if err := c.Certs.Delete(ctx, cert.ID); err != nil {
				c.Log.Warn("delete preview certificate", "host", cert.Domains[0], "err", err)
			}
		}
	}
}

// ---- delete

func (c *Core) removePreview(ctx context.Context, op *previewOp) {
	p := c.findPreview(op.parentID, op.key)
	if p == nil {
		return
	}
	if err := c.DeleteSite(context.WithoutCancel(ctx), p.ID, true); err != nil {
		c.Bus.Error(events.PreviewFailed, op.parentID, "the preview %s of %s could not be deleted: %v", p.Preview.URL, c.siteName(op.parentID), err)
		return
	}
	c.Bus.Info(events.PreviewDeleted, op.parentID, "preview of %s at %s deleted (%s)", c.siteName(op.parentID), p.Preview.URL, op.reason)
}

// previewSiteDeleted tidies up after DeleteSite: a preview's automatic
// certificate goes with it, and a deleted parent takes its previews.
func (c *Core) previewSiteDeleted(s *model.Site) {
	if s.IsPreview() {
		c.dropPreviewCertificates(context.Background(), s)
	}
	for _, p := range c.Previews(s.ID) {
		c.queuePreview(&previewOp{parentID: s.ID, key: p.Preview.Key, del: true, reason: "its site was deleted", user: "previews"})
	}
}

// previewLoop deletes expired previews, and previews whose parent is
// gone (a shutdown in the middle of deleting it), hourly.
func (c *Core) previewLoop(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		c.expirePreviews(time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// disallowedFork says why settings no longer allow previews of forks'
// pull requests ("" = they do).
func disallowedFork(cfg model.PreviewConfig) string {
	switch {
	case !cfg.Enabled:
		return "previews turned off"
	case !cfg.PullRequests:
		return "previews of pull requests turned off"
	case !cfg.AllowForks:
		return "forks disabled"
	}
	return ""
}

// dropDisallowedForks deletes a site's fork previews once its settings no
// longer allow them (called when the site is saved): the forks' code
// stops running at once, not at their next push.
func (c *Core) dropDisallowedForks(parent *model.Site) {
	reason := disallowedFork(parent.Deploy.Previews)
	if reason == "" || parent.IsPreview() {
		return
	}
	for _, p := range c.Previews(parent.ID) {
		if p.Preview.Fork && c.queued(parent.ID, p.Preview.Key) != model.PreviewDeleting {
			c.queuePreview(&previewOp{parentID: parent.ID, key: p.Preview.Key, del: true, reason: reason, user: "previews"})
		}
	}
}

func (c *Core) expirePreviews(now time.Time) {
	for _, s := range c.Sites() {
		if !s.IsPreview() || s.Preview == nil {
			continue
		}
		reason := ""
		if parent, err := c.Site(s.PreviewOf); err != nil {
			reason = "its site no longer exists"
		} else if why := disallowedFork(parent.Deploy.Previews); s.Preview.Fork && why != "" {
			reason = why
		} else if days := parent.Deploy.Previews.ExpireDays; days > 0 && now.Sub(s.Preview.LastPush) > time.Duration(days)*24*time.Hour {
			reason = fmt.Sprintf("expired: no push for %d days", days)
		}
		if reason != "" && c.queued(s.PreviewOf, s.Preview.Key) == "" {
			c.queuePreview(&previewOp{parentID: s.PreviewOf, key: s.Preview.Key, del: true, reason: reason, user: "previews"})
		}
	}
}

// ---- status reports

// reportPreview sets a commit status on the git host, when the parent
// asks for it and the preview came from a host NodeHoster knows. A
// failure is logged: it must not fail the preview.
func (c *Core) reportPreview(ctx context.Context, parent *model.Site, info model.PreviewInfo, commit, state, desc string) {
	cfg := parent.Deploy.Previews
	if !cfg.ReportStatus || info.Provider == "" || commit == "" {
		return
	}
	sealed := cfg.StatusToken
	if sealed == "" {
		sealed = parent.Deploy.Git.Token
	}
	token, err := c.Box.Unseal(sealed)
	if err == nil && token == "" && cfg.StatusToken == "" && parent.Deploy.Git.TokenFrom != nil {
		// The git token, held in a secret store.
		ref := *parent.Deploy.Git.TokenFrom
		var vals map[model.SecretRef]string
		if vals, err = c.Secrets.Resolve(c.secretsCtx(), []model.SecretRef{ref}, secretstore.ResolveOptions{}); err == nil {
			token = vals[ref]
		}
	}
	if err != nil || token == "" {
		c.Log.Warn("preview status not reported: no usable token; set one in the site's preview settings", "site", parent.Name, "err", err)
		return
	}
	st := preview.Status{Commit: commit, State: state, Description: desc}
	if state == preview.StateSuccess {
		st.TargetURL = info.URL
	}
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := c.previews.reporter.Report(rctx, info.Provider, parent.Deploy.Git.Repo, token, st); err != nil {
		c.Log.Warn("preview status report failed", "site", parent.Name, "preview", info.Key, "err", err)
	}
}

// ---- configuration

// preparePreviews checks the preview settings against the server and
// seals their secrets, as prepare does for the site's own: masked values
// keep the stored ones, basic auth passwords are hashed.
func (c *Core) preparePreviews(in *model.Site, ex *model.Site) error {
	p, old := &in.Deploy.Previews, ex.Deploy.Previews
	for i := range p.Env {
		e := &p.Env[i]
		if !e.Secret {
			if e.Value == secrets.Mask {
				e.Value = ""
			}
			continue
		}
		if e.Value == secrets.Mask {
			e.Value = ""
			for _, o := range old.Env {
				if o.Name == e.Name && o.Secret {
					e.Value = o.Value
				}
			}
			continue
		}
		sealed, err := c.Box.Seal(e.Value)
		if err != nil {
			return err
		}
		e.Value = sealed
	}
	switch p.StatusToken {
	case secrets.Mask:
		p.StatusToken = old.StatusToken
	case "":
	default:
		sealed, err := c.Box.Seal(p.StatusToken)
		if err != nil {
			return err
		}
		p.StatusToken = sealed
	}
	for i := range p.BasicAuth.Users {
		u := &p.BasicAuth.Users[i]
		if u.Password != "" {
			h, err := bcrypt.GenerateFromPassword([]byte(u.Password), bcrypt.DefaultCost)
			if err != nil {
				return err
			}
			u.PasswordHash, u.Password = string(h), ""
			continue
		}
		// Never a hash sent by a client: the stored one of that user.
		u.PasswordHash = ""
		for _, o := range old.BasicAuth.Users {
			if o.Username == u.Username {
				u.PasswordHash = o.PasswordHash
			}
		}
		if u.PasswordHash == "" {
			return &model.ValidationError{Field: fmt.Sprintf("deploy.previews.basicAuth.users[%d].password", i), Message: "set a password"}
		}
	}
	if !p.Enabled || p.Protocol != "https" {
		return nil
	}
	_, suffix := model.SplitHostPattern(p.HostPattern)
	switch p.CertMode {
	case model.PreviewCertManual:
		cert, err := c.Store.GetCertificate(context.Background(), p.CertificateID)
		if err != nil {
			return &model.ValidationError{Field: "deploy.previews.certificateId", Message: "the selected certificate does not exist"}
		}
		if !slices.Contains(cert.Domains, "*."+suffix) {
			return &model.ValidationError{Field: "deploy.previews.certificateId", Message: fmt.Sprintf("the certificate does not cover *.%s", suffix)}
		}
	case model.PreviewCertWildcard:
		found := false
		for _, d := range c.Settings().DNSProviders {
			found = found || d.ID == p.DNSProviderID
		}
		if !found {
			return &model.ValidationError{Field: "deploy.previews.dnsProviderId", Message: "the selected DNS provider does not exist"}
		}
	}
	return nil
}

// maskPreviews hides the preview settings' secrets for the API.
func maskPreviews(s *model.Site) {
	p := &s.Deploy.Previews
	for i := range p.Env {
		if p.Env[i].Secret && p.Env[i].Value != "" {
			p.Env[i].Value = secrets.Mask
		}
	}
	if p.StatusToken != "" {
		p.StatusToken = secrets.Mask
	}
	for i := range p.BasicAuth.Users {
		p.BasicAuth.Users[i].PasswordHash = ""
		p.BasicAuth.Users[i].Password = ""
	}
}

// previewStartable refuses to start a preview before its first release:
// its application folder only means something inside one.
func previewStartable(s *model.Site) error {
	if s.IsPreview() && s.ActiveRelease == "" {
		return errors.New("the preview has not been deployed yet; redeploy it")
	}
	return nil
}

// keepPreviewFields stops a site update from the API making a site a
// preview, or changing what a preview is: only the server sets them.
func (c *Core) keepPreviewFields(id string, in *model.Site) {
	in.PreviewOf, in.Preview = "", nil
	if cur, err := c.Site(id); err == nil && cur.Preview != nil {
		info := *cur.Preview
		in.PreviewOf, in.Preview = cur.PreviewOf, &info
	}
}
