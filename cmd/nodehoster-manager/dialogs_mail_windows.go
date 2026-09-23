package main

import (
	"net"
	"slices"
	"strings"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/secrets"
	"github.com/tailscale/walk"
	. "github.com/tailscale/walk/declarative"
)

// mailPropertiesDialog edits settings.mail, laid out like the property
// sheet of an IIS 6 SMTP virtual server.
func mailPropertiesDialog(m *manager) {
	var ms model.MailSettings
	if err := m.getSettingsSection("mail", &ms); err != nil {
		m.errorBox("SMTP E-mail properties", err)
		return
	}
	ms.ApplyDefaults()
	certs := m.certificates()

	// General
	var enabled, requireAuth, skipVerify, pickup *walk.CheckBox
	var listenIP, hostname, shHost, shUser, shPass *walk.LineEdit
	var port, maxMB, maxRcpt, shPort, expire, keepFailed *walk.NumberEdit
	var allowIPs, senderDomains, dkimRecord *walk.TextEdit
	var cert, delivery, security *walk.ComboBox
	var smartBox *walk.GroupBox

	// Access: users
	users := slices.Clone(ms.Users)
	var dlg *walk.Dialog
	userList := newEditList(&users, func(u model.MailUser) []string {
		pw := "set"
		if u.Password != "" {
			pw = "new (saved with the settings)"
		}
		return []string{u.Username, pw}
	})
	userList.minHeight = 70
	userList.refresh()
	editUser := func() {
		userList.edit(func(u *model.MailUser) bool { return mailUserDialog(dlg, "Edit user", u) })
	}

	certNames := []string{"None (no STARTTLS)"}
	certIDs := []string{""}
	for _, c := range certs {
		certNames = append(certNames, c.Name+" ("+strings.Join(c.Domains, ", ")+")")
		certIDs = append(certIDs, c.ID)
	}
	if ms.CertificateID != "" && !slices.Contains(certIDs, ms.CertificateID) {
		certNames = append(certNames, "Certificate "+ms.CertificateID+" (not found)")
		certIDs = append(certIDs, ms.CertificateID)
	}

	// Delivery
	deliveries := []string{"direct", "smarthost"}
	deliveryNames := []string{"Directly to the recipients' mail servers (MX records)", "Through a smart host"}
	securities := []string{"starttls", "tls", "none"}
	securityNames := []string{"STARTTLS (usually port 587)", "TLS (usually port 465)", "None (port 25, no password)"}
	sh := ms.SmartHost
	storedPass := sh.Password == secrets.Mask
	onSecurity := func() {
		if shPort == nil {
			return
		}
		switch i := security.CurrentIndex(); {
		case i == 1 && shPort.Value() == 587:
			shPort.SetValue(465)
		case i == 0 && shPort.Value() == 465:
			shPort.SetValue(587)
		}
	}

	// DKIM
	keys := slices.Clone(ms.DKIM)
	dkimList := newEditList(&keys, func(k model.DKIMKey) []string {
		name := k.DNSName
		if name == "" {
			name = "(key generated when saved)"
		}
		return []string{k.Domain, k.Selector, yesNo(k.Enabled), name}
	})
	dkimList.minHeight = 90
	dkimList.refresh()
	showRecord := func() {
		if dkimRecord == nil {
			return
		}
		k := dkimList.current()
		switch {
		case k == nil:
			dkimRecord.SetText("")
		case k.DNSRecord == "":
			dkimRecord.SetText("Save the settings to generate this key. Then open Properties again to copy the DNS record to publish.")
		default:
			dkimRecord.SetText("Publish this TXT record in the DNS zone of " + k.Domain + ":\r\n\r\nName: " + k.DNSName + "\r\n\r\nValue: " + k.DNSRecord)
		}
	}
	dkimList.t.onSelect = showRecord
	copyRecord := func(name bool) {
		if k := dkimList.current(); k != nil && k.DNSRecord != "" {
			if name {
				walk.Clipboard().SetText(k.DNSName)
			} else {
				walk.Clipboard().SetText(k.DNSRecord)
			}
		}
	}

	var saved model.MailSettings
	ok := runDialogAs(&dlg, m.mw, "SMTP E-mail properties", Size{Width: 620, Height: 500}, []Widget{
		TabWidget{Pages: []TabPage{
			{Title: "General", Layout: VBox{}, Children: []Widget{
				CheckBox{AssignTo: &enabled, Text: "Enable the SMTP server", Checked: ms.Enabled},
				Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: []Widget{
					Label{Text: "IP address:"}, LineEdit{AssignTo: &listenIP, Text: ms.ListenIP, CueBanner: "(All unassigned)"},
					Label{Text: "TCP port:"}, NumberEdit{AssignTo: &port, Value: float64(ms.Port), MinValue: 1, MaxValue: 65535},
					Label{Text: "Fully-qualified domain name:"}, LineEdit{AssignTo: &hostname, Text: ms.Hostname, CueBanner: "(this computer's name) e.g. mail.example.com"},
				}},
				Label{Text: "The name is used in the SMTP greeting and in Received headers. Use one that resolves to this server, and whose reverse DNS matches, or mail may be treated as spam.", TextColor: colorMuted},
				VSpacer{},
			}},
			{Title: "Access", Layout: VBox{}, Children: []Widget{
				Label{Text: "Only these computers may connect and relay (IP addresses or ranges such as 10.0.0.0/8, one per line):"},
				TextEdit{AssignTo: &allowIPs, Text: joinLines(ms.AllowIPs), VScroll: true, MinSize: Size{Height: 60}},
				CheckBox{AssignTo: &requireAuth, Text: "Require authentication (clients sign in as one of these users)", Checked: ms.RequireAuth},
				userList.view(editUser, []TableViewColumn{col("User name", 200), col("Password", 180)},
					button("Add…", func() {
						var u model.MailUser
						if mailUserDialog(dlg, "Add user", &u) {
							userList.add(u)
						}
					}),
					button("Edit…", editUser),
					button("Remove", userList.remove),
				),
				Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: []Widget{
					Label{Text: "STARTTLS certificate:"},
					ComboBox{AssignTo: &cert, Model: certNames, CurrentIndex: max(slices.Index(certIDs, ms.CertificateID), 0)},
				}},
				Label{Text: "Passwords are only accepted over TLS or from this computer.", TextColor: colorMuted},
			}},
			{Title: "Messages", Layout: VBox{}, Children: []Widget{
				Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: []Widget{
					Label{Text: "Maximum message size (MB):"}, NumberEdit{AssignTo: &maxMB, Value: float64(ms.MaxMessageMB), MinValue: 1, MaxValue: 150},
					Label{Text: "Maximum recipients per message:"}, NumberEdit{AssignTo: &maxRcpt, Value: float64(ms.MaxRecipients), MinValue: 1, MaxValue: 100000},
				}},
				Label{Text: "Accept mail only from these sender domains (one per line; empty = any):"},
				TextEdit{AssignTo: &senderDomains, Text: joinLines(ms.AllowedSenderDomains), VScroll: true, MinSize: Size{Height: 80}},
				VSpacer{},
			}},
			{Title: "Delivery", Layout: VBox{}, Children: []Widget{
				Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: []Widget{
					Label{Text: "Deliver mail:"},
					ComboBox{AssignTo: &delivery, Model: deliveryNames, CurrentIndex: max(slices.Index(deliveries, ms.Delivery), 0),
						OnCurrentIndexChanged: func() {
							if smartBox != nil {
								smartBox.SetEnabled(delivery.CurrentIndex() == 1)
							}
						}},
				}},
				GroupBox{AssignTo: &smartBox, Title: "Smart host", Enabled: ms.Delivery == "smarthost", Layout: Grid{Columns: 2}, Children: []Widget{
					Label{Text: "Server:"}, LineEdit{AssignTo: &shHost, Text: sh.Host, CueBanner: "smtp.sendgrid.net"},
					Label{Text: "Security:"}, ComboBox{AssignTo: &security, Model: securityNames, CurrentIndex: max(slices.Index(securities, sh.Security), 0), OnCurrentIndexChanged: onSecurity},
					Label{Text: "Port:"}, NumberEdit{AssignTo: &shPort, Value: float64(sh.Port), MinValue: 1, MaxValue: 65535},
					Label{Text: "User name:"}, LineEdit{AssignTo: &shUser, Text: sh.Username, CueBanner: "(no authentication)"},
					Label{Text: "Password:"}, LineEdit{AssignTo: &shPass, PasswordMode: true, Text: map[bool]string{false: sh.Password}[storedPass],
						CueBanner: map[bool]string{true: "unchanged"}[storedPass]},
					Label{}, CheckBox{AssignTo: &skipVerify, Text: "Ignore certificate errors (not recommended)", Checked: sh.InsecureSkipVerify},
				}},
				Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: []Widget{
					Label{Text: "Give up on a message after (hours):"}, NumberEdit{AssignTo: &expire, Value: float64(ms.ExpireHours), MinValue: 1, MaxValue: 720},
					Label{Text: "Keep undeliverable mail for (days):"}, NumberEdit{AssignTo: &keepFailed, Value: float64(ms.KeepFailedDays), MinValue: 1, MaxValue: 365},
				}},
				CheckBox{AssignTo: &pickup, Text: `Send .eml files dropped in the pickup folder (data\mail\pickup)`, Checked: ms.PickupDirectory},
				VSpacer{},
			}},
			{Title: "DKIM", Layout: VBox{}, Children: []Widget{
				Label{Text: "Sign outgoing mail with DKIM, so receivers can verify it came from this server:"},
				dkimList.view(nil, []TableViewColumn{col("Domain", 150), col("Selector", 100), col("Enabled", 60), col("DNS name", 220)},
					button("Add…", func() {
						// Signing starts off: turn it on once the record is published.
						k := model.DKIMKey{Selector: "nodehoster"}
						if dkimKeyDialog(dlg, &k) {
							dkimList.add(k)
							showRecord()
						}
					}),
					button("Enable/Disable", func() {
						if k := dkimList.current(); k != nil {
							k.Enabled = !k.Enabled
							dkimList.refresh()
						}
					}),
					button("Remove", func() {
						if k := dkimList.current(); k != nil && walk.MsgBox(dlg, "Remove DKIM key",
							"Remove the key of "+k.Domain+"? Its private key is deleted when the settings are saved; mail from "+k.Domain+" is no longer signed.",
							walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) == walk.DlgCmdYes {
							dkimList.remove()
							showRecord()
						}
					}),
				),
				TextEdit{AssignTo: &dkimRecord, ReadOnly: true, VScroll: true, MinSize: Size{Height: 80}, Font: Font{Family: "Consolas", PointSize: 9}},
				Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
					HSpacer{},
					button("Copy name", func() { copyRecord(true) }),
					button("Copy record", func() { copyRecord(false) }),
				}},
			}},
		}},
	}, func(dlg *walk.Dialog) bool {
		in := ms
		in.Enabled = enabled.Checked()
		in.ListenIP = strings.TrimSpace(listenIP.Text())
		if in.ListenIP == "*" {
			in.ListenIP = ""
		}
		if in.ListenIP != "" && net.ParseIP(in.ListenIP) == nil {
			return invalid(dlg, "Enter an IP address for the server to listen on, or leave it empty for all addresses.")
		}
		in.Port = int(port.Value())
		in.Hostname = strings.TrimSpace(hostname.Text())

		in.AllowIPs = lines(allowIPs.Text())
		if in.AllowIPs == nil {
			in.AllowIPs = []string{}
		}
		in.RequireAuth = requireAuth.Checked()
		in.Users = users
		if in.RequireAuth && len(users) == 0 {
			return invalid(dlg, "Add at least one user, or turn off authentication.")
		}
		in.CertificateID = certIDs[max(cert.CurrentIndex(), 0)]

		in.MaxMessageMB, in.MaxRecipients = int(maxMB.Value()), int(maxRcpt.Value())
		in.AllowedSenderDomains = lines(strings.ToLower(senderDomains.Text()))

		in.Delivery = deliveries[max(delivery.CurrentIndex(), 0)]
		in.SmartHost = model.MailSmartHost{
			Host:               strings.TrimSpace(shHost.Text()),
			Port:               int(shPort.Value()),
			Security:           securities[max(security.CurrentIndex(), 0)],
			Username:           strings.TrimSpace(shUser.Text()),
			Password:           shPass.Text(),
			InsecureSkipVerify: skipVerify.Checked(),
		}
		switch {
		case in.SmartHost.Username == "":
			in.SmartHost.Password = "" // no authentication
		case in.SmartHost.Password == "" && storedPass:
			in.SmartHost.Password = secrets.Mask // keep the stored password
		}
		in.ExpireHours, in.KeepFailedDays = int(expire.Value()), int(keepFailed.Value())
		in.PickupDirectory = pickup.Checked()
		in.DKIM = keys

		if err := m.putSettingsSection("mail", in, &saved); err != nil {
			m.errorBoxFor(dlg, "SMTP E-mail properties", err)
			return false
		}
		return true
	})
	if !ok {
		return
	}
	m.refresh(true)

	// Keys added in this dialog were generated by the server: show what to
	// publish. (Keys that had a record before were published already.)
	var records []string
	for _, k := range saved.DKIM {
		isNew := true
		for _, old := range ms.DKIM {
			if old.Domain == k.Domain && old.Selector == k.Selector && old.DNSRecord != "" {
				isNew = false
			}
		}
		if isNew && k.DNSRecord != "" {
			records = append(records, "Name: "+k.DNSName+"\r\nValue: "+k.DNSRecord)
		}
	}
	if len(records) > 0 {
		dkimRecordsDialog(m.mw, strings.Join(records, "\r\n\r\n"))
	}
}

