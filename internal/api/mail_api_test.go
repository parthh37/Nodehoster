package api

import (
	"net/http"
	"net/smtp"
	"strings"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/secrets"
)

func (e *env) settings(admin []opt) model.Settings {
	e.t.Helper()
	rec := e.do(http.MethodGet, "/api/settings", nil, admin...)
	expect(e.t, rec, http.StatusOK)
	return decodeJSON[model.Settings](e.t, rec)
}

// Mail secrets are sealed or hashed, never returned, and survive a
// round trip of the masked settings.
func TestMailSettingsSecrets(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()

	s := e.settings(admin)
	if s.Mail.Port != 25 || s.Mail.Delivery != "direct" || len(s.Mail.AllowIPs) != 2 || s.Mime.UnknownTypes != "serve" {
		t.Fatalf("defaults: %+v %+v", s.Mail, s.Mime)
	}
	s.Mail.Delivery = "smarthost"
	s.Mail.SmartHost = model.MailSmartHost{Host: "smtp.example.net", Port: 587, Security: "starttls", Username: "apikey", Password: "smart-host-secret"}
	s.Mail.Users = []model.MailUser{{Username: "app", Password: "user-secret"}}
	s.Mail.DKIM = []model.DKIMKey{{Domain: "example.com", Selector: "nh", Enabled: true}}
	rec := e.do(http.MethodPut, "/api/settings", s, admin...)
	expect(t, rec, http.StatusOK)
	for _, leak := range []string{"smart-host-secret", "user-secret", "PRIVATE KEY", "$2a$"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("settings response leaks %q", leak)
		}
	}
	saved := decodeJSON[model.Settings](t, rec)
	if saved.Mail.SmartHost.Password != secrets.Mask || saved.Mail.Users[0].PasswordHash != secrets.Mask || saved.Mail.DKIM[0].PrivateKey != secrets.Mask {
		t.Fatalf("not masked: %+v", saved.Mail)
	}
	d := saved.Mail.DKIM[0]
	if d.DNSName != "nh._domainkey.example.com" || !strings.HasPrefix(d.DNSRecord, "v=DKIM1; k=rsa; p=") {
		t.Fatalf("DKIM record: %+v", d)
	}
	stored := e.c.Settings().Mail
	if strings.Contains(stored.SmartHost.Password, "smart-host-secret") || !strings.HasPrefix(stored.Users[0].PasswordHash, "$2a$") ||
		strings.Contains(stored.DKIM[0].PrivateKey, "PRIVATE KEY") {
		t.Fatalf("stored in the clear: %+v", stored)
	}

	// Sending the masked document back changes nothing.
	rec = e.do(http.MethodPut, "/api/settings", saved, admin...)
	expect(t, rec, http.StatusOK)
	again := e.c.Settings().Mail
	if again.SmartHost.Password != stored.SmartHost.Password || again.Users[0].PasswordHash != stored.Users[0].PasswordHash ||
		again.DKIM[0].PrivateKey != stored.DKIM[0].PrivateKey || again.DKIM[0].DNSRecord != d.DNSRecord {
		t.Fatal("a masked round trip changed secrets")
	}

	// A client cannot plant its own password hash.
	saved.Mail.Users[0].PasswordHash = "$2a$04$attackerattackerattackerattackerattackerattackerattac"
	expect(t, e.do(http.MethodPut, "/api/settings", saved, admin...), http.StatusOK)
	if e.c.Settings().Mail.Users[0].PasswordHash != stored.Users[0].PasswordHash {
		t.Fatal("client-supplied hash was stored")
	}
	// A new user needs a password.
	saved.Mail.Users = append(saved.Mail.Users, model.MailUser{Username: "other"})
	rec = e.do(http.MethodPut, "/api/settings", saved, admin...)
	expect(t, rec, http.StatusUnprocessableEntity)
	if f := decodeJSON[map[string]string](t, rec)["field"]; f != "mail.users[1].password" {
		t.Errorf("field = %q", f)
	}
}

