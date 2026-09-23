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
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
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
	// Activate points the site at a release and applies it (a rolling
	// recycle for Node.js sites).
	Activate func(ctx context.Context, siteID, release string) error
}

type Deployer struct {
	opts Options

	mu      sync.Mutex
	running map[string]string // siteID -> deployment id
	logs    map[string]*depLog
}

func New(opts Options) *Deployer {
	return &Deployer{opts: opts, running: map[string]string{}, logs: map[string]*depLog{}}
}

// depLog is a deployment's output: written to a file and streamed live.
type depLog struct {
	mu   sync.Mutex
	f    *os.File
	subs map[chan string]struct{}
	done chan struct{}
}

func (l *depLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n, err := l.f.Write(p)
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

// Subscribe streams a running deployment's output. The done channel closes
// when it finishes. It returns ok=false when the deployment is not running.
func (d *Deployer) Subscribe(depID string) (lines <-chan string, done <-chan struct{}, cancel func(), ok bool) {
	d.mu.Lock()
	l := d.logs[depID]
	d.mu.Unlock()
	if l == nil {
		return nil, nil, func() {}, false
	}
	ch := make(chan string, 256)
	l.mu.Lock()
	l.subs[ch] = struct{}{}
	l.mu.Unlock()
	return ch, l.done, func() {
		l.mu.Lock()
		delete(l.subs, ch)
		l.mu.Unlock()
	}, true
}

func newReleaseID() string {
	b := make([]byte, 3)
	rand.Read(b)
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

// begin reserves the site and creates the deployment record and log.
func (d *Deployer) begin(ctx context.Context, site *model.Site, source, user string) (*model.Deployment, *depLog, error) {
	d.mu.Lock()
	if _, busy := d.running[site.ID]; busy {
		d.mu.Unlock()
		return nil, nil, ErrBusy
	}
	id := newReleaseID()
	d.running[site.ID] = id
	d.mu.Unlock()

	dep := &model.Deployment{
		ID: id, SiteID: site.ID, Source: source, Status: "running", StartedAt: time.Now(), User: user,
		ReleaseDir: model.ReleaseDir(d.opts.SitesDir, site.ID, id),
	}
	lp := d.logPath(site.ID, id)
	os.MkdirAll(filepath.Dir(lp), 0o750)
	f, err := os.Create(lp)
	if err != nil {
		d.finish(site.ID, id)
		return nil, nil, err
	}
	l := &depLog{f: f, subs: map[chan string]struct{}{}, done: make(chan struct{})}
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
	return dep, nil
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
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, errors.New("git is not installed on the server (install Git for Windows and restart NodeHoster)")
	}
	dep, l, err := d.begin(ctx, site, source, user)
	if err != nil {
		return nil, err
	}
	token := d.opts.Box.MustUnseal(g.Token)
	go d.run(site, dep, l, func() error {
		args := []string{"clone", "--depth", "1", "--single-branch"}
		if branch != "" {
			args = append(args, "--branch", branch)
		}
		args = append(args, g.Repo, dep.ReleaseDir)
		l.printf("git clone %s%s", redact(g.Repo), map[bool]string{true: " (" + branch + ")", false: ""}[branch != ""])
		env := os.Environ()
		env = append(env, "GIT_TERMINAL_PROMPT=0")
		if token != "" {
			// The token travels in an HTTP header set through git's
			// environment config, so it never appears in the process list
			// or in the clone's .git/config.
			basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
			env = append(env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader",
				"GIT_CONFIG_VALUE_0=Authorization: Basic "+basic)
		}
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
	return dep, nil
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
	defer d.finish(site.ID, dep.ID)
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
		d.prune(ctx, site)
	}
}

func (d *Deployer) steps(ctx context.Context, site *model.Site, dep *model.Deployment, l *depLog, fetch func() error) error {
	if err := os.MkdirAll(filepath.Dir(dep.ReleaseDir), 0o750); err != nil {
		return err
	}
	if err := fetch(); err != nil {
		return err
	}
	if err := d.linkShared(site, dep.ReleaseDir, l); err != nil {
		return err
	}
	workDir := dep.ReleaseDir
	if site.Type == model.SiteNode && site.Node.AppRoot != "" && !filepath.IsAbs(site.Node.AppRoot) {
		workDir = filepath.Join(dep.ReleaseDir, site.Node.AppRoot)
	}
	env, err := d.commandEnv(site)
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
		if step.name == "install" && !fileExists(filepath.Join(workDir, "package.json")) {
			l.printf("no package.json, skipping install")
			continue
		}
		l.printf("%s: %s", step.name, step.cmd)
		cctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		err := runShell(cctx, l, workDir, env, step.cmd)
		cancel()
		if err != nil {
			return fmt.Errorf("%s command failed: %w", step.name, err)
		}
	}
	l.printf("activating release %s", dep.ID)
	if err := d.opts.Activate(ctx, site.ID, dep.ID); err != nil {
		return fmt.Errorf("activate: %w", err)
	}
	return nil
}

// commandEnv is the environment for install/build commands: the service's
// environment, the site's node on PATH, and the site's variables.
func (d *Deployer) commandEnv(site *model.Site) ([]string, error) {
	env := os.Environ()
	version := ""
	if site.Type == model.SiteNode {
		version = site.Node.NodeVersion
	}
	if version == "" {
		version = d.opts.Settings().DefaultNodeVersion
	}
	rt, err := d.opts.ResolveNode(version)
	if err == nil {
		env = prependPath(env, filepath.Dir(rt.Exe))
	} else if site.Type == model.SiteNode {
		return nil, err
	}
	env = append(env, "npm_config_cache="+filepath.Join(d.opts.SitesDir, site.ID, ".npm-cache"),
		"npm_config_update_notifier=false", "CI=true")
	if site.Type == model.SiteNode {
		for _, e := range site.Node.Env {
			v := e.Value
			if e.Secret {
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
func (d *Deployer) linkShared(site *model.Site, release string, l *depLog) error {
	if len(site.Deploy.SharedPaths) == 0 {
		return nil
	}
	shared := model.SharedDir(d.opts.SitesDir, site.ID)
	for _, p := range site.Deploy.SharedPaths {
		p = filepath.Clean(filepath.FromSlash(strings.TrimSpace(p)))
		if p == "." || filepath.IsAbs(p) || strings.HasPrefix(p, "..") {
			return fmt.Errorf("shared path %q must be relative to the application", p)
		}
		src := filepath.Join(shared, p)
		dst := filepath.Join(release, p)
		if _, err := os.Stat(src); os.IsNotExist(err) {
			os.MkdirAll(filepath.Dir(src), 0o750)
			if _, err := os.Stat(dst); err == nil {
				if err := os.Rename(dst, src); err != nil {
					return err
				}
				l.printf("shared: seeded %s from this release", p)
			} else if strings.Contains(filepath.Base(p), ".") {
				os.WriteFile(src, nil, 0o640) // looks like a file
			} else {
				os.MkdirAll(src, 0o750)
			}
		}
		os.RemoveAll(dst)
		os.MkdirAll(filepath.Dir(dst), 0o750)
		if err := link(src, dst); err != nil {
			return fmt.Errorf("link shared path %s: %w", p, err)
		}
		l.printf("shared: %s", p)
	}
	return nil
}

func link(src, dst string) error {
	if err := os.Symlink(src, dst); err == nil {
		return nil
	}
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	if runtime.GOOS == "windows" && st.IsDir() {
		// Directory junctions need no special privilege.
		return exec.Command("cmd", "/d", "/c", "mklink", "/J", dst, src).Run()
	}
	// Last resort for files: copy.
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, st.Mode())
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
	if err := d.opts.Activate(ctx, site.ID, dep.ID); err != nil {
		return nil, err
	}
	d.opts.Bus.Info(events.DeploySucceeded, site.ID, "%s rolled back to %s", site.Name, dep.ID)
	return dep, nil
}

// prune deletes releases beyond KeepReleases, never the active one.
func (d *Deployer) prune(ctx context.Context, site *model.Site) {
	current, err := d.opts.Store.GetSite(ctx, site.ID)
	if err != nil {
		return
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
		if dep.ID == current.ActiveRelease || kept < keep {
			kept++
			continue
		}
		os.RemoveAll(dep.ReleaseDir)
		dep.ReleaseDir = ""
		d.opts.Store.PutDeployment(ctx, dep)
	}
	// Keep the history bounded too.
	if len(list) > 100 {
		for _, dep := range list[100:] {
			if dep.ID != current.ActiveRelease {
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

func runShell(ctx context.Context, out io.Writer, dir string, env []string, command string) error {
	if runtime.GOOS == "windows" {
		return runShellWindows(ctx, out, dir, env, command)
	}
	return runCmd(ctx, out, dir, env, "sh", "-c", command)
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
