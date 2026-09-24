package importer

import (
	"encoding/json"
	"fmt"
	"math"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/cron"
	"github.com/parthh37/nodehoster/internal/model"
)

// pm2App is one PM2 application, from an ecosystem file or from `pm2
// jlist` (where the settings are under pm2_env).
type pm2App struct {
	m     map[string]any
	jlist bool
}

func (a pm2App) get(keys ...string) (any, bool) {
	for _, k := range keys {
		if v, ok := a.m[k]; ok && v != nil {
			return v, true
		}
	}
	return nil, false
}

func (a pm2App) str(keys ...string) string {
	v, _ := a.get(keys...)
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	return ""
}

// list reads "a b c" (split like a command line) or ["a", "b"].
func (a pm2App) list(keys ...string) []string {
	v, _ := a.get(keys...)
	switch x := v.(type) {
	case string:
		return splitArgs(x)
	case []any:
		var out []string
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			} else if e != nil {
				out = append(out, fmt.Sprint(e))
			}
		}
		return out
	}
	return nil
}

func (a pm2App) boolean(key string) (bool, bool) {
	v, ok := a.get(key)
	if !ok {
		return false, false
	}
	switch x := v.(type) {
	case bool:
		return x, true
	case string:
		return strings.EqualFold(x, "true"), true
	}
	return false, false
}

func (a pm2App) env(key string) map[string]string {
	v, _ := a.get(key)
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]string{}
	for k, e := range m {
		switch x := e.(type) {
		case string:
			out[k] = x
		case float64:
			out[k] = strconv.FormatFloat(x, 'f', -1, 64)
		case bool:
			out[k] = strconv.FormatBool(x)
		case nil:
		default:
			b, _ := json.Marshal(x)
			out[k] = string(b)
		}
	}
	return out
}

// readPM2 finds the apps in an ecosystem file (JSON or a JavaScript
// literal) or in `pm2 jlist` / `pm2 prettylist` output.
func readPM2(data []byte, filename string) ([]pm2App, error) {
	text := strings.TrimSpace(strings.TrimPrefix(string(data), "\uFEFF"))
	var root any
	if err := json.Unmarshal([]byte(text), &root); err != nil {
		lower := strings.ToLower(filename)
		if strings.HasSuffix(lower, ".json") {
			return nil, fmt.Errorf("not valid JSON: %v", err)
		}
		if root, err = parseEcosystem(text); err != nil {
			return nil, err
		}
	}
	var list []any
	switch x := root.(type) {
	case []any:
		list = x
	case map[string]any:
		if apps, ok := x["apps"]; ok {
			switch a := apps.(type) {
			case []any:
				list = a
			case map[string]any:
				list = []any{a}
			}
		} else if _, ok := x["script"]; ok {
			list = []any{x}
		}
	}
	var apps []pm2App
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if env, ok := m["pm2_env"].(map[string]any); ok {
			// `pm2 jlist`: the effective settings are in pm2_env.
			merged := map[string]any{}
			for k, v := range env {
				merged[k] = v
			}
			if n, ok := m["name"]; ok {
				merged["name"] = n
			}
			apps = append(apps, pm2App{m: merged, jlist: true})
			continue
		}
		apps = append(apps, pm2App{m: m})
	}
	if len(apps) == 0 {
		return nil, fmt.Errorf("no PM2 apps were found: expected an ecosystem file ({apps: [...]}) or the output of `pm2 jlist`")
	}
	return apps, nil
}