// mailUserDialog edits a user who may sign in to the SMTP server. The
// password is write-only: left empty, the stored one is kept.
func mailUserDialog(owner walk.Form, title string, u *model.MailUser) bool {
	var name, pw, confirm *walk.LineEdit
	stored := u.PasswordHash != "" && u.Password == ""
	cue := map[bool]string{true: "unchanged"}[stored]
	return runDialog(owner, title, Size{Width: 400}, []Widget{
		Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: []Widget{
			Label{Text: "User name:"}, LineEdit{AssignTo: &name, Text: u.Username},
			Label{Text: "Password:"}, LineEdit{AssignTo: &pw, PasswordMode: true, Text: u.Password, CueBanner: cue},
			Label{Text: "Confirm password:"}, LineEdit{AssignTo: &confirm, PasswordMode: true, Text: u.Password, CueBanner: cue},
		}},
	}, func(dlg *walk.Dialog) bool {
		n := strings.TrimSpace(name.Text())
		if n == "" || strings.ContainsAny(n, " \t") {
			return invalid(dlg, "Enter a user name without spaces.")
		}
		p := pw.Text()
		if p != confirm.Text() {
			return invalid(dlg, "The passwords do not match.")
		}
		if p == "" && !stored {
			return invalid(dlg, "Enter a password.")
		}
		if p == "" && n != u.Username {
			// The server finds a stored password by the user name.
			return invalid(dlg, "Enter the password again: a stored password cannot move to a new user name.")
		}
		u.Username, u.Password = n, p
		return true
	})
}

