package main

import (
	"context"
	"fmt"
	"net"
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
	err := Dialog{
		AssignTo:      self,
		Title:         title,
		MinSize:       size,
		DefaultButton: &ok,
		CancelButton:  &cancel,
		Layout:        VBox{},
		Children: append(children,
			VSpacer{Size: 4},
			Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
				HSpacer{},
				PushButton{AssignTo: &ok, Text: "OK", OnClicked: func() {
					if onOK == nil || onOK(*self) {
						(*self).Accept()
					}
				}},
				PushButton{AssignTo: &cancel, Text: "Cancel", OnClicked: func() { (*self).Cancel() }},
			}},
		),
	}.Create(owner)
	if err != nil {
		walk.MsgBox(owner, title, err.Error(), walk.MsgBoxIconError)
		return false
	}
	return (*self).Run() == walk.DlgCmdOK
}

func invalid(owner walk.Form, msg string) bool {
	walk.MsgBox(owner, "Check the input", msg, walk.MsgBoxIconWarning)
	return false
}

func inputDialog(owner walk.Form, title, prompt, initial string, password bool) (string, bool) {
	var le *walk.LineEdit
	var value string
	ok := runDialog(owner, title, Size{Width: 380}, []Widget{
		Label{Text: prompt},
		LineEdit{AssignTo: &le, Text: initial, PasswordMode: password},
	}, func(*walk.Dialog) bool {
		value = le.Text()
		return true
	})
	return value, ok
}

func choiceDialog(owner walk.Form, title, prompt string, options []string, current int) (string, bool) {
	var cb *walk.ComboBox
	choice := -1
	ok := runDialog(owner, title, Size{Width: 320}, []Widget{
		Label{Text: prompt},
		ComboBox{AssignTo: &cb, Model: options, CurrentIndex: current},
	}, func(dlg *walk.Dialog) bool {
		if choice = cb.CurrentIndex(); choice < 0 {
			return invalid(dlg, "Pick one of the options.")
		}
		return true
	})
	if !ok {
		return "", false
	}
	return options[choice], true
}

// showSecretDialog shows a generated secret once, with a copy button.
func showSecretDialog(owner walk.Form, title, prompt, secret string) {
	runDialog(owner, title, Size{Width: 420}, []Widget{
		Label{Text: prompt},
		Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
			LineEdit{Text: secret, ReadOnly: true, Font: Font{Family: "Consolas", PointSize: 11}},
			PushButton{Text: "Copy", OnClicked: func() { walk.Clipboard().SetText(secret) }},
		}},
		Label{Text: "It is not shown again.", TextColor: colorMuted},
	}, nil)
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

// ---- bindings, as IIS Manager's "Site Bindings"

