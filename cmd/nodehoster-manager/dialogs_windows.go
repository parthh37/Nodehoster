package main

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/desktop"
	"github.com/parthh37/nodehoster/internal/localapi"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/secrets"
	"github.com/tailscale/walk"
	. "github.com/tailscale/walk/declarative"
)

// runDialog shows a modal dialog with OK and Cancel. onOK validates and
// applies the input; returning false keeps the dialog open. Read the
// widgets in onOK: the dialog and its widgets are disposed when Run
// returns, and their getters then return zero values.
func runDialog(owner walk.Form, title string, size Size, children []Widget, onOK func(dlg *walk.Dialog) bool) bool {
	return runDialogAs(nil, owner, title, size, children, onOK)
}

// runDialogAs is runDialog that also stores the dialog in *self, for
// dialogs that open dialogs of their own (which must be owned by it to stay
// modal).
func runDialogAs(self **walk.Dialog, owner walk.Form, title string, size Size, children []Widget, onOK func(dlg *walk.Dialog) bool) bool {
	var dlg *walk.Dialog
	if self == nil {
		self = &dlg
	}
	var ok, cancel *walk.PushButton
	var icon Property
	if ic := appIcon(); ic != nil {
		icon = ic
	}
	err := Dialog{
		AssignTo:      self,
		Title:         title,
		Icon:          icon,
		MinSize:       size,
		DefaultButton: &ok,
		CancelButton:  &cancel,
		Layout:        VBox{Margins: Margins{Left: 14, Top: 12, Right: 14, Bottom: 12}, Spacing: 8},
		Children: append(children,
			VSpacer{Size: 4},
			Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
				HSpacer{},
				PushButton{AssignTo: &ok, Text: "OK", MinSize: Size{Width: 84}, OnClicked: func() {
					if onOK == nil || onOK(*self) {
						(*self).Accept()
					}
				}},
				PushButton{AssignTo: &cancel, Text: "Cancel", MinSize: Size{Width: 84}, OnClicked: func() { (*self).Cancel() }},
			}},
		),
	}.Create(owner)
	if err != nil {
		walk.MsgBox(owner, title, err.Error(), walk.MsgBoxIconError)
		return false
	}
	return (*self).Run() == walk.DlgCmdOK
}

// intro heads a dialog: its tile and what it is for.
func intro(icon, text string) Composite {
	var iv *walk.ImageView
	return Composite{
		Layout: HBox{MarginsZero: true, Spacing: 12, Alignment: AlignHNearVCenter},
		Children: []Widget{
			ImageView{AssignTo: &iv, Image: asImage(tileIcon(icon)), MinSize: Size{Width: 32, Height: 32}, MaxSize: Size{Width: 32, Height: 32}},
			TextLabel{Text: text, StretchFactor: 1},
		},
	}
}

func invalid(owner walk.Form, msg string) bool {
	notify(owner, "Check the input", "Check the input", msg, "", walk.TaskDialogSystemIconWarning)
	return false
}

func inputDialog(owner walk.Form, title, icon, prompt, initial string, password bool) (string, bool) {
	var le *walk.LineEdit
	var value string
	ok := runDialog(owner, title, Size{Width: 420}, []Widget{
		intro(icon, prompt),
		LineEdit{AssignTo: &le, Text: initial, PasswordMode: password},
	}, func(*walk.Dialog) bool {
		value = le.Text()
		return true
	})
	return value, ok
}

// showSecretDialog shows a generated secret once, with a copy button.
func showSecretDialog(owner walk.Form, title, prompt, secret string) {
	runDialog(owner, title, Size{Width: 460}, []Widget{
		intro(desktop.IconKey, prompt),
		Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
			LineEdit{Text: secret, ReadOnly: true, Font: Font{Family: "Consolas", PointSize: 11}},
			button("Copy", func() { walk.Clipboard().SetText(secret) }),
		}},
		hint("It is not shown again."),
	}, nil)
}