func dkimKeyDialog(owner walk.Form, k *model.DKIMKey) bool {
	var domain, selector *walk.LineEdit
	var enabled *walk.CheckBox
	return runDialog(owner, "Add DKIM key", Size{Width: 420}, []Widget{
		Composite{Layout: Grid{Columns: 2, MarginsZero: true}, Children: []Widget{
			Label{Text: "Domain:"}, LineEdit{AssignTo: &domain, Text: k.Domain, CueBanner: "example.com"},
			Label{Text: "Selector:"}, LineEdit{AssignTo: &selector, Text: k.Selector},
			Label{}, CheckBox{AssignTo: &enabled, Text: "Sign mail from this domain", Checked: k.Enabled},
		}},
		Label{Text: "A new RSA-2048 key is generated when the settings are saved.", TextColor: colorMuted},
	}, func(dlg *walk.Dialog) bool {
		d := strings.ToLower(strings.TrimSpace(domain.Text()))
		sel := strings.ToLower(strings.TrimSpace(selector.Text()))
		if !strings.Contains(d, ".") || strings.ContainsAny(d, " /@") {
			return invalid(dlg, "Enter the domain mail is sent from, such as example.com.")
		}
		if sel == "" || strings.ContainsAny(sel, " /") {
			return invalid(dlg, "Enter a selector, such as nodehoster.")
		}
		k.Domain, k.Selector, k.Enabled, k.PrivateKey = d, sel, enabled.Checked(), ""
		return true
	})
}

// dkimRecordsDialog shows the DNS records of newly generated DKIM keys.
func dkimRecordsDialog(owner walk.Form, text string) {
	runDialog(owner, "Publish the DKIM records", Size{Width: 560, Height: 300}, []Widget{
		Label{Text: "New DKIM keys were generated. Publish these TXT records in DNS; receivers verify signatures once they resolve:"},
		TextEdit{Text: text, ReadOnly: true, VScroll: true, Font: Font{Family: "Consolas", PointSize: 9}},
		Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{
			HSpacer{},
			button("Copy all", func() { walk.Clipboard().SetText(strings.ReplaceAll(text, "\r\n", "\n")) }),
		}},
		Label{Text: "They are also shown in Properties → DKIM.", TextColor: colorMuted},
	}, nil)
}
