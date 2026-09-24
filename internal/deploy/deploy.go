// Package deploy builds releases from a zip upload or a git repository,
// runs install/build commands, and activates them. Each release lives in its
// own folder so a rollback is just activating an older one.
package deploy

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/procmgr"
	"github.com/parthh37/nodehoster/internal/secrets"
	"github.com/parthh37/nodehoster/internal/store"
)

var ErrBusy = errors.New("a deployment is already running for this site")

type Options struct {
	Store    *store.Store
	Box      *secrets.Box
	Log      *slog.Logger
	Bus      *events.Bus
	SitesDir string
	Settings func() model.Settings
	// ResolveNode finds the runtime used for install and build commands.
	ResolveNode func(version string) (procmgr.NodeRuntime, error)
	// ResolveRuntime finds the runtime of a site that is not Node.js
	// (bun, deno, python, dotnet; "" version = the server default).
	ResolveRuntime func(runtime, version string) (procmgr.RuntimeExe, error)
	// Activate points the site at a release and applies it (a rolling
	// recycle for Node.js sites).
	Activate func(ctx context.Context, siteID, release string) error
	// InUse lists releases of a site still used by something other than
	// its processes, such as a scheduled task run that started before a
	// deployment: pruning keeps them. Optional.
	InUse func(siteID string) []string
	// FindGit locates git for git deployments; nil looks on PATH.
	FindGit func() (string, error)
	// SecretEnv reads the site's variables that come from secret stores
	// (by name) and SecretToken a git token that does; optional.
	SecretEnv   func(site *model.Site, vars []model.EnvVar) (map[string]string, error)
	SecretToken func(site *model.Site, ref model.SecretRef) (string, error)
	// ActivateSlot points a deployment slot at a release (slots.go).
	ActivateSlot func(ctx context.Context, siteID, slot, release string) error
	// OnFinish, optional, is told of every deployment that ended, after
	// the site is free for the next one (auto-swap starts from it).
	OnFinish func(site *model.Site, dep *model.Deployment)
}

type Deployer struct {
	opts Options

	mu      sync.Mutex
	running map[string]string // siteID -> deployment id
	logs    map[string]*depLog

	// runAsAccount runs a command as a site's run-as account
	// (procmgr.RunAs); tests, which cannot log on as another account,
	// replace it.
	runAsAccount func(ctx context.Context, cmd *exec.Cmd, runAs model.RunAsConfig, password, siteDir string) error
}

func New(opts Options) *Deployer {
	return &Deployer{opts: opts, running: map[string]string{}, logs: map[string]*depLog{}, runAsAccount: procmgr.RunAs}
}

// depLog is a deployment's output: written to a file and streamed live.
type depLog struct {
	mu      sync.Mutex
	f       *os.File
	path    string
	written int64 // bytes in the file so far
	subs    map[chan string]struct{}
	done    chan struct{}
}

func (l *depLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n, err := l.f.Write(p)
	l.written += int64(n)
	s := string(p)
	for ch := range l.subs {
		select {
		case ch <- s:
		default:
		}
	}
	return n, err
}

func (l *depLog) printf(format string, a ...any) {
	fmt.Fprintf(l, "[%s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, a...))
}

func (d *Deployer) logPath(siteID, depID string) string {
	return filepath.Join(d.opts.SitesDir, siteID, "deploy-logs", depID+".log")
}

// Log returns a deployment's full log.
func (d *Deployer) Log(siteID, depID string) ([]byte, error) {
	return os.ReadFile(d.logPath(siteID, depID))
}

// subscribe adds a live subscriber and returns how many bytes of the file
// were written before it: those are not sent to it, everything after is.
func (l *depLog) subscribe() (ch chan string, offset int64, cancel func()) {
	ch = make(chan string, 256)
	l.mu.Lock()
	l.subs[ch] = struct{}{}
	offset = l.written
	l.mu.Unlock()
	return ch, offset, func() {
		l.mu.Lock()
		delete(l.subs, ch)
		l.mu.Unlock()
	}
}

// Subscribe follows a running deployment: backlog is its output so far
// and lines what it writes next, each chunk in exactly one of them (the
// backlog is read up to where the subscription starts, so a line written
// in between is neither sent twice nor lost). The done channel closes
// when it finishes. It returns ok=false when the deployment is not
// running: its log file (Log) is then complete.
func (d *Deployer) Subscribe(depID string) (backlog []byte, lines <-chan string, done <-chan struct{}, cancel func(), ok bool) {
	d.mu.Lock()
	l := d.logs[depID]
	d.mu.Unlock()
	if l == nil {
		return nil, nil, nil, func() {}, false
	}
	ch, offset, cancel := l.subscribe()
	return readPrefix(l.path, offset), ch, l.done, cancel, true
}

// readPrefix reads the first n bytes of a file. The file only grows, so
// they do not change while it is being written to.
func readPrefix(path string, n int64) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, n)
	m, _ := io.ReadFull(f, buf)
	return buf[:m]
}

