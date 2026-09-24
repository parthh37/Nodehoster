package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/parthh37/nodehoster/internal/certs"
	"github.com/parthh37/nodehoster/internal/config"
	"github.com/parthh37/nodehoster/internal/model"
)

func (a *API) serverInfo(w http.ResponseWriter, r *http.Request) {
	info := a.c.ServerInfo()
	if access(r).SiteScoped() {
		// What the console's frame shows; the rest (data folder,
		// listeners, load, admin URL) is the server's business.
		info = model.ServerInfo{Version: info.Version, Commit: info.Commit, Hostname: info.Hostname, Listeners: []string{}}
	}
	writeJSON(w, http.StatusOK, info)
}

func (a *API) serverMetrics(w http.ResponseWriter, r *http.Request) {
	mins := intParam(r, "minutes", 60, 7*24*60)
	pts, err := a.c.Store.ListMetrics(r.Context(), "", time.Now().Add(-time.Duration(mins)*time.Minute))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pts)
}

func (a *API) listEvents(w http.ResponseWriter, r *http.Request) {
	siteID, limit := r.URL.Query().Get("siteId"), intParam(r, "limit", 100, 1000)
	var list []model.Event
	var err error
	switch acc := access(r); {
	case !acc.SiteScoped():
		list, err = a.c.Store.ListEvents(r.Context(), siteID, limit)
	case siteID != "":
		list = []model.Event{}
		if acc.CanSee(siteID) {
			list, err = a.c.Store.ListEvents(r.Context(), siteID, limit)
		}
	default:
		list, err = a.c.Store.ListSiteEvents(r.Context(), acc.SiteIDs(), limit)
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *API) listAudit(w http.ResponseWriter, r *http.Request) {
	offset := intParam(r, "offset", 0, 1<<30)
	list, err := a.c.Store.ListAudit(r.Context(), intParam(r, "limit", 100, 1000), offset)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// stream pushes every site's status every two seconds and events as they
// happen, so the UI never has to poll. A site-scoped caller gets only their
// sites' status and events.
func (a *API) stream(w http.ResponseWriter, r *http.Request) {
	events, cancel := a.c.Bus.Subscribe()
	defer cancel()
	acc := access(r)
	s := newSSE(w)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	sendStatus := func() error {
		sites := visibleSites(acc, a.c.Sites())
		out := make([]model.SiteStatus, 0, len(sites))
		for _, site := range sites {
			out = append(out, a.c.Status(site))
		}
		return s.send("status", out)
	}
	if sendStatus() != nil {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-events:
			if !eventVisible(acc, e) {
				continue
			}
			if s.send("event", e) != nil {
				return
			}
		case <-tick.C:
			var ok bool
			if acc, ok = a.currentAccess(r); !ok || sendStatus() != nil {
				return
			}
		}
	}
}

// ---- Node.js runtimes

func (a *API) nodeVersions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"system":    a.c.Nodes.System(),
		"installed": a.c.Nodes.List(a.c.Settings().DefaultNodeVersion),
	})
}

func (a *API) nodeAvailable(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	list, err := a.c.Nodes.Available(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *API) installNode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version string `json:"version"`
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	if err := a.c.Nodes.Install(in.Version); err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "node.install", in.Version, "")
	w.WriteHeader(http.StatusAccepted)
}

func (a *API) removeNode(w http.ResponseWriter, r *http.Request) {
	v := chi.URLParam(r, "version")
	if used := a.c.NodeVersionInUse(v); len(used) > 0 {
		writeErr(w, http.StatusConflict, fmt.Sprintf("Node.js %s is used by %s", v, strings.Join(used, ", ")))
		return
	}
	if err := a.c.Nodes.Remove(v); err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "node.remove", v, "")
	w.WriteHeader(http.StatusNoContent)
}

// ---- settings

func (a *API) getSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.c.MaskedSettings())
}

func (a *API) putSettings(w http.ResponseWriter, r *http.Request) {
	var in model.Settings
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	if _, err := a.c.UpdateSettings(r.Context(), in); err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "settings.update", "server", "")
	writeJSON(w, http.StatusOK, a.c.MaskedSettings())
}

func (a *API) dnsCatalog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, certs.Catalog)
}

