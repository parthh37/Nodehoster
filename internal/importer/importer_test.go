package importer

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
)

func preview(t *testing.T, source, file string, opts Options) model.ImportPreview {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	if opts.CPUs == 0 {
		opts.CPUs = 8
	}
	if opts.ReadFile == nil {
		opts.ReadFile = fixtureFiles
	}
	pv, err := Preview(source, data, filepath.Base(file), opts)
	if err != nil {
		t.Fatalf("Preview(%s): %v", file, err)
	}
	return pv
}

func find(t *testing.T, pv model.ImportPreview, key string) model.ImportItem {
	t.Helper()
	for _, it := range pv.Items {
		if it.Key == key {
			return it
		}
	}
	var keys []string
	for _, it := range pv.Items {
		keys = append(keys, it.Key)
	}
	t.Fatalf("no item %q in %v", key, keys)
	return model.ImportItem{}
}

func chosen(it model.ImportItem) model.ImportOption { return it.Options[it.Choice] }

// hasNote reports whether a note of the level mentions all the words.
func hasNote(it model.ImportItem, level string, words ...string) bool {
	for _, n := range it.Notes {
		if n.Level != level {
			continue
		}
		ok := true
		for _, w := range words {
			ok = ok && strings.Contains(n.Text, w)
		}
		if ok {
			return true
		}
	}
	return false
}

func notes(it model.ImportItem) string {
	var b strings.Builder
	for _, n := range it.Notes {
		b.WriteString("  " + n.Level + ": " + n.Text + "\n")
	}
	return b.String()
}