func newReleaseID() string {
	b := make([]byte, 3)
	rand.Read(b)
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

// begin reserves the site and creates the deployment record and log.
func (d *Deployer) begin(ctx context.Context, site *model.Site, source, user string) (*model.Deployment, *depLog, error) {
	d.mu.Lock()
	if holder, busy := d.running[site.ID]; busy {
		d.mu.Unlock()
		return nil, nil, busyError(holder)
	}
	id := newReleaseID()
	d.running[site.ID] = id
	d.mu.Unlock()

	dep := &model.Deployment{
		ID: id, SiteID: site.ID, Source: source, Status: "running", StartedAt: time.Now(), User: user,
		ReleaseDir: model.ReleaseDir(d.opts.SitesDir, site.ID, id), Slot: site.Slot,
	}
	lp := d.logPath(site.ID, id)
	os.MkdirAll(filepath.Dir(lp), 0o750)
	f, err := os.Create(lp)
	if err != nil {
		d.finish(site.ID, id)
		return nil, nil, err
	}
	l := &depLog{f: f, path: lp, subs: map[chan string]struct{}{}, done: make(chan struct{})}
	d.mu.Lock()
	d.logs[id] = l
	d.mu.Unlock()
	if err := d.opts.Store.PutDeployment(ctx, dep); err != nil {
		f.Close()
		d.finish(site.ID, id)
		return nil, nil, err
	}
	return dep, l, nil
}

func (d *Deployer) finish(siteID, depID string) {
	d.mu.Lock()
	delete(d.running, siteID)
	l := d.logs[depID]
	delete(d.logs, depID)
	d.mu.Unlock()
	if l != nil {
		close(l.done)
		l.f.Close()
	}
}

// DeployZip deploys an uploaded archive. The zip file is consumed (removed).
func (d *Deployer) DeployZip(ctx context.Context, site *model.Site, zipPath, user string) (*model.Deployment, error) {
	dep, l, err := d.begin(ctx, site, "zip", user)
	if err != nil {
		os.Remove(zipPath)
		return nil, err
	}
	snapshot := *dep // the worker keeps updating dep; callers get it as started
	go d.run(site, dep, l, func() error {
		defer os.Remove(zipPath)
		l.printf("extracting archive")
		n, err := extractZip(zipPath, dep.ReleaseDir)
		if err != nil {
			return err
		}
		l.printf("extracted %d files", n)
		return nil
	})
	return &snapshot, nil
}

// DeployGit clones the configured repository.
func (d *Deployer) DeployGit(ctx context.Context, site *model.Site, branch, source, user string) (*model.Deployment, error) {
	g := site.Deploy.Git
	if g.Repo == "" {
		return nil, errors.New("no git repository is configured for this site")
	}
	if branch == "" {
		branch = g.Branch
	}
	git, err := d.findGit()
	if err != nil {
		return nil, err
	}
	dep, l, err := d.begin(ctx, site, source, user)
	if err != nil {
		return nil, err
	}
	token := d.opts.Box.MustUnseal(g.Token)
	snapshot := *dep // the worker keeps updating dep; callers get it as started
	go d.run(site, dep, l, func() error {
		if g.TokenFrom != nil {
			l.printf("reading the token from secret store %q", g.TokenFrom.Store)
			t, err := d.secretToken(site, *g.TokenFrom)
			if err != nil {
				return err
			}
			token = t
		}
		args := []string{"clone", "--depth", "1", "--single-branch"}
		if branch != "" {
			args = append(args, "--branch", branch)
		}
		args = append(args, g.Repo, dep.ReleaseDir)
		l.printf("git clone %s%s", redact(g.Repo), map[bool]string{true: " (" + branch + ")", false: ""}[branch != ""])
		env := gitEnv(token)
		if err := runCmd(context.Background(), l, "", env, git, args...); err != nil {
			return fmt.Errorf("git clone failed: %w", err)
		}
		out, _ := exec.Command(git, "-C", dep.ReleaseDir, "log", "-1", "--pretty=%H%n%s").Output()
		if parts := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2); len(parts) == 2 {
			dep.Commit, dep.Message = parts[0], parts[1]
			l.printf("commit %s: %s", dep.Commit[:min(12, len(dep.Commit))], dep.Message)
		}
		os.RemoveAll(filepath.Join(dep.ReleaseDir, ".git"))
		return nil
	})
	return &snapshot, nil
}

