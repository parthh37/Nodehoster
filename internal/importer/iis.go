package importer

import (
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"

	"github.com/parthh37/nodehoster/internal/model"
)

// ---- applicationHost.config

type ahConfig struct {
	Pools     []ahPool     `xml:"system.applicationHost>applicationPools>add"`
	PoolDefs  ahPool       `xml:"system.applicationHost>applicationPools>applicationPoolDefaults"`
	Sites     []ahSite     `xml:"system.applicationHost>sites>site"`
	AppDefs   ahApp        `xml:"system.applicationHost>sites>applicationDefaults"`
	Locations []ahLocation `xml:"location"`
	WebServer struct {
		Inner string `xml:",innerxml"`
	} `xml:"system.webServer"`
}

type ahPool struct {
	Name         string `xml:"name,attr"`
	ProcessModel struct {
		IdentityType string `xml:"identityType,attr"`
		UserName     string `xml:"userName,attr"`
	} `xml:"processModel"`
}

type ahSite struct {
	Name            string      `xml:"name,attr"`
	ID              string      `xml:"id,attr"`
	ServerAutoStart string      `xml:"serverAutoStart,attr"`
	Apps            []ahApp     `xml:"application"`
	Bindings        []ahBinding `xml:"bindings>binding"`
}

type ahApp struct {
	Path  string   `xml:"path,attr"`
	Pool  string   `xml:"applicationPool,attr"`
	VDirs []ahVDir `xml:"virtualDirectory"`
}

type ahVDir struct {
	Path         string `xml:"path,attr"`
	PhysicalPath string `xml:"physicalPath,attr"`
}

type ahBinding struct {
	Protocol        string `xml:"protocol,attr"`
	Info            string `xml:"bindingInformation,attr"`
	SSLFlags        string `xml:"sslFlags,attr"`
	CertificateHash string `xml:"certificateHash,attr"`
}

type ahLocation struct {
	Path  string `xml:"path,attr"`
	Inner string `xml:",innerxml"`
}

func previewIIS(pv *model.ImportPreview, data []byte, opts Options) error {
	var cfg ahConfig
	d := xml.NewDecoder(strings.NewReader(string(data)))
	d.Strict = false
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	if err := d.Decode(&cfg); err != nil {
		return fmt.Errorf("not an applicationHost.config: %v", err)
	}
	if len(cfg.Sites) == 0 {
		return fmt.Errorf("no sites were found; this does not look like IIS's applicationHost.config (in %%windir%%\\System32\\inetsrv\\config)")
	}
	pools := map[string]ahPool{}
	for _, p := range cfg.Pools {
		pools[strings.ToLower(p.Name)] = p
	}
	if arrOnRe.MatchString(cfg.WebServer.Inner) {
		pv.Warnings = append(pv.Warnings, "Application Request Routing (ARR) proxying is on in this IIS: rules that rewrite to http:// URLs were imported and NodeHoster proxies them the same way. ARR's server-wide settings (timeouts, preserveHostHeader, caching) were not.")
	}
	b := &iisBuilder{opts: opts, pools: pools, poolDefs: cfg.PoolDefs, appDefs: cfg.AppDefs, locations: cfg.Locations, keys: map[string]bool{}}
	for _, site := range cfg.Sites {
		b.site(pv, site)
	}
	return nil
}

type iisBuilder struct {
	opts      Options
	pools     map[string]ahPool
	poolDefs  ahPool
	appDefs   ahApp
	locations []ahLocation
	keys      map[string]bool
}

var (
	envVarRe = regexp.MustCompile(`%([^%]+)%`)
	arrOnRe  = regexp.MustCompile(`<proxy\b[^>]*\benabled="true"`)
)

// physical expands %VARIABLES% in a physical path.
func (b *iisBuilder) physical(it *item, p string) string {
	return envVarRe.ReplaceAllStringFunc(p, func(m string) string {
		name := strings.Trim(m, "%")
		if v := b.opts.Getenv(name); v != "" {
			return v
		}
		it.approximated("%s in the path %q could not be resolved; correct the path.", m, p)
		return m
	})
}

// config merges what IIS applies to a site path: the <location> sections
// of applicationHost.config for it, then the web.config in its folder.
func (b *iisBuilder) config(it *item, locPath, dir string) *webConfig {
	wc := &webConfig{}
	for _, l := range b.locations {
		if strings.EqualFold(strings.Trim(l.Path, "/"), strings.Trim(locPath, "/")) {
			parseWebConfig(wc, "<location>"+l.Inner+"</location>")
		}
	}
	if dir != "" {
		file := strings.TrimRight(dir, `\/`) + string(sep(dir)) + "web.config"
		if data, err := b.opts.ReadFile(file); err == nil {
			if err := parseWebConfig(wc, string(data)); err != nil {
				it.skipped("%s could not be read (%v); its settings were not imported.", file, err)
			}
		}
	}
	return wc
}