func TestIISApplicationHost(t *testing.T) {
	pv := preview(t, model.ImportIIS, "iis/applicationHost.config", Options{})
	var order []string
	for _, it := range pv.Items {
		order = append(order, it.Key)
	}
	// A mounted application comes before the site that mounts it.
	want := []string{"iis-default web site", "iis-shop-api", "iis-shop", "iis-docs", "iis-api gateway", "iis-old", "iis-located"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("items %v, want %v", order, want)
	}
	if len(pv.Warnings) != 1 || !strings.Contains(pv.Warnings[0], "ARR") {
		t.Errorf("warnings: %v", pv.Warnings)
	}

	def := find(t, pv, "iis-default web site")
	if def.Selected || chosen(def).Site.Static.Root != `C:\inetpub\wwwroot` {
		t.Errorf("default web site: selected=%v %+v", def.Selected, chosen(def).Site.Static)
	}

	shop := find(t, pv, "iis-shop")
	s := chosen(shop).Site
	if s.Type != model.SiteNode || s.Node.AppRoot != `C:\sites\shop` || s.Node.Script != "server.js" || !s.Node.WatchFiles || !shop.Selected {
		t.Fatalf("shop: %+v %+v\n%s", s, s.Node, notes(shop))
	}
	wantBindings := []model.Binding{
		{Protocol: "http", Port: 80, Host: "shop.example.com"},
		{Protocol: "https", Port: 443, Host: "shop.example.com", CertMode: model.CertModeAuto},
	}
	if !reflect.DeepEqual(s.Bindings, wantBindings) {
		t.Errorf("shop bindings %+v", s.Bindings)
	}
	if len(s.Node.Env) != 2 || !s.Node.Env[0].Secret || s.Node.Env[0].Name != "STRIPE_SECRET_KEY" || s.Node.Env[1].Secret {
		t.Errorf("shop env %+v", s.Node.Env)
	}
	// The iisnode routing rules are dropped, the site's own rule is kept.
	if len(s.Routing.Rewrites) != 1 || s.Routing.Rewrites[0].Name != "Force www" {
		t.Errorf("shop rewrites %+v", s.Routing.Rewrites)
	}
	wantLocs := []model.Location{
		{Path: "/images", Kind: "static", Root: `D:\media\images`, StripPrefix: true},
		{Path: "/api", Kind: "site", SiteID: "import:iis-shop-api"},
	}
	if !reflect.DeepEqual(s.Routing.Locations, wantLocs) {
		t.Errorf("shop locations %+v", s.Routing.Locations)
	}
	for _, n := range []struct{ level, text string }{
		{model.NoteConverted, "NodeInspector"},
		{model.NoteApproximated, "3F2A9C"},
		{model.NoteSkipped, "https *:443: has no host name"},
		{model.NoteSkipped, "net.tcp"},
		{model.NoteSkipped, `CONTOSO\svc-node`},
		{model.NoteSkipped, "loggingEnabled"},
		{model.NoteConverted, "uses SNI"},
	} {
		if !hasNote(shop, n.level, n.text) {
			t.Errorf("shop: no %s note about %q:\n%s", n.level, n.text, notes(shop))
		}
	}
	if strings.Contains(notes(shop), "enc:") {
		t.Error("the app pool password leaked into a note")
	}

	api := chosen(find(t, pv, "iis-shop-api")).Site
	if api.Type != model.SiteNode || api.Node.Script != "index.js" || api.Node.AppRoot != `C:\sites\shop-api` || len(api.Bindings) != 0 || api.Name != "Shop api" {
		t.Errorf("shop api: %+v %+v", api, api.Node)
	}

	docsItem := find(t, pv, "iis-docs")
	docs := chosen(docsItem).Site
	if docs.Type != model.SiteStatic || docs.AutoStart || !docs.Static.DirectoryBrowsing || !reflect.DeepEqual(docs.Static.IndexFiles, []string{"start.html"}) {
		t.Errorf("docs: %+v %+v", docs, docs.Static)
	}
	if !reflect.DeepEqual(docs.Bindings, []model.Binding{{Protocol: "http", IP: "10.0.0.5", Port: 8080, Host: "docs.example.com"}, {Protocol: "http", IP: "::1", Port: 8081}}) {
		t.Errorf("docs bindings %+v", docs.Bindings)
	}
	if len(docs.Routing.MimeTypes) != 1 || docs.Routing.MimeTypes[0].Extension != ".webmanifest" || len(docs.Routing.Rewrites) != 1 {
		t.Errorf("docs routing %+v", docs.Routing)
	}
	// The PHP application is left out; its virtual directory is kept.
	if !reflect.DeepEqual(docs.Routing.Locations, []model.Location{{Path: "/legacy/files", Kind: "static", Root: `C:\share\files`, StripPrefix: true}}) {
		t.Errorf("docs locations %+v", docs.Routing.Locations)
	}
	if !hasNote(docsItem, model.NoteSkipped, "/legacy", "PHP") || !hasNote(docsItem, model.NoteSkipped, "NetworkService") {
		t.Errorf("docs notes:\n%s", notes(docsItem))
	}

	gw := chosen(find(t, pv, "iis-api gateway")).Site
	if gw.Type != model.SiteProxy || gw.Proxy.Upstreams[0].URL != "http://localhost:3000" || len(gw.Routing.Rewrites) != 0 {
		t.Errorf("ARR site: %+v %+v", gw, gw.Proxy)
	}
	old := chosen(find(t, pv, "iis-old")).Site
	if old.Type != model.SiteRedirect || old.Redirect.TargetURL != "https://new.example.com" || old.Redirect.StatusCode != 301 {
		t.Errorf("redirect site: %+v", old.Redirect)
	}

	// iisnode configured in applicationHost.config's <location>, and a
	// path variable of another machine.
	locItem := find(t, pv, "iis-located")
	loc := chosen(locItem).Site
	if loc.Type != model.SiteNode || loc.Node.Script != "app.js" || loc.Node.Instances != 2 {
		t.Errorf("located: %+v", loc.Node)
	}
	if !hasNote(locItem, model.NoteApproximated, "%DATA_DRIVE%") {
		t.Errorf("located notes:\n%s", notes(locItem))
	}

	for _, it := range pv.Items {
		if len(it.Conflicts) != 0 {
			t.Errorf("%s: conflicts %v", it.Key, it.Conflicts)
		}
	}
}