func (d *Deployer) findGit() (string, error) {
	if d.opts.FindGit != nil {
		return d.opts.FindGit()
	}
	if p, err := exec.LookPath("git"); err == nil {
		return p, nil
	}
	return "", errors.New("git is not installed on the server; install it with: nodehoster deps install git")
}

// gitEnv is the environment NodeHoster runs git in. git never asks for
// anything: not on a terminal (GIT_TERMINAL_PROMPT), and not through a
// credential helper, which the machine's git configuration may name (Git
// for Windows sets Git Credential Manager, which can wait for someone to
// sign in); credentials are the site's token or nothing. The token
// travels in an HTTP header set through git's environment config, so it
// never appears in the process list or in the repository's .git/config.
func gitEnv(token string) []string {
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never")
	var keys [][2]string
	if token != "" {
		basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
		keys = append(keys, [2]string{"http.extraHeader", "Authorization: Basic " + basic})
	}
	keys = append(keys, [2]string{"credential.helper", ""}) // an empty value clears the configured helpers
	env = append(env, "GIT_CONFIG_COUNT="+strconv.Itoa(len(keys)))
	for i, kv := range keys {
		n := strconv.Itoa(i)
		env = append(env, "GIT_CONFIG_KEY_"+n+"="+kv[0], "GIT_CONFIG_VALUE_"+n+"="+kv[1])
	}
	return env
}

func redact(repo string) string {
	u, err := url.Parse(repo)
	if err != nil || u.User == nil {
		return repo
	}
	u.User = url.User("***")
	return u.String()
}

// run executes a deployment: fetch, link shared paths, install, build,
// activate, prune.
func (d *Deployer) run(site *model.Site, dep *model.Deployment, l *depLog, fetch func() error) {
	ctx := context.Background()
	defer d.finished(site, dep) // after finish: the site is free again
	defer d.finish(site.ID, dep.ID)
	// The release active before this deployment keeps serving while the
	// rolling recycle drains it, so pruning must not remove it.
	previous := ""
	if cur, err := d.opts.Store.GetSite(ctx, site.ID); err == nil {
		previous = cur.ReleaseIn(site.Slot)
	}
	err := d.steps(ctx, site, dep, l, fetch)
	now := time.Now()
	dep.FinishedAt = &now
	if err != nil {
		l.printf("FAILED: %v", err)
		dep.Status = "failed"
		dep.Message = strings.TrimSpace(dep.Message + " — " + err.Error())
		os.RemoveAll(dep.ReleaseDir)
		dep.ReleaseDir = ""
		d.opts.Bus.Error(events.DeployFailed, site.ID, "deployment of %s failed: %v", site.Name, err)
	} else {
		l.printf("deployment succeeded in %s", now.Sub(dep.StartedAt).Round(time.Second))
		dep.Status = "succeeded"
		d.opts.Bus.Info(events.DeploySucceeded, site.ID, "%s deployed (%s)", site.Name, dep.ID)
	}
	d.opts.Store.PutDeployment(ctx, dep)
	if err == nil {
		d.prune(ctx, site, previous)
	}
}