func TestMailSettingsValidation(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	for field, mutate := range map[string]func(*model.Settings){
		"mail.smartHost.host": func(s *model.Settings) { s.Mail.Delivery = "smarthost" },
		"mail.smartHost.security": func(s *model.Settings) {
			s.Mail.Delivery, s.Mail.SmartHost = "smarthost", model.MailSmartHost{Host: "h", Port: 25, Security: "none", Username: "u"}
		},
		"mail.allowIps[0]": func(s *model.Settings) { s.Mail.AllowIPs = []string{"not-an-ip"} },
		"mail.users":       func(s *model.Settings) { s.Mail.RequireAuth = true },
		"mail.dkim[0].selector": func(s *model.Settings) {
			s.Mail.DKIM = []model.DKIMKey{{Domain: "example.com", Selector: "bad selector"}}
		},
		"mail.dkim[0].privateKey": func(s *model.Settings) {
			s.Mail.DKIM = []model.DKIMKey{{Domain: "example.com", Selector: "a", PrivateKey: "junk"}}
		},
		"mail.certificateId": func(s *model.Settings) { s.Mail.CertificateID = "missing" },
		"mime.types[0].type": func(s *model.Settings) { s.Mime.Types = []model.MimeMap{{Extension: ".x", Type: "nope"}} },
		"mime.types[1].extension": func(s *model.Settings) {
			s.Mime.Types = []model.MimeMap{{Extension: ".x", Type: "a/b"}, {Extension: ".X", Type: "a/c"}}
		},
	} {
		s := e.settings(admin)
		mutate(&s)
		rec := e.do(http.MethodPut, "/api/settings", s, admin...)
		expect(t, rec, http.StatusUnprocessableEntity)
		if got := decodeJSON[map[string]string](t, rec)["field"]; got != field {
			t.Errorf("%s: field = %q (%s)", field, got, rec.Body.String())
		}
	}
}

func TestMailQueueEndpoints(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	port := freePort(t)
	e.c.Mail.Start() // as core.Start does; the test environment does not start the server

	s := e.settings(admin)
	s.Mail.Enabled, s.Mail.ListenIP, s.Mail.Port = true, "127.0.0.1", port
	// Deliver to a smart host that is not there: mail stays queued.
	s.Mail.Delivery = "smarthost"
	s.Mail.SmartHost = model.MailSmartHost{Host: "127.0.0.1", Port: freePort(t), Security: "none"}
	expect(t, e.do(http.MethodPut, "/api/settings", s, admin...), http.StatusOK)

	st := decodeJSON[model.MailStatus](t, e.do(http.MethodGet, "/api/mail/status", nil, admin...))
	if !st.Listening || st.Addr == "" {
		t.Fatalf("status: %+v", st)
	}
	msg := "From: a@example.com\r\nSubject: Queued\r\n\r\nHi\r\n"
	if err := smtp.SendMail(st.Addr, nil, "a@example.com", []string{"b@example.org"}, []byte(msg)); err != nil {
		t.Fatal(err)
	}
	var q []model.MailMessage
	deadline := time.Now().Add(10 * time.Second)
	for {
		q = decodeJSON[[]model.MailMessage](t, e.do(http.MethodGet, "/api/mail/queue?state=queued", nil, admin...))
		if len(q) == 1 && q[0].Attempts > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue: %+v", q)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if q[0].Subject != "Queued" || !strings.Contains(q[0].LastError, "smart host") {
		t.Errorf("message: %+v", q[0])
	}

	viewer := e.user("viewer", model.RoleViewer, false)
	ro := []opt{withBearer(e.token(viewer))}
	expect(t, e.do(http.MethodGet, "/api/mail/queue", nil, ro...), http.StatusOK)
	expect(t, e.do(http.MethodGet, "/api/mail/queue/"+q[0].ID+"/eml", nil, ro...), http.StatusForbidden)
	expect(t, e.do(http.MethodPost, "/api/mail/queue/"+q[0].ID+"/retry", nil, ro...), http.StatusForbidden)

	rec := e.do(http.MethodGet, "/api/mail/queue/"+q[0].ID+"/eml", nil, admin...)
	expect(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), "Subject: Queued") || rec.Header().Get("Content-Type") != "message/rfc822" {
		t.Errorf("eml: %v %q", rec.Header(), rec.Body.String())
	}
	expect(t, e.do(http.MethodPost, "/api/mail/queue/"+q[0].ID+"/retry", nil, admin...), http.StatusNoContent)
	expect(t, e.do(http.MethodPost, "/api/mail/queue/retry", nil, admin...), http.StatusNoContent)
	expect(t, e.do(http.MethodDelete, "/api/mail/queue/"+q[0].ID, nil, admin...), http.StatusNoContent)
	expect(t, e.do(http.MethodDelete, "/api/mail/queue/"+q[0].ID, nil, admin...), http.StatusNotFound)

	rec = e.do(http.MethodPost, "/api/mail/test", map[string]string{"to": "not an address"}, admin...)
	expect(t, rec, http.StatusUnprocessableEntity)
	rec = e.do(http.MethodPost, "/api/mail/test", map[string]string{"to": "ops@example.org"}, admin...)
	expect(t, rec, http.StatusAccepted)
	if m := decodeJSON[model.MailMessage](t, rec); m.Source != "test" || m.Recipients[0].Address != "ops@example.org" {
		t.Errorf("test message: %+v", m)
	}
	audit := strings.Join(e.auditActions(), " ")
	for _, a := range []string{"mail.download", "mail.retry", "mail.delete", "mail.test"} {
		if !strings.Contains(audit, ":"+a) {
			t.Errorf("audit log lacks %s: %s", a, audit)
		}
	}

	// A site cannot bind the SMTP port, and the SMTP server cannot take a
	// site's port.
	rec = e.do(http.MethodPost, "/api/sites", redirectSite("Clash", port), admin...)
	expect(t, rec, http.StatusUnprocessableEntity)
	if f := decodeJSON[map[string]string](t, rec)["field"]; f != "bindings[0].port" {
		t.Errorf("field = %q", f)
	}
}