func (a *API) testWebhook(w http.ResponseWriter, r *http.Request) {
	var in model.WebhookTarget
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	e := model.Event{Time: time.Now(), Level: "info", Type: "test", Message: "Test notification from NodeHoster"}
	if err := a.c.Bus.Send(ctx, in, e); err != nil {
		writeErr(w, http.StatusBadGateway, "delivery failed: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type adminSettings struct {
	Listen          string `json:"listen"`
	TLS             string `json:"tls"`
	CertificateID   string `json:"certificateId"`
	RestartRequired bool   `json:"restartRequired"`
}

func (a *API) getAdminSettings(w http.ResponseWriter, r *http.Request) {
	saved, _ := config.LoadBootstrap(a.c.Paths.Config)
	running := a.c.Boot.Admin
	writeJSON(w, http.StatusOK, adminSettings{
		Listen: saved.Admin.Listen, TLS: saved.Admin.TLS, CertificateID: saved.Admin.CertificateID,
		RestartRequired: saved.Admin != running,
	})
}

func (a *API) putAdminSettings(w http.ResponseWriter, r *http.Request) {
	var in adminSettings
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	if !strings.Contains(in.Listen, ":") {
		a.fail(w, &model.ValidationError{Field: "listen", Message: "use address:port, e.g. 0.0.0.0:8484"})
		return
	}
	switch in.TLS {
	case "selfsigned", "none":
		in.CertificateID = ""
	case "certificate":
		if a.c.Certs.Get(in.CertificateID) == nil {
			a.fail(w, &model.ValidationError{Field: "certificateId", Message: "select an issued certificate"})
			return
		}
	default:
		a.fail(w, &model.ValidationError{Field: "tls", Message: "selfsigned, certificate or none"})
		return
	}
	boot, _ := config.LoadBootstrap(a.c.Paths.Config)
	boot.Admin = config.AdminConfig{Listen: in.Listen, TLS: in.TLS, CertificateID: in.CertificateID}
	if err := config.SaveBootstrap(a.c.Paths.Config, boot); err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "settings.admin", in.Listen, in.TLS)
	in.RestartRequired = boot.Admin != a.c.Boot.Admin
	writeJSON(w, http.StatusOK, in)
}

// ---- backup

// backup downloads the configuration export (JSON), or with ?format=zip
// an archive with the contents and passphrase of the backup settings.
func (a *API) backup(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("format") == "zip" {
		path, name, err := a.c.BackupArchive(r.Context())
		if err != nil {
			a.backupFail(w, err)
			return
		}
		defer os.Remove(path)
		f, err := os.Open(path)
		if err != nil {
			a.fail(w, err)
			return
		}
		defer f.Close()
		a.audit(r, "backup.download", "server", name)
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		if st, err := f.Stat(); err == nil {
			w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
		}
		io.Copy(w, f)
		return
	}
	data, err := a.c.Backup(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "backup.download", "server", "")
	name := fmt.Sprintf("nodehoster-backup-%s.json", time.Now().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Write(data)
}

// restore accepts a configuration export (.json) or a backup archive
// (.zip), see receiveBackup for the upload forms.
func (a *API) restore(w http.ResponseWriter, r *http.Request) {
	path, passphrase, err := a.receiveBackup(w, r)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer os.Remove(path)
	res, err := a.c.RestoreFile(r.Context(), path, passphrase)
	if err != nil {
		a.backupFail(w, err)
		return
	}
	a.audit(r, "backup.restore", "server", res.Format)
	writeJSON(w, http.StatusOK, res)
}

// ---- Prometheus

func (a *API) prometheus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	var b strings.Builder
	metric := func(name, help, typ string) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
	}
	sites := visibleSites(access(r), a.c.Sites())
	statuses := make([]model.SiteStatus, len(sites))
	for i, s := range sites {
		statuses[i] = a.c.Status(s)
	}
	label := func(s *model.Site) string {
		return fmt.Sprintf(`site=%q,type=%q`, s.Name, s.Type)
	}
	metric("nodehoster_site_up", "1 if the site is running", "gauge")
	for i, s := range sites {
		up := 0
		if statuses[i].State == model.StateRunning || statuses[i].State == model.StateDegraded {
			up = 1
		}
		fmt.Fprintf(&b, "nodehoster_site_up{%s} %d\n", label(s), up)
	}
	metric("nodehoster_requests_total", "Requests served, by status class", "counter")
	for i, s := range sites {
		t := statuses[i].Traffic
		for code, v := range map[string]int64{"2xx": t.Status2xx, "3xx": t.Status3xx, "4xx": t.Status4xx, "5xx": t.Status5xx} {
			fmt.Fprintf(&b, "nodehoster_requests_total{%s,code=%q} %d\n", label(s), code, v)
		}
	}
	metric("nodehoster_bytes_sent_total", "Response bytes sent", "counter")
	for i, s := range sites {
		fmt.Fprintf(&b, "nodehoster_bytes_sent_total{%s} %d\n", label(s), statuses[i].Traffic.BytesOut)
	}
	metric("nodehoster_instance_memory_bytes", "Working set of an instance's process tree", "gauge")
	for i, s := range sites {
		for _, in := range statuses[i].Instances {
			fmt.Fprintf(&b, "nodehoster_instance_memory_bytes{%s,instance=\"%d\"} %d\n", label(s), in.Index, in.MemoryBytes)
		}
	}
	metric("nodehoster_instance_cpu_percent", "CPU usage of an instance's process tree", "gauge")
	for i, s := range sites {
		for _, in := range statuses[i].Instances {
			fmt.Fprintf(&b, "nodehoster_instance_cpu_percent{%s,instance=\"%d\"} %.2f\n", label(s), in.Index, in.CPUPercent)
		}
	}
	metric("nodehoster_instance_restarts_total", "Restarts of an instance", "counter")
	for i, s := range sites {
		for _, in := range statuses[i].Instances {
			fmt.Fprintf(&b, "nodehoster_instance_restarts_total{%s,instance=\"%d\"} %d\n", label(s), in.Index, in.Restarts)
		}
	}
	a.prometheusAlerts(&b, access(r))
	if access(r).SiteScoped() {
		// Certificates are server-wide.
		io.WriteString(w, b.String())
		return
	}
	metric("nodehoster_certificate_expiry_seconds", "Seconds until a certificate expires", "gauge")
	if list, err := a.c.Store.ListCertificates(r.Context()); err == nil {
		for _, c := range list {
			if c.NotAfter != nil {
				fmt.Fprintf(&b, "nodehoster_certificate_expiry_seconds{name=%q} %.0f\n", c.Name, time.Until(*c.NotAfter).Seconds())
			}
		}
	}
	io.WriteString(w, b.String())
}
