package core

import (
	"path/filepath"
	"sort"

	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/procmgr"
	"github.com/parthh37/nodehoster/internal/runtimes"
)

// openRuntimes sets up the runtimes other than Node.js: Bun and Deno
// versions live in <data>\runtimes next to the Node.js ones.
func (c *Core) openRuntimes() {
	c.Runtimes = runtimes.New(filepath.Join(c.Paths.Data, "runtimes"), c.Paths.Tmp, c.Log)
	c.Runtimes.OnInstalled = func(rt, version string, err error) {
		label := model.RuntimeLabel(rt)
		if err != nil {
			c.Bus.Error(events.RuntimeFailed, "", "%s %s could not be installed: %v", label, version, err)
			return
		}
		c.Bus.Info(events.RuntimeInstalled, "", "%s %s installed", label, version)
	}
}

// resolveRuntime finds the executable of a site's runtime (not Node.js):
// the site's version, else the server's default for that runtime.
func (c *Core) resolveRuntime(rt, version string) (procmgr.RuntimeExe, error) {
	if version == "" {
		version = c.Settings().Runtimes.Default(rt)
	}
	return c.Runtimes.Resolve(rt, version)
}

// RuntimeReport is what the Runtimes page shows.
func (c *Core) RuntimeReport() runtimes.Report {
	return c.Runtimes.Report(c.Settings().Runtimes)
}

// RuntimeVersionInUse reports which sites pin a Bun or Deno version, and
// whether it is the server's default, which removing would break too.
func (c *Core) RuntimeVersionInUse(rt, v string) []string {
	var out []string
	for _, s := range c.Sites() {
		if s.RunsNode() && s.Node.RuntimeName() == rt && s.Node.RuntimeVersion == v {
			out = append(out, s.Name)
		}
	}
	sort.Strings(out)
	if c.Settings().Runtimes.Default(rt) == v {
		out = append(out, "the server default")
	}
	return out
}