// textDialog shows read-only text, such as a deployment's output.
func textDialog(owner walk.Form, title, icon, text string) {
	var dlg *walk.Dialog
	var closeBtn *walk.PushButton
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n")
	if strings.TrimSpace(text) == "" {
		text = "(no output)"
	}
	var iconProp Property
	if ic := tileIcon(icon); ic != nil {
		iconProp = ic
	}
	err := Dialog{
		AssignTo:     &dlg,
		Title:        title,
		Icon:         iconProp,
		MinSize:      Size{Width: 560, Height: 360},
		Size:         Size{Width: 860, Height: 560},
		CancelButton: &closeBtn,
		Layout:       VBox{Margins: Margins{Left: 12, Top: 12, Right: 12, Bottom: 12}},
		Children: []Widget{
			TextEdit{Text: text, ReadOnly: true, VScroll: true, HScroll: true, Font: fontMono},
			Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
				button("Copy all", func() { walk.Clipboard().SetText(text) }),
				HSpacer{},
				PushButton{AssignTo: &closeBtn, Text: "Close", MinSize: Size{Width: 84}, OnClicked: func() { dlg.Cancel() }},
			}},
		},
	}.Create(owner)
	if err != nil {
		walk.MsgBox(owner, title, err.Error(), walk.MsgBoxIconError)
		return
	}
	dlg.Run()
}

// browseFolder lets the user pick a folder into le.
func browseFolder(owner walk.Form, le *walk.LineEdit, title string) {
	dlg := walk.FileDialog{Title: title, InitialDirPath: le.Text()}
	if ok, _ := dlg.ShowBrowseFolder(owner); ok {
		le.SetText(dlg.FilePath)
	}
}

// editableSite returns a copy of a site that a dialog may change, with
// its own slices.
func (m *manager) editableSite(id string) *model.Site {
	st := m.siteByID(id)
	if st == nil {
		return nil
	}
	s := *st.Site
	s.Bindings = slices.Clone(s.Bindings)
	if s.Node != nil {
		n := *s.Node
		n.Env = slices.Clone(n.Env)
		s.Node = &n
	}
	return &s
}

func (m *manager) certificates() []certView {
	var list []certView
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m.cl.Get(ctx, "/api/certificates", &list)
	return list
}

// ---- installing Node.js

// nodeRelease is a release in nodejs.org's index.
type nodeRelease struct {
	Version  string `json:"version"`
	LTS      any    `json:"lts"` // the codename, or false
	Date     string `json:"date"`
	Security bool   `json:"security"`
}

var versionRe = regexp.MustCompile(`^v?(\d+\.\d+\.\d+)`)

// releaseChoices lists the newest release of each major version, newest
// majors first, the LTS ones marked.
func releaseChoices(list []nodeRelease) []string {
	var items []string
	seen := map[string]bool{}
	for _, r := range list {
		major, _, _ := strings.Cut(strings.TrimPrefix(r.Version, "v"), ".")
		if seen[major] || len(items) >= 16 {
			continue
		}
		seen[major] = true
		label := strings.TrimPrefix(r.Version, "v")
		if lts, ok := r.LTS.(string); ok && lts != "" {
			label += "  — LTS (" + lts + ")"
		} else {
			label += "  — Current"
		}
		if r.Date != "" {
			label += ", " + r.Date
		}
		items = append(items, label)
	}
	return items
}

// installNodeDialog asks which version to install, offering the recent
// releases of each line and taking any typed one. The releases load in the
// background (the server asks nodejs.org, which a firewalled server may
// not reach), so the dialog opens at once and typing works meanwhile.
func installNodeDialog(m *manager) (string, bool) {
	var cb *walk.ComboBox
	var note *walk.TextLabel
	var version string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // the dialog closed: drop a late answer
	go func() {
		var list []nodeRelease
		rctx, rcancel := context.WithTimeout(ctx, 20*time.Second)
		err := m.cl.Get(rctx, "/api/node/available", &list)
		rcancel()
		m.mw.Synchronize(func() {
			if ctx.Err() != nil || cb == nil {
				return
			}
			if err != nil {
				note.SetText("The list of releases could not be read (" + err.Error() + "). Type a version, such as 22.12.0.")
				return
			}
			typed := cb.Text()
			cb.SetModel(releaseChoices(list))
			if strings.TrimSpace(typed) == "" && len(list) > 0 {
				cb.SetCurrentIndex(0)
			} else {
				cb.SetText(typed)
			}
			note.SetText("Pick a release, or type any version, such as 20.18.1. It is downloaded from nodejs.org and verified by SHA-256.")
		})
	}()

	ok := runDialog(m.mw, "Install Node.js", Size{Width: 480}, []Widget{
		intro(desktop.IconNode, "Install a version of Node.js for the sites to run on."),
		Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: []Widget{
			Label{Text: "Version:"},
			ComboBox{AssignTo: &cb, Editable: true},
		}},
		TextLabel{AssignTo: &note, Text: "Loading the releases from nodejs.org…", TextColor: colorMuted},
	}, func(dlg *walk.Dialog) bool {
		v := versionRe.FindStringSubmatch(strings.TrimSpace(cb.Text()))
		if v == nil {
			return invalid(dlg, "Enter a version number, such as 22.12.0.")
		}
		version = v[1]
		return true
	})
	return version, ok
}

