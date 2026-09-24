package importer

import (
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/rewrite"
)

// webConfig is what matters in a web.config (or in a <location> of
// applicationHost.config) for hosting: the handler that runs the site,
// iisnode's settings, app settings, redirects and URL Rewrite.
type webConfig struct {
	text        []string // the documents, for the rewrite importer
	handlers    []wcHandler
	iisnode     map[string]string // attribute -> value
	httpPlat    *wcHTTPPlatform
	appSettings [][2]string
	connStrings []string
	aspNet      []string // ASP.NET's own settings found (<system.web>)
	redirect    *wcRedirect
	defaultDocs []string
	dirBrowse   *bool
	mimeMaps    []model.MimeMap
}

type wcHandler struct {
	Name         string `xml:"name,attr"`
	Path         string `xml:"path,attr"`
	Modules      string `xml:"modules,attr"`
	ScriptProc   string `xml:"scriptProcessor,attr"`
	ResourceType string `xml:"resourceType,attr"`
	Type         string `xml:"type,attr"` // a managed (.NET) handler
}

func (h wcHandler) is(module string) bool {
	return strings.EqualFold(strings.TrimSpace(h.Modules), module)
}

type wcHTTPPlatform struct {
	ProcessPath string `xml:"processPath,attr"`
	Arguments   string `xml:"arguments,attr"`
	Env         []struct {
		Name  string `xml:"name,attr"`
		Value string `xml:"value,attr"`
	} `xml:"environmentVariables>environmentVariable"`
}

type wcRedirect struct {
	Enabled     string `xml:"enabled,attr"`
	Destination string `xml:"destination,attr"`
	Exact       string `xml:"exactDestination,attr"`
	Status      string `xml:"httpResponseStatus,attr"`
}

// parseWebConfig reads the settings wherever they are nested (a
// web.config may wrap them in <location path=".">). Later documents
// override earlier ones, as a site's web.config overrides the server's
// <location> for it.
func parseWebConfig(wc *webConfig, text string) error {
	d := xml.NewDecoder(strings.NewReader(text))
	d.Strict = false
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("not valid XML: %v", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		var derr error
		switch se.Name.Local {
		case "handlers":
			var h struct {
				Clear *struct{}   `xml:"clear"`
				Add   []wcHandler `xml:"add"`
				Rem   []struct {
					Name string `xml:"name,attr"`
				} `xml:"remove"`
			}
			derr = d.DecodeElement(&h, &se)
			if h.Clear != nil {
				wc.handlers = nil
			}
			for _, r := range h.Rem {
				wc.handlers = slicesDeleteFunc(wc.handlers, func(x wcHandler) bool { return strings.EqualFold(x.Name, r.Name) })
			}
			wc.handlers = append(wc.handlers, h.Add...)
		case "iisnode":
			var n struct {
				Attrs []xml.Attr `xml:",any,attr"`
			}
			derr = d.DecodeElement(&n, &se)
			if wc.iisnode == nil {
				wc.iisnode = map[string]string{}
			}
			for _, a := range n.Attrs {
				wc.iisnode[a.Name.Local] = a.Value
			}
		case "httpPlatform", "aspNetCore":
			var p wcHTTPPlatform
			derr = d.DecodeElement(&p, &se)
			wc.httpPlat = &p
		case "appSettings":
			var a struct {
				Add []struct {
					Key   string `xml:"key,attr"`
					Value string `xml:"value,attr"`
				} `xml:"add"`
			}
			derr = d.DecodeElement(&a, &se)
			for _, x := range a.Add {
				wc.appSettings = append(wc.appSettings, [2]string{x.Key, x.Value})
			}
		case "connectionStrings":
			var c struct {
				Add []struct {
					Name string `xml:"name,attr"`
				} `xml:"add"`
			}
			derr = d.DecodeElement(&c, &se)
			for _, x := range c.Add {
				wc.connStrings = append(wc.connStrings, x.Name)
			}
		case "compilation", "httpRuntime", "machineKey":
			// Only ASP.NET reads these (<system.web>); their children
			// are scanned like any other element.
			if !slices.Contains(wc.aspNet, "<"+se.Name.Local+">") {
				wc.aspNet = append(wc.aspNet, "<"+se.Name.Local+">")
			}
		case "httpRedirect":
			var r wcRedirect
			derr = d.DecodeElement(&r, &se)
			wc.redirect = &r
		case "defaultDocument":
			var dd struct {
				Enabled string `xml:"enabled,attr"`
				Files   []struct {
					Value string `xml:"value,attr"`
				} `xml:"files>add"`
			}
			derr = d.DecodeElement(&dd, &se)
			for _, f := range dd.Files {
				wc.defaultDocs = append(wc.defaultDocs, f.Value)
			}
		case "directoryBrowse":
			var db struct {
				Enabled string `xml:"enabled,attr"`
			}
			derr = d.DecodeElement(&db, &se)
			v := strings.EqualFold(db.Enabled, "true")
			wc.dirBrowse = &v
		case "staticContent":
			var sc struct {
				Maps []struct {
					Ext  string `xml:"fileExtension,attr"`
					Type string `xml:"mimeType,attr"`
				} `xml:"mimeMap"`
			}
			derr = d.DecodeElement(&sc, &se)
			for _, m := range sc.Maps {
				wc.mimeMaps = append(wc.mimeMaps, model.MimeMap{Extension: strings.ToLower(m.Ext), Type: m.Type})
			}
		default:
			continue
		}
		if derr != nil {
			return fmt.Errorf("not valid XML: %v", derr)
		}
	}
	wc.text = append(wc.text, text)
	return nil
}