func (d *Deployer) steps(ctx context.Context, site *model.Site, dep *model.Deployment, l *depLog, fetch func() error) error {
	if err := os.MkdirAll(filepath.Dir(dep.ReleaseDir), 0o750); err != nil {
		return err
	}
	// The service fills the release; a run-as account, which can change
	// what is in the site's folder, cannot reach it until the commands
	// that run as that account (see createClosedRelease).
	ra, asAccount := runAs(site)
	if asAccount {
		if err := createClosedRelease(dep.ReleaseDir); err != nil {
			return fmt.Errorf("create the release folder: %w", err)
		}
	}
	if err := fetch(); err != nil {
		return err
	}
	if err := d.linkShared(site, dep.ReleaseDir, l); err != nil {
		return err
	}
	workDir := dep.ReleaseDir
	if site.RunsNode() && site.Node.AppRoot != "" && !filepath.IsAbs(site.Node.AppRoot) {
		workDir = filepath.Join(dep.ReleaseDir, site.Node.AppRoot)
	}
	env, err := d.commandEnv(site)
	if err != nil {
		return err
	}
	if asAccount {
		if err := openRelease(dep.ReleaseDir); err != nil {
			return fmt.Errorf("give %s access to the release: %w", ra.Username, err)
		}
		// The service's temporary folder may be closed to the account.
		tmp := filepath.Join(d.opts.SitesDir, site.ID, ".tmp")
		if err := os.MkdirAll(tmp, 0o750); err != nil {
			return err
		}
		env = append(env, "TEMP="+tmp, "TMP="+tmp, "TMPDIR="+tmp)
		l.printf("commands run as %s", ra.Username)
	} else if strings.TrimSpace(site.Deploy.InstallCommand+site.Deploy.BuildCommand) != "" {
		// Static sites have no run-as setting: their builds always run as
		// the service (README, "Who runs the install and build commands").
		why := "the site has no run-as account"
		if site.Type == model.SiteStatic {
			why = "static sites have no run-as account"
		}
		l.printf("commands run as the NodeHoster service (%s)", why)
	}
	env, python, err := d.prepareVenv(ctx, site, workDir, env, l)
	if err != nil {
		return err
	}
	for _, step := range []struct{ name, cmd string }{
		{"install", site.Deploy.InstallCommand},
		{"build", site.Deploy.BuildCommand},
	} {
		if strings.TrimSpace(step.cmd) == "" {
			continue
		}
		if msg := installSkipMessage(site, workDir); step.name == "install" && msg != "" {
			l.printf("%s", msg)
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		cmd, shown := shellCommand(cctx, step.cmd), step.cmd
		if c := isolatedInstall(cctx, site, step.cmd, python); step.name == "install" && c != nil {
			cmd, shown = c, strings.Join(c.Args, " ")
		}
		l.printf("%s: %s", step.name, shown)
		err := d.siteCommand(cctx, site, l, workDir, env, cmd)
		cancel()
		if err != nil {
			return fmt.Errorf("%s command failed: %w", step.name, err)
		}
	}
	l.printf("activating release %s", dep.ID)
	if err := d.activate(ctx, site, dep.ID); err != nil {
		return fmt.Errorf("activate: %w", err)
	}
	return nil
}

// commandEnv is the environment for install/build commands: the service's
// environment, the site's node and git on PATH (npm fetches git
// dependencies with it), the site's own runtime ahead of them when it is
// not Node.js, and the site's variables.
func (d *Deployer) commandEnv(site *model.Site) ([]string, error) {
	env := os.Environ()
	if git, err := d.findGit(); err == nil {
		env = prependPath(env, filepath.Dir(git))
	}
	version := ""
	if site.RunsNode() {
		version = site.Node.NodeVersion
	}
	if version == "" {
		version = d.opts.Settings().DefaultNodeVersion
	}
	// Node.js is on PATH for every site when there is one (a Python site
	// may still build its front end with npm); only Node.js sites need it.
	rt, err := d.opts.ResolveNode(version)
	if err == nil {
		env = prependPath(env, filepath.Dir(rt.Exe))
	} else if siteRuntime(site) == model.RuntimeNode {
		return nil, err
	}
	if env, err = d.runtimeEnv(site, env); err != nil {
		return nil, err
	}
	env = append(env, "npm_config_cache="+filepath.Join(d.opts.SitesDir, site.ID, ".npm-cache"),
		"npm_config_update_notifier=false", "CI=true")
	if site.RunsNode() {
		fromStore, err := d.secretEnv(site, site.Node.Env)
		if err != nil {
			return nil, err
		}
		for _, e := range site.Node.Env {
			v := e.Value
			if e.From != nil {
				v = fromStore[e.Name]
			} else if e.Secret {
				v = d.opts.Box.MustUnseal(v)
			}
			env = append(env, e.Name+"="+v)
		}
	}
	return env, nil
}

func prependPath(env []string, dir string) []string {
	for i, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.EqualFold(k, "PATH") {
			env[i] = k + "=" + dir + string(os.PathListSeparator) + v
			return env
		}
	}
	return append(env, "PATH="+dir)
}