// ---- bindings, as IIS Manager's "Site Bindings"

func bindingsDialog(m *manager, siteID string) {
	s := m.editableSite(siteID)
	if s == nil {
		return
	}
	certs := m.certificates()
	var t table
	t.icon = func(row, col int) walk.Image {
		if col != 0 || row >= len(s.Bindings) {
			return nil
		}
		if s.Bindings[row].Protocol == "https" {
			return img(desktop.IconHTTPS)
		}
		return img(desktop.IconHTTP)
	}
	refresh := func() {
		keys := make([]string, len(s.Bindings))
		rows := make([][]string, len(s.Bindings))
		for i, b := range s.Bindings {
			keys[i] = fmt.Sprint(i, b.ID)
			rows[i] = []string{b.Protocol, orStar(b.IP), strconv.Itoa(b.Port), hostWithSlot(b), certLabel(b, certs), desktop.ClientCertText(b.ClientCert)}
		}
		t.set(keys, rows)
	}
	var dlg *walk.Dialog
	edit := func() {
		if i := t.current(); i >= 0 {
			b := s.Bindings[i]
			if bindingEditDialog(dlg, "Edit site binding", &b, certs) {
				s.Bindings[i] = b
				refresh()
			}
		}
	}
	remove := func() {
		if i := t.current(); i >= 0 {
			s.Bindings = slices.Delete(s.Bindings, i, i+1)
			refresh()
		}
	}
	refresh()
	ok := runDialogAs(&dlg, m.mw, "Site bindings — "+s.Name, Size{Width: 900, Height: 380}, []Widget{
		intro(desktop.IconLink, "The addresses the site answers on: protocol, IP address, port and host name. HTTPS bindings use a certificate from this server, or get one automatically."),
		Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
			t.viewWith(tableOpts{onActivate: edit, onDelete: remove},
				col("Type", 70), col("IP address", 120), colR("Port", 60), col("Host name", 200), col("Certificate", 180), col("Client certificates", 150)),
			Composite{Layout: VBox{MarginsZero: true}, Children: []Widget{
				button("Add…", func() {
					b := model.Binding{Protocol: "http", IP: "*", Port: 80}
					if bindingEditDialog(dlg, "Add site binding", &b, certs) {
						s.Bindings = append(s.Bindings, b)
						refresh()
						t.selectModel(len(s.Bindings) - 1)
					}
				}),
				button("Edit…", edit),
				button("Remove", remove),
				button("Browse", func() {
					if i := t.current(); i >= 0 {
						shellOpen(desktop.BrowseURL(s.Bindings[i]))
					}
				}),
				VSpacer{},
			}},
		}},
	}, nil)
	if ok {
		m.saveSite(s, nil)
	}
}

func orStar(ip string) string {
	if ip == "" {
		return "*"
	}
	return ip
}

func certLabel(b model.Binding, certs []certView) string {
	if b.Protocol != "https" {
		return ""
	}
	if b.CertMode != model.CertModeManual {
		return "Automatic (ACME)"
	}
	for _, c := range certs {
		if c.ID == b.CertificateID {
			return c.Name
		}
	}
	return b.CertificateID
}

