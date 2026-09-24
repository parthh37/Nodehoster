package deploy

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/procmgr"
)

// siteRuntime is the runtime of a node or worker site, "" for other sites
// (static sites build with Node.js when they build at all).
func siteRuntime(site *model.Site) string {
	if !site.RunsNode() {
		return ""
	}
	return site.Node.RuntimeName()
}

// manifests are the files whose presence means a runtime's install step has
// something to install; without any of them the step is skipped.
func manifests(rt string) []string {
	switch rt {
	case model.RuntimeDeno:
		return []string{"deno.json", "deno.jsonc", "package.json"}
	case model.RuntimePython:
		return []string{"requirements.txt", "pyproject.toml", "setup.py"}
	case model.RuntimeDotnet, model.RuntimeCustom:
		return nil // their install command, if any, is the site's own business
	}
	return []string{"package.json"}
}

// missingManifest names the manifest the install step needs when none is
// in dir ("" when one is, or the runtime needs none).
func missingManifest(rt, dir string) string {
	list := manifests(rt)
	for _, m := range list {
		if fileExists(filepath.Join(dir, m)) {
			return ""
		}
	}
	return strings.Join(list, " or ")
}

// runtimeEnv adds to a deployment's environment what the site's runtime
// needs: its executable's folder first on PATH (bun, deno, python,
// dotnet, ahead of Node.js) and its package caches in the site's folder,
// which a run-as account can write and deployments share.
func (d *Deployer) runtimeEnv(site *model.Site, env []string) ([]string, error) {
	rt := siteRuntime(site)
	siteDir := filepath.Join(d.opts.SitesDir, site.ID)
	switch rt {
	case model.RuntimeBun, model.RuntimeDeno, model.RuntimePython, model.RuntimeDotnet:
		resolve := d.opts.ResolveRuntime
		if resolve == nil {
			resolve = func(string, string) (procmgr.RuntimeExe, error) {
				return procmgr.RuntimeExe{}, fmt.Errorf("%s is not available on this server", model.RuntimeLabel(rt))
			}
		}
		exe, err := resolve(rt, site.Node.RuntimeVersion)
		switch {
		case err == nil:
			env = prependPath(env, filepath.Dir(exe.Exe))
		case rt != model.RuntimeDotnet:
			return nil, err
		}
		// Without .NET only a build command that runs dotnet fails, and it
		// says so; a self-contained app deployed ready-built needs none.
	}
	switch rt {
	case model.RuntimeBun:
		env = append(env, "BUN_INSTALL_CACHE_DIR="+filepath.Join(siteDir, ".bun-cache"))
	case model.RuntimeDeno:
		env = append(env, "DENO_DIR="+model.DenoDir(d.opts.SitesDir, site.ID), "DENO_NO_UPDATE_CHECK=1", "DENO_NO_PROMPT=1")
	case model.RuntimePython:
		env = append(env, "PIP_CACHE_DIR="+filepath.Join(siteDir, ".pip-cache"), "PIP_DISABLE_PIP_VERSION_CHECK=1",
			"PIP_NO_INPUT=1", "PYTHONUTF8=1")
	case model.RuntimeDotnet:
		env = append(env, "DOTNET_CLI_TELEMETRY_OPTOUT=1", "DOTNET_NOLOGO=1", "DOTNET_SKIP_FIRST_TIME_EXPERIENCE=1",
			"DOTNET_CLI_HOME="+filepath.Join(siteDir, ".dotnet"), "NUGET_PACKAGES="+filepath.Join(siteDir, ".nuget"))
	}
	return env, nil
}

// prepareVenv creates a python site's virtual environment in the release
// (each release has its own, so a rollback gets the packages it was
// deployed with and a running release is never changed under it) and
// activates it for the install and build commands. Nothing is created
// when the release has nothing to install.
func (d *Deployer) prepareVenv(ctx context.Context, site *model.Site, workDir string, env []string, l *depLog) ([]string, error) {
	if siteRuntime(site) != model.RuntimePython || missingManifest(model.RuntimePython, workDir) != "" {
		return env, nil
	}
	venv := model.DefaultVenv
	if p := site.Node.Python; p != nil && p.Venv != "" {
		venv = p.Venv
	}
	dir := filepath.Join(workDir, filepath.FromSlash(strings.ReplaceAll(venv, `\`, "/")))
	scripts := filepath.Join(dir, "bin")
	if runtime.GOOS == "windows" {
		scripts = filepath.Join(dir, "Scripts")
	}
	if d.opts.ResolveRuntime == nil {
		return nil, fmt.Errorf("%s is not available on this server", model.RuntimeLabel(model.RuntimePython))
	}
	exe, err := d.opts.ResolveRuntime(model.RuntimePython, site.Node.RuntimeVersion)
	if err != nil {
		return nil, err
	}
	// A release is new, so an environment already in it came with the
	// upload or the repository: made on another computer, for another
	// interpreter. It is replaced rather than trusted.
	args := []string{"-m", "venv", dir}
	if fileExists(filepath.Join(dir, "pyvenv.cfg")) {
		args = []string{"-m", "venv", "--clear", dir}
		l.printf("venv: replacing the virtual environment that came with the release")
	}
	l.printf("venv: %s -m venv %s", exe.Exe, venv)
	cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	err = runCmd(cctx, l, workDir, env, exe.Exe, args...)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("create the virtual environment: %w", err)
	}
	env = prependPath(env, scripts)
	return append(env, "VIRTUAL_ENV="+dir), nil
}

// installSkipMessage says why the install step is skipped for a release
// with nothing to install, "" when it runs. Python's default command
// installs requirements.txt, so a project with only pyproject.toml skips
// it (set an install command such as `python -m pip install .` for one).
func installSkipMessage(site *model.Site, workDir string) string {
	rt := siteRuntime(site)
	m := missingManifest(rt, workDir)
	if m == "" && rt == model.RuntimePython && site.Deploy.InstallCommand == model.DefaultInstallCommand(rt) &&
		!fileExists(filepath.Join(workDir, "requirements.txt")) {
		m = "requirements.txt"
	}
	if m != "" {
		return "no " + m + ", skipping install"
	}
	return ""
}
