package main

import (
	"context"
	"encoding/hex"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/parthh37/nodehoster/internal/desktop"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/tailscale/walk"
	. "github.com/tailscale/walk/declarative"
)

// ---- client certificates of a binding (IIS "SSL Settings")

// clientCertField is the "Client certificates" row of the binding dialog:
// a summary and a button that opens clientCertDialog.
type clientCertField struct {
	cc    *model.ClientCertPolicy // the edited copy; nil = ignore
	label *walk.Label
	btn   *walk.PushButton
}

func newClientCertField(cc *model.ClientCertPolicy) *clientCertField {
	return &clientCertField{cc: cloneClientCert(cc)}
}

func (f *clientCertField) text() string {
	if s := desktop.ClientCertText(f.cc); s != "" {
		return s
	}
	return "Ignore"
}

// widgets is the label and the row, for a two-column grid. owner is the
// binding dialog, once it exists.
func (f *clientCertField) widgets(owner **walk.Dialog, enabled bool) []Widget {
	return []Widget{
		Label{Text: "Client certificates:"},
		Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
			Label{AssignTo: &f.label, Text: f.text(), TextColor: colorMuted},
			HSpacer{},
			PushButton{AssignTo: &f.btn, Text: "Settings…", Enabled: enabled, OnClicked: func() {
				if cc, ok := clientCertDialog(*owner, f.cc); ok {
					f.cc = cc
					f.label.SetText(f.text())
				}
			}},
		}},
	}
}

func (f *clientCertField) setEnabled(on bool) {
	if f.btn != nil {
		f.btn.SetEnabled(on)
	}
}

func cloneClientCert(p *model.ClientCertPolicy) *model.ClientCertPolicy {
	if p == nil {
		return nil
	}
	c := *p
	c.AllowedSubjects = slices.Clone(p.AllowedSubjects)
	c.AllowedFingerprints = slices.Clone(p.AllowedFingerprints)
	c.RequirePaths = slices.Clone(p.RequirePaths)
	return &c
}

var clientCertModes = []string{model.ClientCertIgnore, model.ClientCertAccept, model.ClientCertRequire}