func bindingEditDialog(owner walk.Form, title string, b *model.Binding, certs []certView) bool {
	var typ, cert *walk.ComboBox
	var ip, host *walk.LineEdit
	var port *walk.NumberEdit
	certNames := []string{"Automatic (Let's Encrypt or another ACME CA)"}
	certIdx := 0
	for i, c := range certs {
		certNames = append(certNames, c.Name+" ("+strings.Join(c.Domains, ", ")+")")
		if b.CertMode == model.CertModeManual && c.ID == b.CertificateID {
			certIdx = i + 1
		}
	}
	protos := []string{"http", "https"}
	var self *walk.Dialog
	clientCert := newClientCertField(b.ClientCert) // pages_tls_windows.go
	onType := func() {
		if cert == nil || port == nil { // still being created
			return
		}
		https := typ.CurrentIndex() == 1
		cert.SetEnabled(https)
		clientCert.setEnabled(https)
		// Like IIS: switching the protocol moves the default port along.
		switch {
		case https && port.Value() == 80:
			port.SetValue(443)
		case !https && port.Value() == 443:
			port.SetValue(80)
		}
	}
	return runDialogAs(&self, owner, title, Size{Width: 500}, []Widget{
		Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: append([]Widget{
			Label{Text: "Type:"},
			ComboBox{AssignTo: &typ, Model: protos, CurrentIndex: slices.Index(protos, b.Protocol), OnCurrentIndexChanged: onType},
			Label{Text: "IP address:"},
			LineEdit{AssignTo: &ip, Text: orStar(b.IP), CueBanner: "* (all unassigned)"},
			Label{Text: "Port:"},
			NumberEdit{AssignTo: &port, Value: float64(b.Port), MinValue: 1, MaxValue: 65535, Decimals: 0},
			Label{Text: "Host name:"},
			LineEdit{AssignTo: &host, Text: b.Host, CueBanner: "www.example.com, *.example.com, or empty for any"},
			Label{Text: "Certificate:"},
			ComboBox{AssignTo: &cert, Model: certNames, CurrentIndex: certIdx, Enabled: b.Protocol == "https"},
		}, clientCert.widgets(&self, b.Protocol == "https")...)},
	}, func(dlg *walk.Dialog) bool {
		addr := strings.TrimSpace(ip.Text())
		if addr != "" && addr != "*" && net.ParseIP(addr) == nil {
			return invalid(dlg, "Enter an IP address, or * for all addresses.")
		}
		b.Protocol = protos[max(typ.CurrentIndex(), 0)]
		b.IP = addr
		if b.IP == "*" {
			b.IP = ""
		}
		b.Port = int(port.Value())
		b.Host = strings.ToLower(strings.TrimSpace(host.Text()))
		b.CertMode, b.CertificateID, b.ClientCert = "", "", nil
		if b.Protocol == "https" {
			b.ClientCert = clientCert.cc
			b.CertMode = model.CertModeAuto
			if i := cert.CurrentIndex(); i > 0 {
				b.CertMode, b.CertificateID = model.CertModeManual, certs[i-1].ID
			} else if b.Host == "" {
				return invalid(dlg, "An automatic certificate needs a host name. Enter one, or pick an existing certificate.")
			}
		}
		return true
	})
}

// ---- environment variables

func envDialog(m *manager, siteID string) {
	s := m.editableSite(siteID)
	if s == nil {
		return
	}
	if s.Node == nil {
		notify(m.mw, "Environment", "Environment variables apply to Node.js sites only.", "", "", walk.TaskDialogSystemIconInformation)
		return
	}
	var t table
	t.icon = func(row, col int) walk.Image {
		if col != 0 || row >= len(s.Node.Env) {
			return nil
		}
		if s.Node.Env[row].From != nil {
			return img(desktop.IconKey)
		}
		if s.Node.Env[row].Secret {
			return img(desktop.IconLock)
		}
		return img(desktop.IconBraces)
	}
	refresh := func() {
		keys := make([]string, len(s.Node.Env))
		rows := make([][]string, len(s.Node.Env))
		for i, e := range s.Node.Env {
			v, source := desktop.EnvText(e)
			keys[i] = fmt.Sprint(i, e.Name)
			rows[i] = []string{e.Name, v, source}
		}
		t.set(keys, rows)
	}
	var dlg *walk.Dialog
	edit := func() {
		if i := t.current(); i >= 0 {
			e := s.Node.Env[i]
			if envEditDialog(dlg, "Edit variable", &e) {
				s.Node.Env[i] = e
				refresh()
			}
		}
	}
	remove := func() {
		if i := t.current(); i >= 0 {
			s.Node.Env = slices.Delete(s.Node.Env, i, i+1)
			refresh()
		}
	}
	refresh()
	ok := runDialogAs(&dlg, m.mw, "Environment variables — "+s.Name, Size{Width: 700, Height: 420}, []Widget{
		intro(desktop.IconBraces, "Variables the site's processes see. Changes apply with a zero-downtime recycle; secret values are encrypted at rest. A value secretref:<store>/<secret> is read from a secret store at each start."),
		Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
			t.viewWith(tableOpts{onActivate: edit, onDelete: remove}, col("Name", 200), col("Value", 280), col("Source", 80)),
			Composite{Layout: VBox{MarginsZero: true}, Children: []Widget{
				button("Add…", func() {
					var e model.EnvVar
					if envEditDialog(dlg, "Add variable", &e) {
						s.Node.Env = append(s.Node.Env, e)
						refresh()
						t.selectModel(len(s.Node.Env) - 1)
					}
				}),
				button("Edit…", edit),
				button("Remove", remove),
				VSpacer{},
			}},
		}},
	}, nil)
	if ok {
		m.saveSite(s, nil)
	}
}