func slicesDeleteFunc[T any](s []T, del func(T) bool) []T {
	out := s[:0]
	for _, x := range s {
		if !del(x) {
			out = append(out, x)
		}
	}
	return out
}

// iisnodeHandler is the handler that runs the application through
// iisnode, if any.
func (wc *webConfig) iisnodeHandler() *wcHandler {
	for i := range wc.handlers {
		if wc.handlers[i].is("iisnode") {
			return &wc.handlers[i]
		}
	}
	return nil
}

// nodePlatform is an httpPlatformHandler or ASP.NET Core Module that
// starts node.exe, the other way IIS hosts Node.js.
func (wc *webConfig) nodePlatform() (script string, args []string, ok bool) {
	p := wc.httpPlat
	if p == nil || !strings.Contains(strings.ToLower(path.Base(strings.ReplaceAll(p.ProcessPath, `\`, "/"))), "node") {
		return "", nil, false
	}
	for i, a := range splitArgs(p.Arguments) {
		if strings.HasSuffix(strings.ToLower(a), ".js") || strings.HasSuffix(strings.ToLower(a), ".mjs") || strings.HasSuffix(strings.ToLower(a), ".cjs") {
			rest := splitArgs(p.Arguments)[i+1:]
			return strings.TrimPrefix(strings.TrimPrefix(a, `.\`), "./"), rest, true
		}
	}
	return "", nil, false
}

// codeHandler names a handler that runs code NodeHoster cannot (ASP.NET
// Core, PHP, CGI…): such a site must not be served as static files.
func (wc *webConfig) codeHandler() string {
	for _, h := range wc.handlers {
		switch {
		case h.is("iisnode"):
		case h.Type != "":
			return "ASP.NET (the handler " + h.Name + ")"
		case h.is("AspNetCoreModule"), h.is("AspNetCoreModuleV2"):
			return "ASP.NET Core"
		case h.is("FastCgiModule"):
			if strings.Contains(strings.ToLower(h.ScriptProc), "php") {
				return "PHP"
			}
			return "FastCGI (" + h.ScriptProc + ")"
		case h.is("CgiModule"), h.is("IsapiModule"):
			return h.Modules + " (" + h.Name + ")"
		case h.is("httpPlatformHandler"):
			if _, _, ok := wc.nodePlatform(); !ok && wc.httpPlat != nil {
				return "httpPlatformHandler (" + wc.httpPlat.ProcessPath + ")"
			}
		}
	}
	if wc.httpPlat != nil {
		if _, _, ok := wc.nodePlatform(); !ok && wc.httpPlat.ProcessPath != "" {
			return wc.httpPlat.ProcessPath
		}
	}
	return ""
}

// rewrites imports the URL Rewrite rules of all documents.
func (wc *webConfig) rewrites() (model.RewriteImport, bool) {
	var all model.RewriteImport
	found := false
	for _, t := range wc.text {
		if !strings.Contains(t, "<rewrite") && !strings.Contains(t, "<rules") {
			continue
		}
		imp, err := rewrite.Import(model.RewriteImportRequest{Format: "webconfig", Text: t})
		if err != nil {
			continue
		}
		found = true
		all.Rules = append(all.Rules, imp.Rules...)
		all.OutboundRules = append(all.OutboundRules, imp.OutboundRules...)
		all.RewriteMaps = append(all.RewriteMaps, imp.RewriteMaps...)
		for _, w := range imp.Warnings {
			if w != "No rewrite rules were found." {
				all.Warnings = append(all.Warnings, w)
			}
		}
	}
	return all, found && len(all.Rules)+len(all.OutboundRules)+len(all.RewriteMaps) > 0
}

// ---- iisnode

// isEntryRule recognizes the rules every iisnode template has: send
// everything that is not a file to the entry script, and the node-inspector
// debugging URL. NodeHoster sends every request to the application anyway.
func isEntryRule(r model.RewriteRule, entry string) bool {
	e := strings.ToLower(strings.TrimPrefix(entry, "/"))
	t := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(r.Target), "/"))
	if r.Action == "rewrite" && e != "" && (t == e || strings.HasPrefix(t, e+"/") || strings.HasPrefix(t, e+"?")) {
		return true
	}
	if e == "" {
		return false
	}
	m := strings.ToLower(r.Match)
	return strings.Contains(m, strings.ReplaceAll(e, ".", `\.`)+`\/debug`) || strings.Contains(m, e+`\/debug`)
}

// applyIISNode fills a node site from iisnode's settings.
func applyIISNode(it *item, s *model.Site, wc *webConfig, opts Options) {
	n := s.Node
	a := wc.iisnode
	get := func(k string) (string, bool) {
		for name, v := range a {
			if strings.EqualFold(name, k) {
				return strings.TrimSpace(v), true
			}
		}
		return "", false
	}
	used := map[string]bool{}
	use := func(k string) (string, bool) {
		v, ok := get(k)
		if ok {
			used[strings.ToLower(k)] = true
		}
		return v, ok
	}

	if v, ok := use("nodeProcessCountPerApplication"); ok {
		switch c, err := strconv.Atoi(v); {
		case err != nil:
			it.skipped("nodeProcessCountPerApplication %q is not a number.", v)
		case c == 0:
			n.Instances = min(opts.CPUs, 64)
			it.approximated("nodeProcessCountPerApplication=0 (one process per CPU): %d instances, this server's CPU count.", n.Instances)
		default:
			n.Instances = min(max(c, 1), 64)
			it.converted("nodeProcessCountPerApplication=%d: %d instances, load-balanced by NodeHoster.", c, n.Instances)
		}
	}
	if v, ok := use("nodeProcessCommandLine"); ok && v != "" {
		parts := splitArgs(v)
		exe := parts[0]
		it.nodeVersion(n, nodeVersionIn(exe), "nodeProcessCommandLine", opts)
		var nodeArgs, other []string
		for _, p := range parts[1:] {
			if strings.HasPrefix(p, "-") {
				nodeArgs = append(nodeArgs, p)
			} else {
				other = append(other, p)
			}
		}
		if len(nodeArgs) > 0 {
			n.NodeArgs = nodeArgs
			it.converted("Node.js options %s.", strings.Join(nodeArgs, " "))
		}
		if len(other) > 0 {
			it.skipped("nodeProcessCommandLine arguments %q were not recognized.", strings.Join(other, " "))
		}
		if n.NodeVersion == "" && len(nodeArgs) == 0 && !strings.Contains(strings.ToLower(exe), "node") {
			it.skipped("nodeProcessCommandLine %q does not look like node.exe; NodeHoster runs its own Node.js versions.", v)
		} else if n.NodeVersion == "" {
			it.approximated("node.exe from %s: the server's default Node.js version is used instead.", exe)
		}
	}
	if v, ok := use("watchedFiles"); ok && v != "" {
		n.WatchFiles = true
		it.approximated("watchedFiles %q: the whole application folder is watched (except node_modules, .git and logs) and a change recycles the site.", v)
	}
	if v, ok := use("gracefulShutdownTimeout"); ok {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			n.ShutdownTimeoutSec = max((ms+999)/1000, 1)
			it.converted("gracefulShutdownTimeout: %d s shutdown timeout.", n.ShutdownTimeoutSec)
		}
	}
	if v, ok := use("node_env"); ok && v != "" && !strings.EqualFold(v, "production") {
		setEnv(n, "NODE_ENV", v)
		it.converted("node_env: NODE_ENV=%s.", v)
	}
	if v, ok := use("configOverrides"); ok && v != "" {
		it.skipped("configOverrides=%q: settings in that file (iisnode.yml) were not read; check them by hand.", v)
	}
	var ignored []string
	for _, k := range sortedKeys(a) {
		if !used[strings.ToLower(k)] {
			ignored = append(ignored, k)
		}
	}
	if len(ignored) > 0 {
		it.skipped("iisnode settings with no NodeHoster equivalent were ignored: %s.", strings.Join(ignored, ", "))
	}
}

// applyAppSettings turns <appSettings> into variables: iisnode passes them
// to the application as environment variables.
func applyAppSettings(it *item, n *model.NodeConfig, wc *webConfig, opts Options) {
	var secrets, bad []string
	count := 0
	for _, kv := range wc.appSettings {
		if strings.EqualFold(kv[0], "WEBSITE_NODE_DEFAULT_VERSION") { // Azure App Service
			it.nodeVersion(n, strings.TrimPrefix(strings.TrimPrefix(kv[1], "~"), "v"), "WEBSITE_NODE_DEFAULT_VERSION", opts)
			continue
		}
		e, ok := envVar(kv[0], kv[1])
		if !ok {
			bad = append(bad, kv[0])
			continue
		}
		setEnvVar(n, e)
		count++
		if e.Secret {
			secrets = append(secrets, e.Name)
		}
	}
	if count > 0 {
		it.converted("%d app setting(s) became environment variables.", count)
	}
	if len(secrets) > 0 {
		it.converted("Stored encrypted as secrets (by their names): %s. Check the others on the Environment tab.", strings.Join(secrets, ", "))
	}
	if len(bad) > 0 {
		it.skipped("App settings whose names are not valid variable names were left out: %s.", strings.Join(bad, ", "))
	}
	if len(wc.connStrings) > 0 {
		it.skipped("Connection strings (%s) are not passed to Node.js by iisnode and were not imported; add them as secret variables if the app reads them.", strings.Join(wc.connStrings, ", "))
	}
}

func setEnv(n *model.NodeConfig, name, value string) {
	setEnvVar(n, model.EnvVar{Name: name, Value: value})
}

func setEnvVar(n *model.NodeConfig, e model.EnvVar) {
	for i := range n.Env {
		if strings.EqualFold(n.Env[i].Name, e.Name) {
			n.Env[i] = e
			return
		}
	}
	n.Env = append(n.Env, e)
}

// applyRewrites adds the URL Rewrite rules to a draft. For a Node.js site
// the rules that only route to the entry script are dropped.
func applyRewrites(it *item, s *model.Site, wc *webConfig, entry string) {
	imp, ok := wc.rewrites()
	if !ok {
		return
	}
	rules := imp.Rules[:0]
	var dropped []string
	for _, r := range imp.Rules {
		if s.Type == model.SiteNode && isEntryRule(r, entry) {
			dropped = append(dropped, r.Name)
			continue
		}
		rules = append(rules, r)
	}
	if len(dropped) > 0 {
		it.converted("Rewrite rule(s) %s only sent requests to %s, as iisnode needs; NodeHoster sends every request to the application, so they were left out.", quoteList(dropped), entry)
	}
	s.Routing.Rewrites = append(s.Routing.Rewrites, rules...)
	s.Routing.OutboundRules = append(s.Routing.OutboundRules, imp.OutboundRules...)
	s.Routing.RewriteMaps = append(s.Routing.RewriteMaps, imp.RewriteMaps...)
	if n := len(rules) + len(imp.OutboundRules); n > 0 {
		it.converted("%d URL Rewrite rule(s) imported; check them on the Routing tab.", n)
	}
	if s.Type == model.SiteNode {
		for _, r := range rules {
			if r.Action == "rewrite" && !strings.Contains(r.Target, "://") {
				it.approximated("Rewrite rule %q rewrites to %q: in IIS a rewrite to a file lets IIS serve it; here the rewritten request goes to the application. Disable the rule if the app does not serve that path.", r.Name, r.Target)
			}
		}
	}
	for _, w := range imp.Warnings {
		it.skipped("%s", w)
	}
}

func quoteList(list []string) string {
	q := make([]string, len(list))
	for i, s := range list {
		q[i] = strconv.Quote(s)
	}
	return strings.Join(q, ", ")
}

// previewWebConfig proposes a Node.js site from an iisnode application's
// web.config.
func previewWebConfig(pv *model.ImportPreview, data []byte, opts Options) error {
	var wc webConfig
	if err := parseWebConfig(&wc, string(data)); err != nil {
		return err
	}
	name := opts.Name
	if name == "" {
		name = "iisnode-app"
	}
	it := newItem("webconfig", "web.config")
	s, ok := nodeFromWebConfig(it, &wc, name, opts.AppRoot, opts)
	if !ok {
		return fmt.Errorf("this web.config does not run Node.js: it has no iisnode handler (<add modules=\"iisnode\" …/>) or httpPlatformHandler starting node.exe")
	}
	if opts.AppRoot == "" {
		it.skipped("The application folder is not known from a web.config: enter it before importing.")
	}
	it.addSite("Node.js application", s)
	pv.Items = append(pv.Items, it.ImportItem)
	return nil
}

// nodeFromWebConfig builds a node site if the configuration runs Node.js.
func nodeFromWebConfig(it *item, wc *webConfig, name, appRoot string, opts Options) (*model.Site, bool) {
	var s *model.Site
	entry := ""
	if h := wc.iisnodeHandler(); h != nil {
		entry = strings.TrimPrefix(strings.TrimPrefix(strings.ReplaceAll(h.Path, `\`, "/"), "./"), "/")
		if entry == "" || strings.ContainsAny(entry, "*?") {
			it.approximated("The iisnode handler's path is %q, not one script: entry script set to server.js; change it if needed.", h.Path)
			entry = "server.js"
		} else {
			it.converted("iisnode application: entry script %s.", entry)
		}
		s = nodeSite(name, appRoot, entry)
		if wc.iisnode != nil {
			applyIISNode(it, s, wc, opts)
		}
	} else if script, args, ok := wc.nodePlatform(); ok {
		entry = script
		s = nodeSite(name, appRoot, script)
		s.Node.Args = args
		it.converted("httpPlatformHandler running node.exe: entry script %s.", script)
		it.nodeVersion(s.Node, nodeVersionIn(wc.httpPlat.ProcessPath), "processPath", opts)
		for _, e := range wc.httpPlat.Env {
			if strings.EqualFold(e.Name, "PORT") || strings.Contains(e.Value, "HTTP_PLATFORM_PORT") || strings.Contains(e.Value, "ASPNETCORE_PORT") {
				continue // NodeHoster sets PORT itself
			}
			if v, ok := envVar(e.Name, e.Value); ok {
				setEnvVar(s.Node, v)
			}
		}
	} else {
		return nil, false
	}
	applyAppSettings(it, s.Node, wc, opts)
	applyRewrites(it, s, wc, entry)
	it.converted("Health checks are on (a request to / every 30 s; anything below 500 is healthy); turn them off on the Settings tab if / is slow or protected.")
	s.Node.HealthCheck.Enabled = true
	return s, true
}