// systemEnv are variables of the machine or of PM2 itself that `pm2 jlist`
// reports among an app's environment; they are not the app's settings.
var systemEnv = regexp.MustCompile(`(?i)^(path|pathext|home|homedrive|homepath|user|username|userdomain.*|userprofile|logname|shell|shlvl|pwd|oldpwd|lang|language|lc_.*|term.*|colorterm|tmp|temp|tmpdir|appdata|localappdata|programdata|programfiles.*|programw6432|commonprogram.*|systemroot|systemdrive|windir|comspec|computername|hostname|os|processor_.*|number_of_processors|sessionname|logonserver|psmodulepath|public|allusersprofile|driverdata|onedrive.*|mail|display|xdg_.*|ssh_.*|dbus_.*|editor|pager|less.*|nvm_.*|npm_.*|pm2_.*|pm_.*|node_app_instance|unique_id|name|status|restart_time|axm_.*|instance_var|vizion.*|motd_shown|_|__cf.*|wt_.*|vscode_.*|conemu.*|fps_browser.*|chocolateyinstall|chocolateylastpathupdate|jenkins.*|silent|windows_tracing.*|ide_.*)$`)

// pm2Hint recognizes apps that do not serve HTTP by their name or script.
var pm2Hint = regexp.MustCompile(`(?i)(worker|queue|consumer|bot|job|cron|schedul|daemon|listener|processor|mailer|sync|watcher|poller)`)