func envEditDialog(owner walk.Form, title string, e *model.EnvVar) bool {
	var name, value *walk.LineEdit
	var secret *walk.CheckBox
	stored := e.Secret && e.Value == secrets.Mask
	shown := e.Value
	if stored {
		shown = ""
	}
	if e.From != nil {
		shown = e.From.String()
	}
	return runDialog(owner, title, Size{Width: 460}, []Widget{
		Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: []Widget{
			Label{Text: "Name:"},
			LineEdit{AssignTo: &name, Text: e.Name, CueBanner: "DATABASE_URL"},
			Label{Text: "Value:"},
			LineEdit{AssignTo: &value, Text: shown, PasswordMode: e.Secret, CueBanner: map[bool]string{true: "unchanged", false: "value, or secretref:<store>/<secret>"}[stored]},
			Label{},
			CheckBox{AssignTo: &secret, Text: "Secret (encrypted, never shown again)", Checked: e.Secret,
				OnCheckedChanged: func() {
					if value != nil {
						value.SetPasswordMode(secret.Checked())
					}
				}},
		}},
	}, func(dlg *walk.Dialog) bool {
		n := strings.TrimSpace(name.Text())
		if n == "" || strings.ContainsAny(n, "= \t") {
			return invalid(dlg, "Enter a variable name without spaces or '='.")
		}
		next := *e
		if err := desktop.EnvFromText(&next, n, value.Text(), secret.Checked()); err != nil {
			return invalid(dlg, strings.ToUpper(err.Error()[:1])+err.Error()[1:]+".")
		}
		*e = next
		return true
	})
}

// ---- basic settings

func basicSettingsDialog(m *manager, siteID string) {
	s := m.editableSite(siteID)
	if s == nil {
		return
	}
	var name, path, entry, npm, version, target *walk.LineEdit
	var auto, preserve *walk.CheckBox
	var instances *walk.NumberEdit
	var upstreams *walk.TextEdit
	var code *walk.ComboBox
	codes := []string{"301", "302", "307", "308"}

	fields := []Widget{
		Label{Text: "Site name:"}, LineEdit{AssignTo: &name, Text: s.Name},
		Label{}, CheckBox{AssignTo: &auto, Text: "Start automatically with the service", Checked: s.AutoStart},
	}
	folder := func(value string) Widget {
		return Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
			LineEdit{AssignTo: &path, Text: value},
			PushButton{Text: "Browse…", Image: img(desktop.IconFolder), OnClicked: func() { browseFolder(m.mw, path, "Folder of "+s.Name) }},
		}}
	}
	switch {
	case s.Node != nil:
		fields = append(fields,
			Label{Text: "Application folder:"}, folder(s.Node.AppRoot),
			Label{Text: "Entry script:"}, LineEdit{AssignTo: &entry, Text: s.Node.Script, CueBanner: "server.js"},
			Label{Text: "or npm script:"}, LineEdit{AssignTo: &npm, Text: s.Node.NpmScript, CueBanner: "start"},
			Label{Text: "Instances:"}, NumberEdit{AssignTo: &instances, Value: float64(s.Node.Instances), MinValue: 1, MaxValue: 64},
			Label{Text: "Node.js version:"}, LineEdit{AssignTo: &version, Text: s.Node.NodeVersion, CueBanner: "server default"},
		)
	case s.Static != nil:
		fields = append(fields, Label{Text: "Folder:"}, folder(s.Static.Root))
	case s.Proxy != nil:
		var urls []string
		for _, u := range s.Proxy.Upstreams {
			urls = append(urls, u.URL)
		}
		fields = append(fields, Label{Text: "Upstream URLs\n(one per line):"},
			TextEdit{AssignTo: &upstreams, Text: strings.Join(urls, "\r\n"), MinSize: Size{Height: 80}, VScroll: true})
	case s.Redirect != nil:
		fields = append(fields,
			Label{Text: "Redirect to:"}, LineEdit{AssignTo: &target, Text: s.Redirect.TargetURL},
			Label{Text: "Status code:"}, ComboBox{AssignTo: &code, Model: codes, CurrentIndex: slices.Index(codes, strconv.Itoa(s.Redirect.StatusCode))},
			Label{}, CheckBox{AssignTo: &preserve, Text: "Keep the requested path", Checked: s.Redirect.PreservePath},
		)
	}
	ok := runDialog(m.mw, "Basic settings — "+s.Name, Size{Width: 560}, []Widget{
		intro(desktop.SiteTypeIcon(string(s.Type)), desktop.SiteTypeText(s.Type)+". Other settings (routing, recycling, limits, health checks) are in the web console."),
		Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: fields},
	}, func(dlg *walk.Dialog) bool {
		s.Name, s.AutoStart = strings.TrimSpace(name.Text()), auto.Checked()
		if s.Name == "" {
			return invalid(dlg, "Enter a site name.")
		}
		switch {
		case s.Node != nil:
			s.Node.AppRoot, s.Node.Script, s.Node.NpmScript = path.Text(), strings.TrimSpace(entry.Text()), strings.TrimSpace(npm.Text())
			s.Node.Instances, s.Node.NodeVersion = int(instances.Value()), strings.TrimSpace(version.Text())
		case s.Static != nil:
			s.Static.Root = path.Text()
		case s.Proxy != nil:
			var ups []model.Upstream
			for _, l := range strings.Split(upstreams.Text(), "\n") {
				if l = strings.TrimSpace(l); l != "" {
					ups = append(ups, model.Upstream{URL: l, Weight: weightOf(s.Proxy.Upstreams, l)})
				}
			}
			s.Proxy.Upstreams = ups
		case s.Redirect != nil:
			s.Redirect.TargetURL, s.Redirect.PreservePath = strings.TrimSpace(target.Text()), preserve.Checked()
			if i := code.CurrentIndex(); i >= 0 {
				s.Redirect.StatusCode, _ = strconv.Atoi(codes[i])
			}
		}
		return true
	})
	if ok {
		m.saveSite(s, nil)
	}
}