// linkShared makes each shared path point into the site's shared folder, so
// files like .env and folders like uploads survive deployments. On the first
// deployment, content shipped in the release seeds the shared copy.
//
// The service does this, in folders a site's run-as account can change:
// it has Modify access to the site's folder, shared\ included. Had the
// account put a junction or link where a shared path (or a folder on the
// way to it) is, the service would create files and folders, or move the
// release's files, wherever that points. So every step goes through the site's
// folder opened as an os.Root, which never leaves it (on Windows each
// name is opened with OBJ_DONT_REPARSE, and a junction, whose target is
// absolute, counts as leaving), and a link or junction found on the way is
// refused rather than followed within the folder.
func (d *Deployer) linkShared(site *model.Site, release string, l *depLog) error {
	if len(site.Deploy.SharedPaths) == 0 {
		return nil
	}
	siteDir := filepath.Join(d.opts.SitesDir, site.ID)
	relRelease, err := filepath.Rel(siteDir, release)
	if err != nil || !filepath.IsLocal(relRelease) {
		return fmt.Errorf("release %s is not in the site's folder", release)
	}
	root, err := os.OpenRoot(siteDir)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, p := range site.Deploy.SharedPaths {
		p = filepath.Clean(filepath.FromSlash(strings.TrimSpace(p)))
		if p == "." || !filepath.IsLocal(p) || strings.HasPrefix(p, "..") {
			return fmt.Errorf("shared path %q must be relative to the application", p)
		}
		if err := linkSharedPath(root, siteDir, relRelease, p, l); err != nil {
			return fmt.Errorf("shared path %s: %w", filepath.ToSlash(p), err)
		}
	}
	return nil
}

// linkSharedPath links the shared path p of a release; root is the site's
// folder, and release the release's folder in it.
func linkSharedPath(root *os.Root, siteDir, release, p string, l *depLog) error {
	src := filepath.Join("shared", p)
	dst := filepath.Join(release, p)
	if err := realFolders(root, src); err != nil {
		return err
	}
	if err := realFolders(root, dst); err != nil {
		return err
	}
	fi, err := root.Lstat(src)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := root.MkdirAll(filepath.Dir(src), 0o750); err != nil {
			return err
		}
		// A link shipped in the release is not what seeds the shared copy.
		if fi, err := root.Lstat(dst); err == nil && (fi.Mode().IsRegular() || fi.IsDir()) {
			if err := root.Rename(dst, src); err != nil {
				return err
			}
			l.printf("shared: seeded %s from this release", p)
		} else if strings.Contains(filepath.Base(p), ".") { // looks like a file
			// O_EXCL: what appeared there since is not written through.
			f, err := root.OpenFile(src, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
			if err != nil {
				return err
			}
			f.Close()
		} else if err := root.Mkdir(src, 0o750); err != nil {
			return err
		}
	case err != nil:
		return err
	case !fi.Mode().IsRegular() && !fi.IsDir():
		return notFollowed(src)
	}
	if err := root.RemoveAll(dst); err != nil {
		return err
	}
	if err := root.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	if err := link(root, siteDir, src, dst); err != nil {
		return fmt.Errorf("link: %w", err)
	}
	l.printf("shared: %s", p)
	return nil
}