// previewPM2 proposes a site (or a scheduled task) per PM2 app.
func previewPM2(pv *model.ImportPreview, data []byte, filename string, opts Options) error {
	apps, err := readPM2(data, filename)
	if err != nil {
		return err
	}
	keys := map[string]bool{}
	type built struct {
		it     *item
		cwd    string
		isTask bool
	}
	var all []built
	for i, a := range apps {
		name := a.str("name")
		if name == "" {
			name = strings.TrimSuffix(path.Base(strings.ReplaceAll(a.str("script", "pm_exec_path"), `\`, "/")), ".js")
		}
		if name == "" {
			name = fmt.Sprintf("app-%d", i+1)
		}
		it := newItem(uniqueKey(keys, "pm2-"+strings.ToLower(siteName(name))), fmt.Sprintf("PM2 app %q", name))
		b := pm2Builder{it: it, app: a, opts: opts, name: name}
		b.build()
		all = append(all, built{it: it, cwd: b.cwd, isTask: b.task != nil && it.Choice == 0 && it.Options[0].Kind == model.ImportKindTask})
	}
	// A cron-style app usually belongs with a web or worker app in the
	// same folder: propose that as the site that runs the task.
	for _, t := range all {
		if !t.isTask {
			continue
		}
		for _, o := range all {
			if o.isTask || o.cwd == "" || !strings.EqualFold(o.cwd, t.cwd) {
				continue
			}
			for i := range t.it.Options {
				if t.it.Options[i].Kind == model.ImportKindTask {
					t.it.Options[i].TaskSite = model.ImportRef + o.it.Key
				}
			}
			t.it.converted("Proposed as a task of %s, the app in the same folder.", o.it.Source)
			break
		}
	}
	for _, b := range all {
		pv.Items = append(pv.Items, b.it.ImportItem)
	}
	return nil
}

type pm2Builder struct {
	it   *item
	app  pm2App
	opts Options
	name string

	cwd, script, npmScript string
	nodeVersion            string
	args, nodeArgs         []string
	env                    []model.EnvVar
	hasPort                bool
	task                   *model.ScheduledTask
}

func (b *pm2Builder) build() {
	it, a := b.it, b.app
	unsupported := b.entry()
	b.readEnv()

	hasPort := b.hasPort
	if _, ok := a.get("port"); ok {
		hasPort = true
	}
	for _, x := range b.args {
		if strings.HasPrefix(x, "--port") || x == "-p" {
			hasPort = true
		}
	}

	autorestart, hasAuto := a.boolean("autorestart")
	cronSpec := a.str("cron_restart", "cron")
	isCronJob := hasAuto && !autorestart && cronSpec != ""

	node := b.site(model.SiteNode)
	worker := b.site(model.SiteWorker)
	b.recycle(node, worker, cronSpec, isCronJob)

	switch {
	case isCronJob:
		b.task = b.makeTask(cronSpec)
		b.it.Options = append(b.it.Options, model.ImportOption{Label: "Scheduled task", Kind: model.ImportKindTask, Task: b.task})
		it.addSite("Background worker", worker)
		it.addSite("Node.js application", node)
		it.converted("autorestart is off and cron_restart is %q: PM2 runs this app as a cron job, so it is proposed as a scheduled task of a site.", cronSpec)
	case !hasPort && pm2Hint.MatchString(b.name+" "+b.script+" "+b.npmScript):
		it.addSite("Background worker", worker)
		it.addSite("Node.js application", node)
		it.converted("No port is configured and the name suggests a background process: proposed as a background worker (no HTTP). Choose Node.js application if it serves requests.")
	default:
		it.addSite("Node.js application", node)
		it.addSite("Background worker", worker)
		it.skipped("Add bindings (host names and ports) after import: PM2 does not know how the app is reached.")
	}
	if unsupported != "" {
		it.Selected = false
		it.skipped("%s", unsupported)
	}
}

// entry works out the folder, the script (or npm script) and arguments.
// It returns why the app cannot be hosted, if it cannot.
func (b *pm2Builder) entry() (unsupported string) {
	it, a := b.it, b.app
	b.cwd = a.str("cwd", "pm_cwd")
	script := a.str("script", "pm_exec_path")
	b.args = a.list("args")
	b.nodeArgs = a.list("node_args", "interpreter_args", "nodeArgs")

	interp := a.str("interpreter", "exec_interpreter")
	lower := strings.ToLower(path.Base(strings.ReplaceAll(interp, `\`, "/")))
	switch {
	case interp == "" || strings.HasPrefix(lower, "node"):
		if v := nodeVersionIn(interp); v != "" {
			b.nodeVersion = v
		}
	case lower == "none":
		unsupported = "The app is a binary run directly (interpreter: none); NodeHoster runs Node.js applications."
	default:
		unsupported = fmt.Sprintf("The app runs with %s, not Node.js; NodeHoster runs Node.js applications.", interp)
	}
	if v := a.str("node_version"); v != "" && b.nodeVersion == "" {
		b.nodeVersion = v
	}

	base := strings.ToLower(path.Base(strings.ReplaceAll(script, `\`, "/")))
	switch base {
	case "npm", "npm.cmd", "npm-cli.js", "yarn", "yarn.cmd", "yarn.js", "pnpm", "pnpm.cmd":
		args := b.args
		if len(args) > 0 && args[0] == "run" {
			args = args[1:]
		}
		if len(args) == 0 {
			return "The app runs npm without a script."
		}
		b.npmScript, b.args = args[0], args[1:]
		if len(b.args) > 0 && b.args[0] == "--" {
			b.args = b.args[1:]
		}
		if !strings.HasPrefix(base, "npm") {
			it.approximated("%s run %s: NodeHoster runs `npm run %s`.", base, b.npmScript, b.npmScript)
		} else {
			it.converted("npm script %q.", b.npmScript)
		}
		return unsupported
	}
	for _, ext := range []string{".sh", ".py", ".rb", ".php", ".exe", ".bat", ".cmd", ".ps1"} {
		if strings.HasSuffix(base, ext) {
			return fmt.Sprintf("The script %s is not JavaScript; NodeHoster runs Node.js applications.", script)
		}
	}
	if script == "" {
		return "The app has no script."
	}
	// An absolute script without cwd: its folder is the application.
	if isAbs(script) {
		dir, file := splitPath(script)
		if b.cwd == "" {
			b.cwd, script = dir, file
		} else if rel, ok := relTo(b.cwd, script); ok {
			script = rel
		}
	}
	b.script = strings.TrimPrefix(strings.TrimPrefix(script, "./"), `.\`)
	if b.cwd == "" {
		it.skipped("The app's folder (cwd) is not known: enter the application path before importing.")
	}
	it.converted("Runs %s in %s.", b.script, orUnknown(b.cwd))
	return unsupported
}

func orUnknown(s string) string {
	if s == "" {
		return "(folder not set)"
	}
	return s
}

func isAbs(p string) bool {
	return strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`) || (len(p) > 2 && p[1] == ':' && (p[2] == '\\' || p[2] == '/'))
}

func splitPath(p string) (dir, file string) {
	i := strings.LastIndexAny(p, `/\`)
	return p[:i], p[i+1:]
}

// relTo makes an absolute path relative to dir when it is inside it.
func relTo(dir, p string) (string, bool) {
	d := strings.TrimRight(strings.ReplaceAll(dir, `\`, "/"), "/") + "/"
	q := strings.ReplaceAll(p, `\`, "/")
	if strings.HasPrefix(strings.ToLower(q), strings.ToLower(d)) {
		return q[len(d):], true
	}
	return p, false
}

func joinPath(dir, file string) string {
	if dir == "" || isAbs(file) {
		return file
	}
	if sep(dir) == '\\' {
		file = strings.ReplaceAll(file, "/", `\`)
	}
	return strings.TrimRight(dir, `\/`) + string(sep(dir)) + file
}

// readEnv merges env with env_production, as `pm2 start --env production`
// does: that is how apps are usually run on a server.
func (b *pm2Builder) readEnv() {
	it, a := b.it, b.app
	env := a.env("env")
	if b.app.jlist {
		var dropped int
		for k := range env {
			if systemEnv.MatchString(k) {
				delete(env, k)
				dropped++
			}
		}
		if dropped > 0 {
			it.approximated("%d variables of the machine or of PM2 in `pm2 jlist` (PATH, HOME, PM2_…) were left out; check that the app's own variables are all there.", dropped)
		}
	}
	if prod := a.env("env_production"); prod != nil {
		if env == nil {
			env = map[string]string{}
		}
		for k, v := range prod {
			env[k] = v
		}
		it.converted("env_production was used (merged over env), as with `pm2 start --env production`.")
	}
	var others []string
	for k := range a.m {
		if strings.HasPrefix(k, "env_") && k != "env_production" {
			others = append(others, k)
		}
	}
	if len(others) > 0 {
		slices.Sort(others)
		it.skipped("Other environments (%s) were not used.", strings.Join(others, ", "))
	}
	var secrets, bad []string
	for _, k := range sortedKeys(env) {
		v := env[k]
		if strings.EqualFold(k, "NODE_ENV") && v == "production" {
			continue // NodeHoster's default
		}
		if strings.EqualFold(k, "PORT") {
			b.hasPort = true
			it.converted("PORT=%s was dropped: NodeHoster gives each instance its own port in PORT (the app must read process.env.PORT).", v)
			continue
		}
		e, ok := envVar(k, v)
		if !ok {
			bad = append(bad, k)
			continue
		}
		b.env = append(b.env, e)
		if e.Secret {
			secrets = append(secrets, k)
		}
	}
	if n := len(b.env); n > 0 {
		it.converted("%d environment variable(s).", n)
	}
	if len(secrets) > 0 {
		it.converted("Stored encrypted as secrets (by their names): %s.", strings.Join(secrets, ", "))
	}
	if len(bad) > 0 {
		it.skipped("Variables with names Node.js apps cannot use as process.env.NAME were left out: %s.", strings.Join(bad, ", "))
	}
}

// site builds the node or worker draft.
func (b *pm2Builder) site(t model.SiteType) *model.Site {
	it, a := b.it, b.app
	s := nodeSite(b.name, b.cwd, b.script)
	s.Type = t
	n := s.Node
	n.NpmScript, n.Args, n.NodeArgs = b.npmScript, b.args, b.nodeArgs
	if b.npmScript != "" {
		n.Script = ""
	}
	n.Env = slices.Clone(b.env)
	quiet := t == model.SiteWorker // notes are written once, for the node draft
	note := func(f func(string, ...any), format string, args ...any) {
		if !quiet {
			f(format, args...)
		}
	}
	if b.nodeVersion != "" {
		if quiet {
			n.NodeVersion = b.nodeVersion
		} else {
			it.nodeVersion(n, b.nodeVersion, "the PM2 interpreter", b.opts)
		}
	}
	if len(b.nodeArgs) > 0 {
		note(it.converted, "Node.js options %s.", strings.Join(b.nodeArgs, " "))
	}

	if v, ok := a.get("instances"); ok {
		c, isNum := v.(float64)
		txt := strings.ToLower(fmt.Sprint(v))
		switch {
		case txt == "max" || (isNum && c == 0):
			n.Instances = min(b.opts.CPUs, 64)
			note(it.approximated, "instances %v: %d, this server's CPU count.", v, n.Instances)
		case isNum && c < 0:
			n.Instances = min(max(b.opts.CPUs+int(c), 1), 64)
			note(it.approximated, "instances %v (all CPUs but %d): %d on this server.", v, int(-c), n.Instances)
		case isNum:
			n.Instances = min(max(int(c), 1), 64)
			note(it.converted, "%d instance(s).", n.Instances)
		default:
			if c, err := strconv.Atoi(txt); err == nil && c > 0 {
				n.Instances = min(c, 64)
			}
		}
	}
	if mode := strings.ToLower(a.str("exec_mode")); strings.HasPrefix(mode, "cluster") && t == model.SiteNode {
		note(it.converted, "Cluster mode: NodeHoster starts the instances as separate processes, each on its own port, and load-balances requests between them.")
	}
	if t == model.SiteNode {
		n.HealthCheck.Enabled = true
	}

	if v, ok := a.get("max_memory_restart"); ok {
		if mb, ok := memoryMB(v); ok {
			n.Recycle.MemoryLimitMB = mb
			note(it.converted, "max_memory_restart: recycled above %d MB.", mb)
		} else {
			note(it.skipped, "max_memory_restart %v was not understood.", v)
		}
	}
	if w, ok := a.get("watch"); ok {
		switch x := w.(type) {
		case bool:
			n.WatchFiles = x
		case []any, string:
			n.WatchFiles = true
			note(it.approximated, "watch %v: the whole application folder is watched.", x)
		}
		if ig := a.list("ignore_watch"); len(ig) > 0 && n.WatchFiles {
			n.WatchIgnore = append([]string{"node_modules", ".git", "logs"}, ig...)
			note(it.approximated, "ignore_watch: folders named %s are ignored (NodeHoster matches folder names, not glob patterns).", strings.Join(ig, ", "))
		}
	}

	// Restart policy. `pm2 jlist` lists PM2's defaults too; those are left
	// to NodeHoster's own defaults.
	jlistDefault := func(key string, def float64) bool {
		v, _ := a.get(key)
		f, ok := v.(float64)
		return a.jlist && ok && f == def
	}
	if auto, ok := a.boolean("autorestart"); ok && !auto {
		n.RestartPolicy = "never"
		note(it.converted, "autorestart: false — the app is not restarted when it exits.")
	}
	if v, ok := a.get("max_restarts"); ok && !(jlistDefault("max_restarts", 16) && jlistDefault("min_uptime", 1000)) {
		if c, isNum := v.(float64); isNum && c >= 0 {
			n.MaxRestarts = int(c)
			uptime := time.Second
			if u, ok := a.get("min_uptime"); ok {
				if d, ok := duration(u); ok {
					uptime = d
				}
			}
			n.RestartWindowSec = int(math.Min(math.Max(uptime.Seconds()*c, 60), 3600))
			n.RapidFailAction = "stop"
			note(it.approximated, "max_restarts %d / min_uptime %s: rapid-fail protection stops the site after %d failures within %d s.", int(c), uptime, int(c), n.RestartWindowSec)
		}
	}
	if v, ok := a.get("kill_timeout"); ok && !jlistDefault("kill_timeout", 1600) {
		if d, ok := duration(v); ok {
			n.ShutdownTimeoutSec = max(int(math.Ceil(d.Seconds())), 1)
			note(it.converted, "kill_timeout: %d s shutdown timeout.", n.ShutdownTimeoutSec)
		}
	}
	if v, ok := a.get("listen_timeout"); ok && t == model.SiteNode {
		if d, ok := duration(v); ok {
			n.StartupTimeoutSec = max(int(math.Ceil(d.Seconds())), 1)
			note(it.converted, "listen_timeout: %d s startup timeout.", n.StartupTimeoutSec)
		}
	}
	return s
}

// recycle converts cron_restart of a long-running app to a daily recycle.
func (b *pm2Builder) recycle(node, worker *model.Site, spec string, cronJob bool) {
	if spec == "" || cronJob {
		return
	}
	if hhmm, ok := dailyTime(spec); ok {
		for _, s := range []*model.Site{node, worker} {
			s.Node.Recycle.ScheduleTimes = []string{hhmm}
		}
		b.it.converted("cron_restart %q: recycled daily at %s.", spec, hhmm)
		return
	}
	b.it.skipped("cron_restart %q is not a daily time; set recycling times on the Settings tab (HH:MM), or a periodic interval.", spec)
}

var dailyRe = regexp.MustCompile(`^(?:0\s+)?(\d{1,2})\s+(\d{1,2})\s+\*\s+\*\s+\*$`)

// dailyTime recognizes "M H * * *" (optionally with a leading seconds
// field of 0, which PM2's cron accepts).
func dailyTime(spec string) (string, bool) {
	m := dailyRe.FindStringSubmatch(strings.TrimSpace(spec))
	if m == nil {
		return "", false
	}
	min, _ := strconv.Atoi(m[1])
	hour, _ := strconv.Atoi(m[2])
	if min > 59 || hour > 23 {
		return "", false
	}
	return fmt.Sprintf("%02d:%02d", hour, min), true
}

// makeTask turns a cron-style PM2 app into a scheduled task.
func (b *pm2Builder) makeTask(spec string) *model.ScheduledTask {
	t := &model.ScheduledTask{Name: siteName(b.name), Enabled: true, Overlap: model.OverlapSkip,
		NpmScript: b.npmScript, Args: b.args, Env: slices.Clone(b.env)}
	if b.npmScript == "" {
		// Absolute, so it runs from wherever the host site's release is.
		t.Script = joinPath(b.cwd, b.script)
	}
	fields := strings.Fields(spec)
	if len(fields) == 6 && fields[0] == "0" {
		spec = strings.Join(fields[1:], " ")
	}
	if _, err := cron.Parse(spec); err == nil {
		t.Schedule = spec
	} else {
		b.it.skipped("cron_restart %q could not be used as a schedule (%v): the task only runs when started by hand until a schedule is set.", spec, err)
	}
	if b.npmScript != "" {
		b.it.approximated("As a task, `npm run %s` runs in the folder of the site that gets it.", b.npmScript)
	}
	return t
}

// memoryMB reads PM2's max_memory_restart: "300M", "1G", "512K" or bytes.
func memoryMB(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		return max(int(math.Ceil(x/(1<<20))), 1), x > 0
	case string:
		s := strings.ToUpper(strings.TrimSpace(x))
		s = strings.TrimSuffix(s, "B")
		mult := 1.0 / (1 << 20)
		switch {
		case strings.HasSuffix(s, "K"):
			mult, s = 1.0/1024, s[:len(s)-1]
		case strings.HasSuffix(s, "M"):
			mult, s = 1, s[:len(s)-1]
		case strings.HasSuffix(s, "G"):
			mult, s = 1024, s[:len(s)-1]
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil || f <= 0 {
			return 0, false
		}
		return max(int(math.Ceil(f*mult)), 1), true
	}
	return 0, false
}

// duration reads PM2 times: milliseconds, or "5s", "1m", "1h".
func duration(v any) (time.Duration, bool) {
	switch x := v.(type) {
	case float64:
		return time.Duration(x) * time.Millisecond, x >= 0
	case string:
		s := strings.TrimSpace(x)
		if ms, err := strconv.Atoi(s); err == nil {
			return time.Duration(ms) * time.Millisecond, ms >= 0
		}
		d, err := time.ParseDuration(s)
		return d, err == nil && d >= 0
	}
	return 0, false
}