func TestRewriteImportAndMimeDefaults(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	rec := e.do(http.MethodPost, "/api/rewrite/import", model.RewriteImportRequest{
		Format: "htaccess", Text: "RewriteEngine On\nRewriteCond %{REQUEST_FILENAME} !-f\nRewriteRule ^(.*)$ index.html [L,QSA]\n",
	}, admin...)
	expect(t, rec, http.StatusOK)
	imp := decodeJSON[model.RewriteImport](t, rec)
	if len(imp.Rules) != 1 || imp.Rules[0].Target != "/index.html" || imp.Rules[0].QueryString != "append" || imp.Warnings == nil {
		t.Fatalf("import: %+v", imp)
	}
	rec = e.do(http.MethodPost, "/api/rewrite/import", model.RewriteImportRequest{Format: "nginx", Text: "x"}, admin...)
	expect(t, rec, http.StatusUnprocessableEntity)

	defs := decodeJSON[[]model.MimeMap](t, e.do(http.MethodGet, "/api/mime/defaults", nil, admin...))
	found := false
	for _, m := range defs {
		found = found || (m.Extension == ".js" && strings.HasPrefix(m.Type, "text/javascript"))
	}
	if len(defs) < 100 || !found {
		t.Fatalf("defaults: %d entries", len(defs))
	}

	// Imported rules save as part of a site; a reference to a missing map
	// is caught with the field that has it.
	body := redirectSite("Rewrites", freePort(t))
	body["routing"] = map[string]any{"rewrites": []map[string]any{
		{"name": "r", "enabled": false, "match": "^/x", "action": "rewrite", "target": "/{Missing:{R:1}}"},
	}}
	rec = e.do(http.MethodPost, "/api/sites", body, admin...)
	expect(t, rec, http.StatusUnprocessableEntity)
	if f := decodeJSON[map[string]string](t, rec)["field"]; f != "routing.rewrites[0].target" {
		t.Errorf("field = %q", f)
	}
	body["routing"] = map[string]any{
		"rewrites":    imp.Rules,
		"rewriteMaps": []map[string]any{{"name": "Missing", "entries": map[string]string{}}},
		"mimeTypes":   []map[string]string{{"extension": ".", "type": "text/plain"}},
	}
	expect(t, e.do(http.MethodPost, "/api/sites", body, admin...), http.StatusCreated)
}