func TestIISConflicts(t *testing.T) {
	existing := &model.Site{ID: "x", Name: "shop", Type: model.SiteRedirect,
		Bindings: []model.Binding{{Protocol: "http", Port: 80, Host: "shop.example.com"}},
		Redirect: &model.RedirectConfig{TargetURL: "https://x", StatusCode: 301}}
	pv := preview(t, model.ImportIIS, "iis/applicationHost.config", Options{Existing: []*model.Site{existing}})
	shop := find(t, pv, "iis-shop")
	if shop.Selected || len(shop.Conflicts) != 2 ||
		!strings.Contains(shop.Conflicts[0], `a site named "Shop" already exists`) || !strings.Contains(shop.Conflicts[1], "already used by site") {
		t.Fatalf("shop conflicts: %v (selected %v)", shop.Conflicts, shop.Selected)
	}
	// Bindings of items in the same import conflict with each other too:
	// Default Web Site is not selected, so it does not block others.
	for _, it := range pv.Items {
		if it.Key != "iis-shop" && len(it.Conflicts) > 0 {
			t.Errorf("%s: %v", it.Key, it.Conflicts)
		}
	}
}

func TestIISRejectsOtherFiles(t *testing.T) {
	for _, text := range []string{"<configuration/>", "not xml at all <", `{"apps":[]}`} {
		if _, err := Preview(model.ImportIIS, []byte(text), "x", Options{}); err == nil {
			t.Errorf("%q accepted", text)
		}
	}
}

func TestParseBindingInfo(t *testing.T) {
	for _, tc := range []struct {
		in   string
		ip   string
		port int
		host string
		ok   bool
	}{
		{"*:80:", "", 80, "", true},
		{"*:443:Shop.Example.com", "", 443, "shop.example.com", true},
		{"192.168.1.10:8080:", "192.168.1.10", 8080, "", true},
		{"[2001:db8::1]:443:v6.example.com", "2001:db8::1", 443, "v6.example.com", true},
		{"*:0:", "", 0, "", false},
		{"808:*", "", 0, "", false},
		{"bad:80:", "", 0, "", false},
	} {
		ip, port, host, err := parseBindingInfo(tc.in)
		if (err == nil) != tc.ok || (tc.ok && (ip != tc.ip || port != tc.port || host != tc.host)) {
			t.Errorf("%q = %q %d %q %v", tc.in, ip, port, host, err)
		}
	}
}