// realFolders checks that the folders on the way to name, in root, that
// exist are folders, not links or junctions (which Lstat reports as
// irregular files on Windows).
func realFolders(root *os.Root, name string) error {
	parts := strings.Split(filepath.Dir(name), string(filepath.Separator))
	for i := range parts { // from the top: what is below a link is not looked up
		dir := filepath.Join(parts[:i+1]...)
		if dir == "." {
			return nil
		}
		fi, err := root.Lstat(dir)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return notFollowed(dir)
		}
	}
	return nil
}

func notFollowed(name string) error {
	return fmt.Errorf("%s in the site's folder is a link, junction or other special file, which deployments do not follow: replace it with a plain file or folder", filepath.ToSlash(name))
}

// link makes dst point at src, both in root (the site's folder at
// siteDir): a symbolic link, made by handle in root and relative, so that
// it resolves in the site's folder wherever that is; else, on Windows,
// where making one takes a privilege that the service has but a developer
// running NodeHoster unelevated may not, a junction for a folder, or a
// copy of a file.
func link(root *os.Root, siteDir, src, dst string) error {
	target, err := filepath.Rel(filepath.Dir(dst), src)
	if err != nil {
		return err
	}
	if err := root.Symlink(target, dst); err == nil {
		return nil
	}
	st, err := root.Stat(src)
	if err != nil {
		return err
	}
	if runtime.GOOS == "windows" && st.IsDir() {
		// Directory junctions need no special privilege. mklink goes by
		// path, which the checks above leave as the service's own.
		return exec.Command("cmd", "/d", "/c", "mklink", "/J", filepath.Join(siteDir, dst), filepath.Join(siteDir, src)).Run()
	}
	// Last resort for files: copy.
	data, err := root.ReadFile(src)
	if err != nil {
		return err
	}
	return root.WriteFile(dst, data, st.Mode())
}

// Activate switches a site to an existing release (rollback).
func (d *Deployer) Activate(ctx context.Context, site *model.Site, depID string) (*model.Deployment, error) {
	dep, err := d.opts.Store.GetDeployment(ctx, depID)
	if err != nil || dep.SiteID != site.ID {
		return nil, store.ErrNotFound
	}
	if dep.Status != "succeeded" || dep.ReleaseDir == "" {
		return nil, errors.New("only successful deployments that have not been pruned can be activated")
	}
	if _, err := os.Stat(dep.ReleaseDir); err != nil {
		return nil, errors.New("the release folder no longer exists")
	}
	if err := d.activate(ctx, site, dep.ID); err != nil {
		return nil, err
	}
	d.opts.Bus.Info(events.DeploySucceeded, site.ID, "%s rolled back to %s", site.Name, dep.ID)
	return dep, nil
}

// prune deletes releases beyond KeepReleases, never the active one, the
// one that was active before the latest deployment (its processes may
// still be draining) or one a task run in progress uses. Those in use are
// deleted by a later deployment.
func (d *Deployer) prune(ctx context.Context, site *model.Site, previous string) {
	current, err := d.opts.Store.GetSite(ctx, site.ID)
	if err != nil {
		return
	}
	var inUse []string
	if d.opts.InUse != nil {
		inUse = d.opts.InUse(site.ID)
	}
	keep := max(current.Deploy.KeepReleases, 1)
	list, err := d.opts.Store.ListDeployments(ctx, site.ID, 1000)
	if err != nil {
		return
	}
	sort.Slice(list, func(i, j int) bool { return list[i].StartedAt.After(list[j].StartedAt) })
	kept := 0
	for _, dep := range list {
		if dep.ReleaseDir == "" {
			continue
		}
		if dep.ID == current.ActiveRelease || dep.ID == previous || kept < keep {
			kept++
			continue
		}
		if slices.Contains(inUse, dep.ID) {
			continue
		}
		os.RemoveAll(dep.ReleaseDir)
		dep.ReleaseDir = ""
		d.opts.Store.PutDeployment(ctx, dep)
	}
	// Keep the history bounded too.
	if len(list) > 100 {
		for _, dep := range list[100:] {
			if dep.ID != current.ActiveRelease && dep.ID != previous {
				d.opts.Store.DeleteDeployment(ctx, dep.ID)
				os.Remove(d.logPath(site.ID, dep.ID))
			}
		}
	}
}