// clientCertDialog edits a binding's client certificate policy: the mode,
// the trusted CAs (PEM, pasted or loaded from files) and the allow lists.
func clientCertDialog(owner walk.Form, in *model.ClientCertPolicy) (*model.ClientCertPolicy, bool) {
	cur := model.ClientCertPolicy{Mode: model.ClientCertIgnore}
	if in != nil {
		cur = *cloneClientCert(in)
	}
	var mode *walk.ComboBox
	var caPem, subjects, fingerprints, paths *walk.TextEdit
	var caNote *walk.TextLabel
	var self *walk.Dialog
	var result *model.ClientCertPolicy
	idx := max(slices.Index(clientCertModes, cur.Mode), 0)
	caText := func(pem string) string {
		cas, err := model.ParseCABundle(pem)
		switch {
		case strings.TrimSpace(pem) == "":
			return "Paste the CA certificates (PEM) that issue the client certificates, or load them from files."
		case err != nil:
			return err.Error()
		}
		var names []string
		for _, c := range cas {
			names = append(names, c.Subject.CommonName)
		}
		return strings.Join(names, ", ")
	}
	onMode := func() {
		if paths == nil || caPem == nil {
			return // still being created
		}
		m := clientCertModes[max(mode.CurrentIndex(), 0)]
		for _, w := range []*walk.TextEdit{caPem, subjects, fingerprints} {
			w.SetEnabled(m != model.ClientCertIgnore)
		}
		paths.SetEnabled(m == model.ClientCertAccept)
	}
	mono := Font{Family: "Consolas", PointSize: 9}
	on := cur.Mode != model.ClientCertIgnore
	ok := runDialogAs(&self, owner, "Client certificates", Size{Width: 620, Height: 560}, []Widget{
		intro(desktop.IconKey, "Ask HTTPS clients for a certificate (mutual TLS), like IIS SSL Settings. Require fails the TLS handshake without a certificate from these CAs; Accept serves everyone and tells the application (X-Client-Verify, X-Client-Cert…)."),
		Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: []Widget{
			Label{Text: "Client certificates:"},
			ComboBox{AssignTo: &mode, Model: []string{"Ignore", "Accept (optional)", "Require"}, CurrentIndex: idx, OnCurrentIndexChanged: onMode},
		}},
		Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
			Label{Text: "Trusted certificate authorities (PEM):"},
			HSpacer{},
			PushButton{Text: "Load from file…", Image: img(desktop.IconImport), OnClicked: func() {
				fd := walk.FileDialog{Title: "Certificate authorities", Filter: "Certificates (*.pem;*.crt;*.cer)|*.pem;*.crt;*.cer|All files (*.*)|*.*"}
				if ok, _ := fd.ShowOpen(self); !ok {
					return
				}
				data, err := os.ReadFile(fd.FilePath)
				if err != nil {
					walk.MsgBox(self, "Client certificates", err.Error(), walk.MsgBoxIconError)
					return
				}
				text := strings.TrimSpace(caPem.Text())
				if text != "" {
					text += "\r\n"
				}
				caPem.SetText(text + strings.ReplaceAll(strings.TrimSpace(string(data)), "\n", "\r\n"))
				caNote.SetText(caText(caPem.Text()))
			}},
		}},
		TextEdit{AssignTo: &caPem, Text: strings.ReplaceAll(cur.CAPEM, "\n", "\r\n"), Enabled: on, VScroll: true, MinSize: Size{Height: 110}, Font: mono,
			OnTextChanged: func() {
				if caNote != nil {
					caNote.SetText(caText(caPem.Text()))
				}
			}},
		TextLabel{AssignTo: &caNote, Text: caText(cur.CAPEM), TextColor: colorMuted},
		Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: []Widget{
			Label{Text: "Allowed subjects (optional, one per line):"},
			Label{Text: "Allowed SHA-256 fingerprints (optional):"},
			TextEdit{AssignTo: &subjects, Text: joinLines(cur.AllowedSubjects), Enabled: on, VScroll: true, MinSize: Size{Height: 60}},
			TextEdit{AssignTo: &fingerprints, Text: joinLines(cur.AllowedFingerprints), Enabled: on, VScroll: true, MinSize: Size{Height: 60}, Font: mono},
		}},
		Label{Text: "Accept only: require a certificate for these paths (one per line, e.g. /admin):"},
		TextEdit{AssignTo: &paths, Text: joinLines(cur.RequirePaths), Enabled: cur.Mode == model.ClientCertAccept, VScroll: true, MinSize: Size{Height: 44}},
	}, func(dlg *walk.Dialog) bool {
		out := &model.ClientCertPolicy{
			Mode:                clientCertModes[max(mode.CurrentIndex(), 0)],
			CAPEM:               strings.TrimSpace(strings.ReplaceAll(caPem.Text(), "\r\n", "\n")),
			AllowedSubjects:     lines(subjects.Text()),
			AllowedFingerprints: lines(fingerprints.Text()),
			RequirePaths:        lines(paths.Text()),
		}
		if out.Mode != model.ClientCertIgnore {
			if _, err := model.ParseCABundle(out.CAPEM); err != nil {
				return invalid(dlg, "Trusted certificate authorities: "+err.Error())
			}
			for i, f := range out.AllowedFingerprints {
				out.AllowedFingerprints[i] = model.NormalizeFingerprint(f)
				if b, err := hex.DecodeString(out.AllowedFingerprints[i]); err != nil || len(b) != 32 {
					return invalid(dlg, f+" is not a SHA-256 fingerprint (64 hex digits).")
				}
			}
			for _, p := range out.RequirePaths {
				if !strings.HasPrefix(p, "/") {
					return invalid(dlg, "Paths start with /: "+p)
				}
			}
		}
		if out.Mode == model.ClientCertIgnore && out.CAPEM == "" && len(out.AllowedSubjects)+len(out.AllowedFingerprints)+len(out.RequirePaths) == 0 {
			out = nil
		}
		result = out
		return true
	})
	return result, ok
}

// ---- OCSP on the certificates page

// checkOCSPSelected asks the selected certificate's OCSP responder now.
func (s *certsPage) checkOCSPSelected(m *manager) {
	c := s.current()
	if c == nil {
		return
	}
	id, name := c.ID, c.Name
	// The list shows the result when it refreshes afterwards.
	m.do("Checking OCSP for "+name, func(ctx context.Context) error {
		return m.cl.Post(ctx, "/api/certificates/"+url.PathEscape(id)+"/ocsp", nil, nil)
	})
}