// weightOf keeps an upstream's weight when its URL is kept.
func weightOf(ups []model.Upstream, u string) int {
	for _, x := range ups {
		if x.URL == u {
			return x.Weight
		}
	}
	return 0
}

// ---- add site, as IIS Manager's "Add Website"

func addSiteDialog(m *manager) {
	types := []model.SiteType{model.SiteNode, model.SiteWorker, model.SiteStatic, model.SiteProxy, model.SiteRedirect}
	typeNames := []string{"Node.js application", "Background worker (no HTTP)", "Static site", "Reverse proxy", "Redirect"}
	typeNotes := []string{
		"Runs a Node.js app (server.js or an npm script) behind the reverse proxy, with process management and zero-downtime recycling.",
		"Runs a Node.js process that serves no HTTP: a queue consumer, a bot, a long-running script. It has no binding.",
		"Serves the files of a folder, with compression, caching and MIME types.",
		"Forwards requests to one or more servers, with load balancing and health checks.",
		"Answers every request with a redirect to another URL.",
	}
	var name, path, entry, target, ip, host *walk.LineEdit
	var typ, proto *walk.ComboBox
	var port *walk.NumberEdit
	var start *walk.CheckBox
	var pathBox *walk.Composite
	var bindingBox *walk.GroupBox
	var pathLabel, entryLabel, targetLabel *walk.Label
	var note, httpsNote *walk.TextLabel
	var typeIcon *walk.ImageView

	onType := func() {
		if target == nil || note == nil { // still being created
			return
		}
		i := max(typ.CurrentIndex(), 0)
		t := types[i]
		note.SetText(typeNotes[i])
		typeIcon.SetImage(asImage(tileIcon(desktop.SiteTypeIcon(string(t)))))
		node := t == model.SiteNode || t == model.SiteWorker
		hasPath := node || t == model.SiteStatic
		pathLabel.SetVisible(hasPath)
		pathBox.SetVisible(hasPath)
		entryLabel.SetVisible(node)
		entry.SetVisible(node)
		// A worker serves no HTTP, so it has no binding.
		bindingBox.SetVisible(t != model.SiteWorker)
		httpsNote.SetVisible(t != model.SiteWorker)
		targetLabel.SetVisible(!hasPath)
		target.SetVisible(!hasPath)
		if t == model.SiteProxy {
			targetLabel.SetText("Upstream URL:")
			target.SetCueBanner("http://127.0.0.1:3000")
		} else {
			targetLabel.SetText("Redirect to:")
			target.SetCueBanner("https://www.example.com")
		}
	}

	var created *localapi.Site
	var startNow bool
	ok := runDialog(m.mw, "Add site", Size{Width: 600}, []Widget{
		Composite{Layout: HBox{MarginsZero: true, Spacing: 12, Alignment: AlignHNearVCenter}, Children: []Widget{
			ImageView{AssignTo: &typeIcon, Image: asImage(tileIcon(desktop.IconNode)), MinSize: Size{Width: 32, Height: 32}, MaxSize: Size{Width: 32, Height: 32}},
			TextLabel{AssignTo: &note, Text: typeNotes[0], StretchFactor: 1},
		}},
		Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: []Widget{
			Label{Text: "Site name:"}, LineEdit{AssignTo: &name, CueBanner: "shop"},
			Label{Text: "Type:"}, ComboBox{AssignTo: &typ, Model: typeNames, CurrentIndex: 0, OnCurrentIndexChanged: onType},
			Label{AssignTo: &pathLabel, Text: "Physical path:"},
			Composite{AssignTo: &pathBox, Layout: HBox{MarginsZero: true}, Children: []Widget{
				LineEdit{AssignTo: &path, CueBanner: `C:\apps\my-app`},
				PushButton{Text: "Browse…", Image: img(desktop.IconFolder), OnClicked: func() { browseFolder(m.mw, path, "Folder of the site") }},
			}},
			Label{AssignTo: &entryLabel, Text: "Entry script:"}, LineEdit{AssignTo: &entry, Text: "server.js"},
			Label{AssignTo: &targetLabel, Text: "Upstream URL:", Visible: false}, LineEdit{AssignTo: &target, Visible: false},
		}},
		GroupBox{AssignTo: &bindingBox, Title: "Binding", Layout: Grid{Columns: 4}, Children: []Widget{
			Label{Text: "Type:"}, ComboBox{AssignTo: &proto, Model: []string{"http", "https"}, CurrentIndex: 0, OnCurrentIndexChanged: func() {
				if port == nil {
					return
				}
				if proto.CurrentIndex() == 1 && port.Value() == 80 {
					port.SetValue(443)
				} else if proto.CurrentIndex() == 0 && port.Value() == 443 {
					port.SetValue(80)
				}
			}},
			Label{Text: "IP address:"}, LineEdit{AssignTo: &ip, Text: "*"},
			Label{Text: "Port:"}, NumberEdit{AssignTo: &port, Value: 80.0, MinValue: 1, MaxValue: 65535},
			Label{Text: "Host name:"}, LineEdit{AssignTo: &host, CueBanner: "www.example.com"},
		}},
		TextLabel{AssignTo: &httpsNote, Text: "HTTPS bindings get an automatic certificate for the host name.", TextColor: colorMuted},
		CheckBox{AssignTo: &start, Text: "Start the site now", Checked: true},
	}, func(dlg *walk.Dialog) bool {
		t := types[max(typ.CurrentIndex(), 0)]
		s := &model.Site{Name: strings.TrimSpace(name.Text()), Type: t, AutoStart: true}
		if s.Name == "" {
			return invalid(dlg, "Enter a site name.")
		}
		b := model.Binding{Protocol: []string{"http", "https"}[max(proto.CurrentIndex(), 0)], IP: strings.TrimSpace(ip.Text()),
			Port: int(port.Value()), Host: strings.ToLower(strings.TrimSpace(host.Text()))}
		if b.IP == "*" {
			b.IP = ""
		}
		if b.Protocol == "https" && t != model.SiteWorker {
			if b.Host == "" {
				return invalid(dlg, "An HTTPS binding needs a host name for its certificate.")
			}
			b.CertMode = model.CertModeAuto
		}
		s.Bindings = []model.Binding{b}
		switch t {
		case model.SiteNode:
			s.Node = &model.NodeConfig{AppRoot: path.Text(), Script: strings.TrimSpace(entry.Text()), Instances: 1}
		case model.SiteWorker:
			s.Bindings = nil
			s.Node = &model.NodeConfig{AppRoot: path.Text(), Script: strings.TrimSpace(entry.Text()), Instances: 1}
		case model.SiteStatic:
			s.Static = &model.StaticConfig{Root: path.Text()}
		case model.SiteProxy:
			s.Proxy = &model.ProxyConfig{Upstreams: []model.Upstream{{URL: strings.TrimSpace(target.Text())}}}
		case model.SiteRedirect:
			s.Redirect = &model.RedirectConfig{TargetURL: strings.TrimSpace(target.Text()), StatusCode: 301, PreservePath: true}
		}
		// Created here rather than after the dialog closes, so a
		// validation error keeps the user's input.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var out localapi.Site
		if err := m.cl.Post(ctx, "/api/sites", s, &out); err != nil {
			m.errorBoxFor(dlg, "Add site", err)
			return false
		}
		created, startNow = &out, start.Checked()
		return true
	})
	if !ok || created == nil {
		return
	}
	id := created.ID
	m.flashStatus("Added the site "+created.Name, false)
	if startNow {
		m.siteAction(id, "start")
	}
	// Show the new site once a refresh has put it in the tree.
	m.openWhenListed(id)
	m.refresh(true)
}