func TestIISNodeWebConfig(t *testing.T) {
	t.Run("azure template", func(t *testing.T) {
		pv := preview(t, model.ImportWebConfig, "webconfig/azure.web.config", Options{Name: "portal", AppRoot: `C:\apps\portal`})
		it := pv.Items[0]
		s := chosen(it).Site
		if s.Name != "portal" || s.Node.AppRoot != `C:\apps\portal` || s.Node.Script != "server.js" || s.Node.NodeVersion != "18.17.0" || !it.Selected {
			t.Fatalf("site %+v %+v", s, s.Node)
		}
		secret := map[string]bool{}
		for _, e := range s.Node.Env {
			secret[e.Name] = e.Secret
		}
		if !reflect.DeepEqual(secret, map[string]bool{"API_KEY": true, "DB_PASSWORD": true, "APP_TITLE": false, "DatabaseConnectionString": true}) {
			t.Errorf("env: %+v", s.Node.Env)
		}
		if len(s.Routing.Rewrites) != 1 || s.Routing.Rewrites[0].Name != "StaticContent" {
			t.Errorf("rewrites %+v", s.Routing.Rewrites)
		}
		for _, n := range []struct{ level, text string }{
			{model.NoteConverted, `"NodeInspector", "DynamicContent"`},
			{model.NoteApproximated, "StaticContent"},
			{model.NoteSkipped, "Logging:Level"},
			{model.NoteSkipped, "Connection strings (Main)"},
			{model.NoteSkipped, "debuggingEnabled, loggingEnabled"},
		} {
			if !hasNote(it, n.level, n.text) {
				t.Errorf("no %s note about %q:\n%s", n.level, n.text, notes(it))
			}
		}
	})

	t.Run("command line and location wrapper", func(t *testing.T) {
		pv := preview(t, model.ImportWebConfig, "webconfig/cmdline.web.config", Options{CPUs: 6, AppRoot: `C:\apps\x`, NodeInstalled: func(v string) bool { return false }})
		it := pv.Items[0]
		n := chosen(it).Site.Node
		if n.Script != "dist/main.js" || n.Instances != 6 || n.NodeVersion != "20.11.1" || !reflect.DeepEqual(n.NodeArgs, []string{"--max-old-space-size=4096"}) || n.ShutdownTimeoutSec != 60 {
			t.Fatalf("node %+v", n)
		}
		if len(n.Env) != 1 || n.Env[0].Name != "NODE_ENV" || n.Env[0].Value != "staging" {
			t.Errorf("env %+v", n.Env)
		}
		if !hasNote(it, model.NoteApproximated, "20.11.1", "not installed") || !hasNote(it, model.NoteSkipped, "iisnode.yml") || !hasNote(it, model.NoteApproximated, "one process per CPU", "6 instances") {
			t.Errorf("notes:\n%s", notes(it))
		}
	})

	t.Run("httpPlatformHandler", func(t *testing.T) {
		pv := preview(t, model.ImportWebConfig, "webconfig/httpplatform.web.config", Options{AppRoot: `C:\apps\p`})
		n := chosen(pv.Items[0]).Site.Node
		if n.Script != "app.js" || !reflect.DeepEqual(n.Args, []string{"--color"}) || n.NodeVersion != "22.3.0" || len(n.Env) != 1 || n.Env[0].Name != "FEATURE_FLAGS" {
			t.Fatalf("node %+v", n)
		}
	})

	t.Run("no application folder is a conflict", func(t *testing.T) {
		pv := preview(t, model.ImportWebConfig, "webconfig/httpplatform.web.config", Options{})
		if it := pv.Items[0]; it.Selected || len(it.Conflicts) != 1 || !strings.Contains(it.Conflicts[0], "node.appRoot") {
			t.Fatalf("conflicts %v", it.Conflicts)
		}
	})

	t.Run("not Node.js", func(t *testing.T) {
		data, _ := os.ReadFile("testdata/webconfig/aspnet.web.config")
		if _, err := Preview(model.ImportWebConfig, data, "web.config", Options{}); err == nil || !strings.Contains(err.Error(), "does not run Node.js") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestPM2Ecosystem(t *testing.T) {
	pv := preview(t, model.ImportPM2, "pm2/ecosystem.config.js", Options{})
	if len(pv.Items) != 4 {
		t.Fatalf("%d items", len(pv.Items))
	}

	apiItem := find(t, pv, "pm2-api")
	api := chosen(apiItem)
	if api.Label != "Node.js application" || apiItem.Options[1].Site.Type != model.SiteWorker {
		t.Fatalf("api options: %+v", apiItem.Options)
	}
	n := api.Site.Node
	if n.AppRoot != `C:\apps\contoso` || n.Script != "dist/server.js" || n.Instances != 8 || n.Recycle.MemoryLimitMB != 300 ||
		!reflect.DeepEqual(n.Recycle.ScheduleTimes, []string{"03:00"}) || !n.WatchFiles || n.ShutdownTimeoutSec != 8 ||
		n.MaxRestarts != 10 || n.RestartWindowSec != 100 || !reflect.DeepEqual(n.NodeArgs, []string{"--max-old-space-size=512"}) {
		t.Fatalf("api node %+v", n)
	}
	// env_production over env; PORT dropped; NODE_ENV=production is the default.
	if !reflect.DeepEqual(n.Env, []model.EnvVar{{Name: "LOG_LEVEL", Value: "info"}, {Name: "STRIPE_SECRET_KEY", Value: "sk_live_Abc", Secret: true}}) {
		t.Errorf("api env %+v", n.Env)
	}
	if !hasNote(apiItem, model.NoteConverted, "PORT=3000") || !hasNote(apiItem, model.NoteConverted, "env_production") || !hasNote(apiItem, model.NoteSkipped, "env_staging") {
		t.Errorf("api notes:\n%s", notes(apiItem))
	}

	w := find(t, pv, "pm2-queue-worker")
	if s := chosen(w).Site; s.Type != model.SiteWorker || s.Node.Instances != 2 || s.Node.HealthCheck.Enabled || len(s.Bindings) != 0 {
		t.Errorf("worker: %+v %+v", s, s.Node)
	}

	c := find(t, pv, "pm2-cleanup")
	opt := chosen(c)
	if opt.Kind != model.ImportKindTask || opt.TaskSite != "import:pm2-api" || opt.Task.Schedule != "*/30 * * * *" ||
		opt.Task.Script != `C:\apps\contoso\jobs\cleanup.js` || !c.Selected || len(c.Options) != 3 {
		t.Fatalf("cron app: %+v %+v", opt, opt.Task)
	}

	py := find(t, pv, "pm2-reports")
	if py.Selected || !hasNote(py, model.NoteSkipped, "not JavaScript") {
		t.Errorf("python app: selected %v\n%s", py.Selected, notes(py))
	}
}

func TestPM2Variants(t *testing.T) {
	t.Run("array export, npm, nvm interpreter, instances -1", func(t *testing.T) {
		pv := preview(t, model.ImportPM2, "pm2/array.config.cjs", Options{CPUs: 4})
		n := chosen(pv.Items[0]).Site.Node
		if n.AppRoot != "/srv/site" || n.NpmScript != "start:prod" || n.Script != "" || n.NodeVersion != "20.11.1" || n.Instances != 3 {
			t.Fatalf("node %+v", n)
		}
	})
	t.Run("json", func(t *testing.T) {
		pv := preview(t, model.ImportPM2, "pm2/ecosystem.json", Options{})
		it := pv.Items[0]
		n := chosen(it).Site.Node
		if n.AppRoot != "/srv/web" || n.Script != "index.js" || n.Recycle.MemoryLimitMB != 512 || n.StartupTimeoutSec != 15 || len(n.Recycle.ScheduleTimes) != 0 {
			t.Fatalf("node %+v", n)
		}
		if !hasNote(it, model.NoteSkipped, "bad-name") || !hasNote(it, model.NoteSkipped, "*/15 * * * *", "not a daily time") {
			t.Errorf("notes:\n%s", notes(it))
		}
	})
	t.Run("pm2 jlist", func(t *testing.T) {
		pv := preview(t, model.ImportPM2, "pm2/jlist.json", Options{})
		it := pv.Items[0]
		n := chosen(it).Site.Node
		if n.AppRoot != "/srv/api" || n.Script != "src/index.js" || n.NodeVersion != "18.19.0" || n.Instances != 2 ||
			!reflect.DeepEqual(n.Args, []string{"--verbose"}) || !reflect.DeepEqual(n.NodeArgs, []string{"--enable-source-maps"}) {
			t.Fatalf("node %+v", n)
		}
		names := map[string]bool{}
		for _, e := range n.Env {
			names[e.Name] = e.Secret
		}
		// The machine's variables are gone; a URL with a password is secret.
		if !reflect.DeepEqual(names, map[string]bool{"API_TOKEN": true, "DATABASE_URL": true, "FEATURE_X": false}) {
			t.Errorf("env %+v", n.Env)
		}
		// PM2's own defaults are not carried over.
		if n.MaxRestarts != 0 || n.ShutdownTimeoutSec != 0 {
			t.Errorf("pm2 defaults imported: %+v", n)
		}
	})
}

func TestEcosystemLiterals(t *testing.T) {
	good := map[string]any{
		`module.exports = {apps: [{name: 'a', script: "b.js", n: -1.5, h: 0x10, ok: true, no: null, u: undefined,}],}`: map[string]any{
			"apps": []any{map[string]any{"name": "a", "script": "b.js", "n": -1.5, "h": 16.0, "ok": true, "no": nil, "u": nil}},
		},
		"'use strict'\n// c\n/* d */ module.exports = [`t\\`x`, 'it\\'s', \"\\u00e9\\n\"];": []any{"t`x", "it's", "é\n"},
		`export default { "quoted-key": 1, 2: 'two' }`:                                      map[string]any{"quoted-key": 1.0, "2": "two"},
		// pm2 prettylist prints long strings joined with +.
		"[ { name: 'x', script: 'a' +\n   'b.js' } ]": []any{map[string]any{"name": "x", "script": "ab.js"}},
	}
	for src, want := range good {
		got, err := parseEcosystem(src)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("parseEcosystem(%q) = %#v, %v", src, got, err)
		}
	}

	bad := map[string]string{
		"module.exports = {apps: [{name: 'a', env: {PORT: process.env.PORT}}]}": "line 1: process.env.PORT is not a plain value",
		"const base = {}\nmodule.exports = {apps: [base]}":                      "does not start with module.exports",
		"module.exports = {apps: [{...base}]}":                                  "spread",
		"module.exports = {apps: require('./apps')}":                            "require(…) is not a plain value",
		"module.exports = {\n apps: [{ name: `api-${env}` }]}":                  "line 2: a template string with ${…}",
		"module.exports = { apps() { return [] } }":                             "apps is a function",
		"module.exports = { apps }":                                             "apps is not a plain value",
		"module.exports = { a: 1 }; console.log('hi')":                          "unexpected",
		"module.exports = { a: 'x' + b }":                                       "only strings can be joined",
		"module.exports = { a: 'unterminated }":                                 "unterminated string",
	}
	for src, want := range bad {
		_, err := parseEcosystem(src)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("parseEcosystem(%q) = %v, want an error containing %q", src, err, want)
			continue
		}
		if !strings.Contains(err.Error(), "pm2 jlist") && !strings.Contains(err.Error(), "unterminated") {
			t.Errorf("%q: the error does not say how to export JSON instead: %v", src, err)
		}
	}

	// Through Preview: a .js that is code is refused, never run.
	if _, err := Preview(model.ImportPM2, []byte("module.exports = require('./x')"), "ecosystem.config.js", Options{}); err == nil || !strings.Contains(err.Error(), "pm2 jlist") {
		t.Errorf("Preview of code: %v", err)
	}
	if _, err := Preview(model.ImportPM2, []byte(`{"apps": [`), "apps.json", Options{}); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Errorf("Preview of bad JSON: %v", err)
	}
	if _, err := Preview(model.ImportPM2, []byte(`{"version": 1}`), "apps.json", Options{}); err == nil || !strings.Contains(err.Error(), "no PM2 apps") {
		t.Errorf("Preview without apps: %v", err)
	}
}