// ---- helpers

func runCmd(ctx context.Context, out io.Writer, dir string, env []string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, env, out, out
	cmd.WaitDelay = 10 * time.Second
	hideWindow(cmd)
	return cmd.Run()
}

// runAs is the run-as account of a site that has one: its instances and
// its deployments' commands run as that account.
func runAs(site *model.Site) (model.RunAsConfig, bool) {
	if site.RunsNode() && site.Node.RunAs.Enabled {
		return site.Node.RunAs, true
	}
	return model.RunAsConfig{}, false
}

// siteCommand runs a command of a site's deployment in dir: the virtual
// environment's creation, the install and build commands. They run the
// application's own code (package scripts, build targets, setup.py), so
// for a site with a run-as account they run as that account, like its
// instances, in a Job Object; the caches they use in the site's folder are
// the account's to change, and must not be trusted by SYSTEM. Without one
// they run as the service, as the site's instances do.
func (d *Deployer) siteCommand(ctx context.Context, site *model.Site, out io.Writer, dir string, env []string, cmd *exec.Cmd) error {
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, env, out, out
	cmd.WaitDelay = 10 * time.Second
	if ra, ok := runAs(site); ok {
		return d.runAsAccount(ctx, cmd, ra, d.opts.Box.MustUnseal(ra.Password), filepath.Join(d.opts.SitesDir, site.ID))
	}
	return cmd.Run()
}

// program is the command running a program with arguments, without a
// console window.
func program(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	hideWindow(cmd)
	return cmd
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// extractZip unpacks an archive, stripping a single top-level folder if the
// archive has one (the usual shape of a zipped project folder).
func extractZip(src, dest string) (int, error) {
	r, err := zip.OpenReader(src)
	if err != nil {
		return 0, fmt.Errorf("open zip: %w", err)
	}
	defer r.Close()
	strip := commonRoot(r.File)
	dest, _ = filepath.Abs(dest)
	if err := os.MkdirAll(dest, 0o750); err != nil {
		return 0, err
	}
	n := 0
	for _, f := range r.File {
		name := strings.TrimPrefix(strings.ReplaceAll(f.Name, "\\", "/"), strip)
		if name == "" || strings.HasPrefix(name, "__MACOSX/") {
			continue
		}
		p := filepath.Join(dest, filepath.FromSlash(name))
		if !strings.HasPrefix(p, dest+string(os.PathSeparator)) {
			return n, fmt.Errorf("archive entry %q escapes the destination", f.Name)
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(p, 0o750)
			continue
		}
		if f.Mode()&os.ModeSymlink != 0 {
			continue // symlinks from uploads are not trusted
		}
		os.MkdirAll(filepath.Dir(p), 0o750)
		rc, err := f.Open()
		if err != nil {
			return n, err
		}
		w, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640|(f.Mode()&0o111))
		if err != nil {
			rc.Close()
			return n, err
		}
		_, err = io.Copy(w, rc)
		rc.Close()
		w.Close()
		if err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func commonRoot(files []*zip.File) string {
	root := ""
	for _, f := range files {
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if strings.HasPrefix(name, "__MACOSX/") {
			continue
		}
		i := strings.IndexByte(name, '/')
		if i < 0 {
			return "" // a file at the top level
		}
		top := name[:i+1]
		if root == "" {
			root = top
		} else if root != top {
			return ""
		}
	}
	return root
}