func sep(p string) rune {
	if strings.Contains(p, `\`) || (len(p) > 1 && p[1] == ':') {
		return '\\'
	}
	return '/'
}

func (b *iisBuilder) site(pv *model.ImportPreview, site ahSite) {
	key := uniqueKey(b.keys, "iis-"+strings.ToLower(siteName(site.Name)))
	it := newItem(key, fmt.Sprintf("IIS site %q", site.Name))
	var root *ahApp
	for i := range site.Apps {
		if site.Apps[i].Path == "/" {
			root = &site.Apps[i]
		}
	}
	if root == nil || rootDir(root) == "" {
		it.skipped("The site has no root application with a physical path.")
		it.Selected = false
		it.addSite("Static site", &model.Site{Name: siteName(site.Name), Type: model.SiteStatic, Static: &model.StaticConfig{}})
		pv.Items = append(pv.Items, it.ImportItem)
		return
	}
	dir := b.physical(it, rootDir(root))
	wc := b.config(it, site.Name, dir)
	s := b.siteFromConfig(it, site.Name, dir, wc)
	s.AutoStart = !strings.EqualFold(site.ServerAutoStart, "false")
	if !s.AutoStart {
		it.converted("serverAutoStart is off: the site does not start with the service.")
	}
	b.bindings(it, s, site.Bindings)
	b.identity(it, root)
	if strings.EqualFold(site.Name, "Default Web Site") && strings.Contains(strings.ToLower(dir), `inetpub\wwwroot`) {
		it.Selected = false
		it.skipped("This is IIS's default site; it is not selected.")
	}

	// Virtual directories of the root application, then the other
	// applications and their virtual directories, become locations.
	for _, v := range root.VDirs {
		if v.Path != "/" {
			b.staticLocation(it, s, v.Path, b.physical(it, v.PhysicalPath), "virtual directory")
		}
	}
	var children []*item
	for _, app := range site.Apps {
		if app.Path == "/" {
			continue
		}
		appDir := b.physical(it, rootDir(&app))
		awc := b.config(it, site.Name+app.Path, appDir)
		if awc.iisnodeHandler() != nil || awc.httpPlat != nil && func() bool { _, _, ok := awc.nodePlatform(); return ok }() {
			child := b.nodeApp(it, site, app, appDir, awc)
			children = append(children, child)
			s.Routing.Locations = append(s.Routing.Locations, model.Location{
				Path: strings.TrimRight(app.Path, "/"), Kind: "site", SiteID: model.ImportRef + child.Key,
			})
			it.converted("Application %s runs Node.js: imported as its own site %q, mounted at %s (it receives the full path, as under iisnode).", app.Path, child.Options[0].Site.Name, app.Path)
		} else if h := awc.codeHandler(); h != "" {
			it.skipped("Application %s runs %s, which NodeHoster does not host; it was left out.", app.Path, h)
		} else if appDir != "" {
			b.staticLocation(it, s, app.Path, appDir, "application")
		}
		for _, v := range app.VDirs {
			if v.Path != "/" {
				b.staticLocation(it, s, strings.TrimRight(app.Path, "/")+v.Path, b.physical(it, v.PhysicalPath), "virtual directory")
			}
		}
	}
	it.addSite(siteLabel(s.Type), s)
	// Mounted applications first: they must exist before the site that
	// mounts them.
	for _, c := range children {
		pv.Items = append(pv.Items, c.ImportItem)
	}
	pv.Items = append(pv.Items, it.ImportItem)
}

func rootDir(app *ahApp) string {
	for _, v := range app.VDirs {
		if v.Path == "/" {
			return v.PhysicalPath
		}
	}
	return ""
}

func siteLabel(t model.SiteType) string {
	switch t {
	case model.SiteNode:
		return "Node.js application"
	case model.SiteWorker:
		return "Background worker"
	case model.SiteProxy:
		return "Reverse proxy"
	case model.SiteRedirect:
		return "Redirect"
	}
	return "Static site"
}

func (b *iisBuilder) staticLocation(it *item, s *model.Site, p, dir, what string) {
	p = strings.TrimRight(p, "/")
	if p == "" || dir == "" {
		return
	}
	s.Routing.Locations = append(s.Routing.Locations, model.Location{Path: p, Kind: "static", Root: dir, StripPrefix: true})
	it.converted("The %s %s became a location serving the files of %s.", what, p, dir)
}

// nodeApp is an iisnode application below a site: its own node site
// without bindings, mounted by the parent.
func (b *iisBuilder) nodeApp(parent *item, site ahSite, app ahApp, dir string, wc *webConfig) *item {
	name := siteName(site.Name + " " + strings.ReplaceAll(strings.Trim(app.Path, "/"), "/", "-"))
	key := uniqueKey(b.keys, parent.Key+"-"+strings.ToLower(siteName(strings.ReplaceAll(strings.Trim(app.Path, "/"), "/", "-"))))
	it := newItem(key, fmt.Sprintf("IIS application %s%s", site.Name, app.Path))
	s, _ := nodeFromWebConfig(it, wc, name, dir, b.opts)
	s.AutoStart = !strings.EqualFold(site.ServerAutoStart, "false")
	it.converted("No bindings: it is reached through %q at %s.", site.Name, app.Path)
	b.identity(it, &app)
	it.addSite("Node.js application", s)
	return it
}

// siteFromConfig decides what a site is from its configuration: an
// iisnode application, a redirect, an ARR reverse proxy or static files.
func (b *iisBuilder) siteFromConfig(it *item, name, dir string, wc *webConfig) *model.Site {
	if s, ok := nodeFromWebConfig(it, wc, name, dir, b.opts); ok {
		return s
	}
	if r := wc.redirect; r != nil && strings.EqualFold(r.Enabled, "true") && r.Destination != "" {
		code := 302
		switch strings.ToLower(r.Status) {
		case "permanent":
			code = 301
		case "temporary":
			code = 307
		case "permredirect":
			code = 308
		}
		s := &model.Site{Name: siteName(name), Type: model.SiteRedirect,
			Redirect: &model.RedirectConfig{TargetURL: r.Destination, StatusCode: code, PreservePath: !strings.EqualFold(r.Exact, "true")}}
		it.converted("HTTP Redirect to %s (%d).", r.Destination, code)
		return s
	}
	if h := wc.codeHandler(); h != "" {
		it.skipped("The site runs %s, which NodeHoster does not host. It is proposed as static files but not selected: serving its folder as files could expose source code.", h)
		it.Selected = false
	}
	if up, ok := arrProxy(wc); ok {
		s := &model.Site{Name: siteName(name), Type: model.SiteProxy,
			Proxy: &model.ProxyConfig{Upstreams: []model.Upstream{{URL: up}}, LoadBalancing: "round_robin"}}
		it.converted("URL Rewrite + ARR forwards every request to %s: imported as a reverse proxy.", up)
		return s
	}
	s := &model.Site{Name: siteName(name), Type: model.SiteStatic, Static: &model.StaticConfig{Root: dir}}
	s.Routing.Compression, s.Routing.AccessLog = true, true
	it.converted("Static files from %s.", dir)
	if len(wc.defaultDocs) > 0 {
		s.Static.IndexFiles = wc.defaultDocs
		it.converted("Default documents: %s.", strings.Join(wc.defaultDocs, ", "))
	}
	if wc.dirBrowse != nil && *wc.dirBrowse {
		s.Static.DirectoryBrowsing = true
		it.converted("Directory browsing is on.")
	}
	if len(wc.mimeMaps) > 0 {
		s.Routing.MimeTypes = wc.mimeMaps
		it.converted("%d MIME type mapping(s).", len(wc.mimeMaps))
	}
	applyRewrites(it, s, wc, "")
	return s
}

// arrProxy recognizes the classic ARR reverse proxy: one enabled rule
// that rewrites everything to the same path on another server.
func arrProxy(wc *webConfig) (string, bool) {
	imp, ok := wc.rewrites()
	if !ok || len(imp.OutboundRules) > 0 {
		return "", false
	}
	var enabled []model.RewriteRule
	for _, r := range imp.Rules {
		if r.Enabled {
			enabled = append(enabled, r)
		}
	}
	if len(enabled) != 1 {
		return "", false
	}
	r := enabled[0]
	if r.Action != "rewrite" || len(r.Conditions) > 0 || r.Negate || r.Host != "" {
		return "", false
	}
	// The rule as the rewrite importer wrote it: IIS's "(.*)", "^(.*)$"…
	// adapted to paths that begin with "/".
	switch strings.TrimSpace(r.Match) {
	case "^/?.*?(?:(.*))", "^/(.*)$", "^/(.*)", "^/?.*?(?:(.*)$)", ".*", "", "^/.*$", "^/?.*?(?:.*)":
	default:
		return "", false
	}
	t := strings.TrimSpace(r.Target)
	for _, suffix := range []string{"/{R:1}", "/{R:0}", "{R:1}", "{R:0}", "/$1", "$1"} {
		if base, ok := strings.CutSuffix(t, suffix); ok {
			if strings.HasPrefix(base, "http://") || strings.HasPrefix(base, "https://") {
				if !strings.Contains(strings.TrimPrefix(strings.TrimPrefix(base, "http://"), "https://"), "/") &&
					!strings.Contains(base, "{") {
					return base, true
				}
			}
		}
	}
	return "", false
}

// bindings converts IIS bindings: "ip:port:host" (IPv6 in brackets).
// Certificates stay in Windows' store and are not read: HTTPS bindings get
// an automatic certificate for their host name.
func (b *iisBuilder) bindings(it *item, s *model.Site, list []ahBinding) {
	for _, x := range list {
		proto := strings.ToLower(x.Protocol)
		if proto != "http" && proto != "https" {
			it.skipped("The %s binding %q was left out: NodeHoster serves HTTP and HTTPS.", x.Protocol, x.Info)
			continue
		}
		ip, port, host, err := parseBindingInfo(x.Info)
		if err != nil {
			it.skipped("Binding %q: %v; left out.", x.Info, err)
			continue
		}
		bd := model.Binding{Protocol: proto, IP: ip, Port: port, Host: host}
		if proto == "https" {
			switch {
			case host == "" || strings.HasPrefix(host, "*."):
				what := "has no host name"
				if host != "" {
					what = "has a wildcard host name"
				}
				it.skipped("The https binding %s %s, so no automatic certificate can be requested for it; it was left out. Import the certificate (PFX) on the Certificates page and add the binding with it.", model.Binding{Protocol: proto, IP: ip, Port: port, Host: host}.String(), what)
				continue
			default:
				bd.CertMode = model.CertModeAuto
				hash := ""
				if x.CertificateHash != "" {
					hash = " (" + x.CertificateHash + ")"
				}
				it.approximated("https %s: the IIS certificate%s stays in the Windows store; the binding will get a Let's Encrypt certificate (DNS must point here), or import the PFX and select it.", host, hash)
			}
			if flags, _ := strconv.Atoi(x.SSLFlags); flags&1 != 0 {
				it.converted("https %s uses SNI, as every NodeHoster https binding does.", host)
			}
		}
		s.Bindings = append(s.Bindings, bd)
	}
	if len(s.Bindings) > 0 {
		var names []string
		for _, bd := range s.Bindings {
			names = append(names, bd.String())
		}
		it.converted("Bindings: %s.", strings.Join(names, ", "))
	}
}

func parseBindingInfo(info string) (ip string, port int, host string, err error) {
	i := strings.LastIndex(info, ":")
	if i < 0 {
		return "", 0, "", fmt.Errorf("not ip:port:host")
	}
	host, rest := strings.ToLower(strings.TrimSpace(info[i+1:])), info[:i]
	j := strings.LastIndex(rest, ":")
	if j < 0 {
		return "", 0, "", fmt.Errorf("not ip:port:host")
	}
	port, err = strconv.Atoi(rest[j+1:])
	if err != nil || port < 1 || port > 65535 {
		return "", 0, "", fmt.Errorf("bad port")
	}
	ip = strings.Trim(rest[:j], "[]")
	if ip == "*" || ip == "0.0.0.0" {
		ip = ""
	}
	if ip != "" && net.ParseIP(ip) == nil {
		return "", 0, "", fmt.Errorf("bad IP address %q", ip)
	}
	return ip, port, host, nil
}

// identity notes the application pool's identity: passwords are never
// imported, so a specific account must be entered again.
func (b *iisBuilder) identity(it *item, app *ahApp) {
	pool := app.Pool
	if pool == "" {
		pool = b.appDefs.Pool
	}
	if pool == "" {
		return
	}
	p, ok := b.pools[strings.ToLower(pool)]
	if !ok {
		return
	}
	idType := p.ProcessModel.IdentityType
	if idType == "" {
		idType = b.poolDefs.ProcessModel.IdentityType
	}
	switch strings.ToLower(idType) {
	case "", "applicationpoolidentity":
	case "specificuser":
		user := p.ProcessModel.UserName
		it.skipped("Application pool %q runs as %s. Passwords are never imported: to run the site as that account, set Run as on the Settings tab and enter its password.", pool, user)
	default:
		it.skipped("Application pool %q runs as %s; NodeHoster runs sites as its service account unless Run as is set.", pool, idType)
	}
}