func TestHelpers(t *testing.T) {
	for in, want := range map[any]int{"300M": 300, "1G": 1024, "512K": 1, "2gb": 2048, 536870912.0: 512, "1.5G": 1536} {
		if got, ok := memoryMB(in); !ok || got != want {
			t.Errorf("memoryMB(%v) = %d, %v", in, got, ok)
		}
	}
	for in, want := range map[string]string{"0 3 * * *": "03:00", "30 23 * * *": "23:30", "0 15 4 * * *": "04:15"} {
		if got, ok := dailyTime(in); !ok || got != want {
			t.Errorf("dailyTime(%q) = %q, %v", in, got, ok)
		}
	}
	for _, in := range []string{"*/5 * * * *", "0 3 * * 1", "0 3 1 * *", "61 3 * * *"} {
		if _, ok := dailyTime(in); ok {
			t.Errorf("dailyTime(%q) accepted", in)
		}
	}
	for in, want := range map[string]string{
		`C:\Program Files\nodejs\18.17.0\node.exe`:            "18.17.0",
		"/home/u/.nvm/versions/node/v20.11.1/bin/node":        "20.11.1",
		`C:\nvm\v22.3.0\node.exe`:                             "22.3.0",
		`C:\Program Files\nodejs\node.exe`:                    "",
		`"C:\Program Files (x86)\iisnode\node.exe" --inspect`: "",
	} {
		if got := nodeVersionIn(in); got != want {
			t.Errorf("nodeVersionIn(%q) = %q, want %q", in, got, want)
		}
	}
	if got := splitArgs(`"C:\Program Files\node.exe" --a 'b c' d`); !reflect.DeepEqual(got, []string{`C:\Program Files\node.exe`, "--a", "b c", "d"}) {
		t.Errorf("splitArgs = %q", got)
	}
	if got := siteName("  ../Shop: Main*Site  "); got != "Shop- Main-Site" {
		t.Errorf("siteName = %q", got)
	}
}
