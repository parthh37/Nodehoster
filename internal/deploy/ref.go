package deploy

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/preview"
)

// fetchTimeout bounds fetching one ref: a preview must not hang on a git
// host that stopped answering.
const fetchTimeout = 10 * time.Minute

// DeployRef deploys one ref of the site's repository: a branch
// (refs/heads/x) or a pull request ref such as refs/pull/42/head, which
// is how a fork's pull request is reached without cloning the fork. It
// fetches just that commit into an empty repository (git clone can only
// take branches and tags). done, if set, is called with the finished
// deployment once it has succeeded or failed.
//
// The git token (deploy.git.token, or tokenFrom read from its secret
// store) is only given to git, in its environment: never to the build or
// the application.
func (d *Deployer) DeployRef(ctx context.Context, site *model.Site, ref, source, user string, done func(*model.Deployment)) (*model.Deployment, error) {
	g := site.Deploy.Git
	if g.Repo == "" {
		return nil, errors.New("no git repository is configured for this site")
	}
	if !preview.ValidRef(ref) || !strings.HasPrefix(ref, "refs/") {
		return nil, fmt.Errorf("%q is not a ref that can be deployed", ref)
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
	go func() {
		d.run(site, dep, l, func() error {
			if g.TokenFrom != nil {
				// As in DeployGit: read at each deployment.
				l.printf("reading the token from secret store %q", g.TokenFrom.Store)
				t, err := d.secretToken(site, *g.TokenFrom)
				if err != nil {
					return err
				}
				token = t
			}
			env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
			if token != "" {
				// As in DeployGit: the token never reaches the arguments
				// or the repository's config.
				basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
				env = append(env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader",
					"GIT_CONFIG_VALUE_0=Authorization: Basic "+basic)
			}
			cctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
			defer cancel()
			dir := dep.ReleaseDir
			l.printf("git fetch %s %s", redact(g.Repo), ref)
			if err := runCmd(cctx, l, "", env, git, "init", "-q", dir); err != nil {
				return fmt.Errorf("git init failed: %w", err)
			}
			if err := runCmd(cctx, l, "", env, git, "-C", dir, "fetch", "--depth", "1", "--no-tags", "--", g.Repo, ref); err != nil {
				return fmt.Errorf("git fetch failed: %w", err)
			}
			if err := runCmd(cctx, l, "", env, git, "-C", dir, "-c", "advice.detachedHead=false", "checkout", "-q", "--detach", "FETCH_HEAD"); err != nil {
				return fmt.Errorf("git checkout failed: %w", err)
			}
			out, _ := exec.Command(git, "-C", dir, "log", "-1", "--pretty=%H%n%s").Output()
			if parts := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2); len(parts) == 2 {
				dep.Commit, dep.Message = parts[0], parts[1]
				l.printf("commit %s: %s", dep.Commit[:min(12, len(dep.Commit))], dep.Message)
			}
			os.RemoveAll(filepath.Join(dir, ".git"))
			return nil
		})
		if done != nil {
			final := *dep
			done(&final)
		}
	}()
	return &snapshot, nil
}