func bindingsDialog(m *manager, siteID string) {
	s := m.editableSite(siteID)
	if s == nil {
		return
	}
	certs := m.certificates()
	var t table
	refresh := func() {
		keys := make([]string, len(s.Bindings))
		rows := make([][]string, len(s.Bindings))
		for i, b := range s.Bindings {
			keys[i] = fmt.Sprint(i, b.ID)
			rows[i] = []string{b.Protocol, orStar(b.IP), strconv.Itoa(b.Port), b.Host, certLabel(b, certs)}
		}
		t.set(keys, rows)
	}
	index := func() int {
		i := t.tv.CurrentIndex()
		if i < 0 || i >= len(s.Bindings) {
			return -1
		}
		return i
	}
	var dlg *walk.Dialog
	edit := func() {
		if i := index(); i >= 0 {
			b := s.Bindings[i]
			if bindingEditDialog(dlg, "Edit site binding", &b, certs) {
				s.Bindings[i] = b
				refresh()
			}
		}
	}
	refresh()
	ok := runDialogAs(&dlg, m.mw, "Site bindings — "+s.Name, Size{Width: 720, Height: 360}, []Widget{
		Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
			t.view(edit, col("Type", 60), col("IP address", 120), col("Port", 60), col("Host name", 200), col("Certificate", 180)),
			Composite{Layout: VBox{MarginsZero: true}, Children: []Widget{
				PushButton{Text: "Add…", OnClicked: func() {
					b := model.Binding{Protocol: "http", IP: "*", Port: 80}
					if bindingEditDialog(dlg, "Add site binding", &b, certs) {
						s.Bindings = append(s.Bindings, b)
						refresh()
					}
				}},
				PushButton{Text: "Edit…", OnClicked: edit},
				PushButton{Text: "Remove", OnClicked: func() {
					if i := index(); i >= 0 {
						s.Bindings = slices.Delete(s.Bindings, i, i+1)
						refresh()
					}
				}},
				PushButton{Text: "Browse", OnClicked: func() {
					if i := index(); i >= 0 {
						shellOpen(desktop.BrowseURL(s.Bindings[i]))
					}
				}},
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
	onType := func() {
		if cert == nil || port == nil { // still being created
			return
		}
		https := typ.CurrentIndex() == 1
		cert.SetEnabled(https)
		// Like IIS: switching the protocol moves the default port along.
		switch {
		case https && port.Value() == 80:
			port.SetValue(443)
		case !https && port.Value() == 443:
			port.SetValue(80)
		}
	}
	return runDialog(owner, title, Size{Width: 460}, []Widget{
		Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: []Widget{
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
		}},
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
		b.CertMode, b.CertificateID = "", ""
		if b.Protocol == "https" {
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
		walk.MsgBox(m.mw, "Environment", "Environment variables apply to Node.js sites only.", walk.MsgBoxIconInformation)
		return
	}
	var t table
	refresh := func() {
		keys := make([]string, len(s.Node.Env))
		rows := make([][]string, len(s.Node.Env))
		for i, e := range s.Node.Env {
			v := e.Value
			if e.Secret {
				v = "••••••••"
			}
			keys[i] = fmt.Sprint(i, e.Name)
			rows[i] = []string{e.Name, v, yesNo(e.Secret)}
		}
		t.set(keys, rows)
	}
	index := func() int {
		if i := t.tv.CurrentIndex(); i >= 0 && i < len(s.Node.Env) {
			return i
		}
		return -1
	}
	var dlg *walk.Dialog
	edit := func() {
		if i := index(); i >= 0 {
			e := s.Node.Env[i]
			if envEditDialog(dlg, "Edit variable", &e) {
				s.Node.Env[i] = e
				refresh()
			}
		}
	}
	refresh()
	ok := runDialogAs(&dlg, m.mw, "Environment variables — "+s.Name, Size{Width: 640, Height: 380}, []Widget{
		Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
			t.view(edit, col("Name", 200), col("Value", 280), col("Secret", 60)),
			Composite{Layout: VBox{MarginsZero: true}, Children: []Widget{
				PushButton{Text: "Add…", OnClicked: func() {
					var e model.EnvVar
					if envEditDialog(dlg, "Add variable", &e) {
						s.Node.Env = append(s.Node.Env, e)
						refresh()
					}
				}},
				PushButton{Text: "Edit…", OnClicked: edit},
				PushButton{Text: "Remove", OnClicked: func() {
					if i := index(); i >= 0 {
						s.Node.Env = slices.Delete(s.Node.Env, i, i+1)
						refresh()
					}
				}},
				VSpacer{},
			}},
		}},
		Label{Text: "Changes apply with a zero-downtime recycle. Secret values are encrypted at rest.", TextColor: colorMuted},
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
	return runDialog(owner, title, Size{Width: 440}, []Widget{
		Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: []Widget{
			Label{Text: "Name:"},
			LineEdit{AssignTo: &name, Text: e.Name},
			Label{Text: "Value:"},
			LineEdit{AssignTo: &value, Text: shown, PasswordMode: e.Secret, CueBanner: map[bool]string{true: "unchanged"}[stored]},
			Label{},
			CheckBox{AssignTo: &secret, Text: "Secret (encrypted, never shown again)", Checked: e.Secret},
		}},
	}, func(dlg *walk.Dialog) bool {
		n := strings.TrimSpace(name.Text())
		if n == "" || strings.ContainsAny(n, "= \t") {
			return invalid(dlg, "Enter a variable name without spaces or '='.")
		}
		v := value.Text()
		if stored && v == "" && n != e.Name {
			// The server finds a stored secret by its name.
			return invalid(dlg, "Enter the value again: a secret's stored value cannot move to a new name.")
		}
		if stored && v == "" && !secret.Checked() {
			return invalid(dlg, "Enter the value: a secret's stored value is never shown, so it cannot become a plain variable as it is.")
		}
		e.Name, e.Secret = n, secret.Checked()
		if stored && v == "" {
			e.Value = secrets.Mask // keep the stored secret
		} else {
			e.Value = v
		}
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
			PushButton{Text: "…", MaxSize: Size{Width: 30}, OnClicked: func() { browseFolder(m.mw, path, "Folder of "+s.Name) }},
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
	ok := runDialog(m.mw, "Basic settings — "+s.Name, Size{Width: 520}, []Widget{
		Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: fields},
		Label{Text: "Other settings (routing, recycling, limits, health checks) are in the web console.", TextColor: colorMuted},
	}, func(dlg *walk.Dialog) bool {
		s.Name, s.AutoStart = strings.TrimSpace(name.Text()), auto.Checked()
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
	var name, path, entry, target, ip, host *walk.LineEdit
	var typ, proto *walk.ComboBox
	var port *walk.NumberEdit
	var start *walk.CheckBox
	var pathBox *walk.Composite
	var bindingBox *walk.GroupBox
	var pathLabel, entryLabel, targetLabel, httpsNote *walk.Label

	onType := func() {
		if target == nil { // still being created
			return
		}
		t := types[max(typ.CurrentIndex(), 0)]
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
	ok := runDialog(m.mw, "Add site", Size{Width: 560}, []Widget{
		Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: []Widget{
			Label{Text: "Site name:"}, LineEdit{AssignTo: &name},
			Label{Text: "Type:"}, ComboBox{AssignTo: &typ, Model: typeNames, CurrentIndex: 0, OnCurrentIndexChanged: onType},
			Label{AssignTo: &pathLabel, Text: "Physical path:"},
			Composite{AssignTo: &pathBox, Layout: HBox{MarginsZero: true}, Children: []Widget{
				LineEdit{AssignTo: &path, CueBanner: `C:\apps\my-app`},
				PushButton{Text: "…", MaxSize: Size{Width: 30}, OnClicked: func() { browseFolder(m.mw, path, "Folder of the site") }},
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
		Label{AssignTo: &httpsNote, Text: "HTTPS bindings get an automatic certificate for the host name.", TextColor: colorMuted},
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
			m.errorBox("Add site", err)
			return false
		}
		created, startNow = &out, start.Checked()
		return true
	})
	if !ok || created == nil {
		return
	}
	id := created.ID
	if startNow {
		m.siteAction(id, "start")
	}
	// Show the new site once a refresh has put it in the tree.
	m.pendingSite = id
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
	ok := runDialog(m.mw, "Web console settings", Size{Width: 520}, []Widget{
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
		Label{Text: "Use 127.0.0.1:8484 to reach the console from this machine only. The change applies when the service restarts.", TextColor: colorMuted},
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
			!m.confirm("Web console settings", "Without encryption, passwords cross the network in clear text. Serve the console over plain HTTP on "+in.Listen+" anyway?") {
			return false
		}
		var out adminSettings
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := m.cl.Put(ctx, "/api/settings/admin", in, &out); err != nil {
			m.errorBox("Web console settings", err)
			return false
		}
		cur = out
		return true
	})
	if ok && cur.RestartRequired && m.confirm("Web console settings", "Restart the NodeHoster service now to apply the change? Sites go offline for a few seconds.") {
		m.do("Restarting the service", func(context.Context) error { return controlService("restart") })
	}
}