// ---- the web console's listener

func adminConsoleDialog(m *manager) {
	type adminSettings struct {
		Listen          string `json:"listen"`
		TLS             string `json:"tls"`
		CertificateID   string `json:"certificateId"`
		RestartRequired bool   `json:"restartRequired"`
	}
	var cur adminSettings
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := m.cl.Get(ctx, "/api/settings/admin", &cur)
	cancel()
	if err != nil {
		m.errorBox("Web console settings", err)
		return
	}
	certs := m.certificates()
	modes := []string{"selfsigned", "certificate", "none"}
	modeNames := []string{"HTTPS with a self-signed certificate", "HTTPS with a certificate from this server", "HTTP (no encryption)"}
	var certNames []string
	certIdx := -1
	for i, c := range certs {
		certNames = append(certNames, c.Name+" ("+strings.Join(c.Domains, ", ")+")")
		if c.ID == cur.CertificateID {
			certIdx = i
		}
	}
	var listen *walk.LineEdit
	var mode, cert *walk.ComboBox
	ok := runDialog(m.mw, "Web console settings", Size{Width: 560}, []Widget{
		intro(desktop.IconConsole, "Where the web console listens, and how it is secured. The change applies when the service restarts."),
		Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: []Widget{
			Label{Text: "Listen on:"}, LineEdit{AssignTo: &listen, Text: cur.Listen, CueBanner: "0.0.0.0:8484"},
			Label{Text: "Security:"}, ComboBox{AssignTo: &mode, Model: modeNames, CurrentIndex: max(slices.Index(modes, cur.TLS), 0),
				OnCurrentIndexChanged: func() {
					if cert != nil {
						cert.SetEnabled(mode.CurrentIndex() == 1)
					}
				}},
			Label{Text: "Certificate:"}, ComboBox{AssignTo: &cert, Model: certNames, CurrentIndex: certIdx, Enabled: cur.TLS == "certificate"},
		}},
		hint("Use 127.0.0.1:8484 to reach the console from this machine only."),
	}, func(dlg *walk.Dialog) bool {
		in := adminSettings{Listen: strings.TrimSpace(listen.Text()), TLS: modes[max(mode.CurrentIndex(), 0)]}
		host, _, err := net.SplitHostPort(in.Listen)
		if err != nil {
			return invalid(dlg, "Enter an address and port, such as 0.0.0.0:8484.")
		}
		if in.TLS == "certificate" {
			if cert.CurrentIndex() < 0 {
				return invalid(dlg, "Pick a certificate.")
			}
			in.CertificateID = certs[cert.CurrentIndex()].ID
		}
		if in.TLS == "none" && host != "127.0.0.1" && host != "localhost" && host != "::1" &&
			ask(dlg, "Web console settings", "Serve the console without encryption?",
				"Passwords would cross the network in clear text on "+in.Listen+".",
				walk.TaskDialogSystemIconWarning, [2]string{"Use plain HTTP anyway", ""}) != 0 {
			return false
		}
		var out adminSettings
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := m.cl.Put(ctx, "/api/settings/admin", in, &out); err != nil {
			m.errorBoxFor(dlg, "Web console settings", err)
			return false
		}
		cur = out
		return true
	})
	if ok && cur.RestartRequired && ask(m.mw, "Web console settings", "Restart the NodeHoster service now?",
		"The web console's new settings apply when the service restarts. Sites go offline for a few seconds.",
		walk.TaskDialogSystemIconInformation, [2]string{"Restart now", ""}, [2]string{"Later", ""}) == 0 {
		m.do("Restarting the service", func(context.Context) error { return controlService("restart") })
	}
}
