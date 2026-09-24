package api

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/parthh37/nodehoster/internal/core"
	"github.com/parthh37/nodehoster/internal/model"
)

type siteView struct {
	*model.Site
	Status model.SiteStatus `json:"status"`
}

func (a *API) view(s *model.Site) siteView {
	return siteView{Site: core.Masked(s), Status: a.c.Status(s)}
}

func (a *API) site(w http.ResponseWriter, r *http.Request) *model.Site {
	s, err := a.c.Site(chi.URLParam(r, "id"))
	if err != nil {
		a.fail(w, err)
		return nil
	}
	return s
}

func (a *API) listSites(w http.ResponseWriter, r *http.Request) {
	out := []siteView{}
	for _, s := range visibleSites(access(r), a.c.Sites()) {
		out = append(out, a.view(s))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) getSite(w http.ResponseWriter, r *http.Request) {
	if s := a.site(w, r); s != nil {
		writeJSON(w, http.StatusOK, a.view(s))
	}
}

func (a *API) siteStatus(w http.ResponseWriter, r *http.Request) {
	if s := a.site(w, r); s != nil {
		writeJSON(w, http.StatusOK, a.c.Status(s))
	}
}

func (a *API) createSite(w http.ResponseWriter, r *http.Request) {
	var in model.Site
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	s, err := a.c.CreateSite(r.Context(), &in)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "site.create", s.Name, string(s.Type))
	writeJSON(w, http.StatusCreated, a.view(s))
}

func (a *API) updateSite(w http.ResponseWriter, r *http.Request) {
	var in model.Site
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	s, err := a.c.UpdateSite(r.Context(), chi.URLParam(r, "id"), &in)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "site.update", s.Name, "")
	writeJSON(w, http.StatusOK, a.view(s))
}

func (a *API) deleteSite(w http.ResponseWriter, r *http.Request) {
	s := a.site(w, r)
	if s == nil {
		return
	}
	files := r.URL.Query().Get("deleteFiles") == "true"
	if err := a.c.DeleteSite(r.Context(), s.ID, files); err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "site.delete", s.Name, map[bool]string{true: "with files"}[files])
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) siteAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := a.site(w, r)
		if s == nil {
			return
		}
		var err error
		switch action {
		case "start":
			err = a.c.StartSite(s.ID)
		case "stop":
			err = a.c.StopSite(s.ID)
		case "restart":
			err = a.c.RestartSite(s.ID)
		case "recycle":
			err = a.c.RecycleSite(s.ID)
		}
		a.audit(r, "site."+action, s.Name, errString(err))
		if err != nil {
			a.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, a.c.Status(s))
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (a *API) siteMetrics(w http.ResponseWriter, r *http.Request) {
	s := a.site(w, r)
	if s == nil {
		return
	}
	mins := intParam(r, "minutes", 60, 7*24*60)
	pts, err := a.c.Store.ListMetrics(r.Context(), s.ID, time.Now().Add(-time.Duration(mins)*time.Minute))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pts)
}

// ---- logs

func (a *API) siteLogs(w http.ResponseWriter, r *http.Request) {
	s := a.site(w, r)
	if s == nil {
		return
	}
	n := intParam(r, "lines", 500, 5000)
	if r.URL.Query().Get("type") == "access" {
		lines := tailFile(a.c.Proxy.AccessLogPath(s.ID), n)
		out := make([]model.LogLine, 0, len(lines))
		for _, l := range lines {
			out = append(out, model.LogLine{Stream: "access", Instance: -1, Text: l})
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	lines := a.c.Procs.Logs(s.ID).Recent(n)
	if lines == nil {
		lines = []model.LogLine{}
	}
	writeJSON(w, http.StatusOK, lines)
}

func (a *API) siteLogStream(w http.ResponseWriter, r *http.Request) {
	s := a.site(w, r)
	if s == nil {
		return
	}
	ctx, stop := a.siteStreamContext(r, s.ID, model.RoleViewer)
	defer stop()
	stream := newSSE(w)
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	if r.URL.Query().Get("type") == "access" {
		a.followFile(ctx, stream, a.c.Proxy.AccessLogPath(s.ID), ping)
		return
	}
	ch, cancel := a.c.Procs.Logs(s.ID).Subscribe()
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case l := <-ch:
			if stream.send("log", l) != nil {
				return
			}
		case <-ping.C:
			if stream.ping() != nil {
				return
			}
		}
	}
}

// followFile streams lines appended to a file (the access log).
func (a *API) followFile(ctx context.Context, stream *sse, path string, ping *time.Ticker) {
	var offset int64
	if st, err := os.Stat(path); err == nil {
		offset = st.Size()
	}
	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	var partial string
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			if stream.ping() != nil {
				return
			}
		case <-poll.C:
			st, err := os.Stat(path)
			if err != nil {
				continue
			}
			if st.Size() < offset {
				offset = 0 // rotated
			}
			if st.Size() == offset {
				continue
			}
			f, err := os.Open(path)
			if err != nil {
				continue
			}
			f.Seek(offset, io.SeekStart)
			data, _ := io.ReadAll(io.LimitReader(f, 1<<20))
			f.Close()
			offset += int64(len(data))
			text := partial + string(data)
			lines := strings.Split(text, "\n")
			partial = lines[len(lines)-1]
			for _, l := range lines[:len(lines)-1] {
				if stream.send("log", model.LogLine{Time: time.Now(), Stream: "access", Instance: -1, Text: l}) != nil {
					return
				}
			}
		}
	}
}

// tailFile returns the last n lines of a file.
func tailFile(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return []string{}
	}
	defer f.Close()
	st, _ := f.Stat()
	const chunk = 64 << 10
	size := st.Size()
	var buf []byte
	for off := size; off > 0 && strings.Count(string(buf), "\n") <= n; {
		read := int64(chunk)
		if off < read {
			read = off
		}
		off -= read
		b := make([]byte, read)
		f.ReadAt(b, off)
		buf = append(b, buf...)
		if len(buf) > 32<<20 {
			break
		}
	}
	var lines []string
	sc := bufio.NewScanner(strings.NewReader(string(buf)))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

func (a *API) siteLogDownload(w http.ResponseWriter, r *http.Request) {
	s := a.site(w, r)
	if s == nil {
		return
	}
	path, name := a.c.Procs.Logs(s.ID).Path(), "app.log"
	if r.URL.Query().Get("type") == "access" {
		path, name = a.c.Proxy.AccessLogPath(s.ID), "access.log"
	}
	f, err := os.Open(path)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no log has been written yet")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+safeName(s.Name)+"-"+name+`"`)
	io.Copy(w, f)
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '"' || r == '\\' || r == '/' || r < 32 {
			return '_'
		}
		return r
	}, s)
}

func (a *API) siteLogClear(w http.ResponseWriter, r *http.Request) {
	s := a.site(w, r)
	if s == nil {
		return
	}
	if err := a.c.Procs.Logs(s.ID).Clear(); err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "site.logs.clear", s.Name, "")
	w.WriteHeader(http.StatusNoContent)
}

// ---- deployments

func (a *API) listDeployments(w http.ResponseWriter, r *http.Request) {
	s := a.site(w, r)
	if s == nil {
		return
	}
	list, err := a.c.Store.ListDeployments(r.Context(), s.ID, 100)
	if err != nil {
		a.fail(w, err)
		return
	}
	if list == nil {
		list = []*model.Deployment{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *API) deployZip(w http.ResponseWriter, r *http.Request) {
	s := a.site(w, r)
	if s == nil {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<30)
	file, hdr, err := r.FormFile("file")
	if err != nil {
		a.fail(w, errors.New("upload a .zip file in the 'file' field"))
		return
	}
	defer file.Close()
	if !strings.HasSuffix(strings.ToLower(hdr.Filename), ".zip") {
		a.fail(w, errors.New("only .zip archives are supported"))
		return
	}
	tmp, err := os.CreateTemp(a.c.Paths.Tmp, "upload-*.zip")
	if err != nil {
		a.fail(w, err)
		return
	}
	if _, err := io.Copy(tmp, file); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		a.fail(w, err)
		return
	}
	tmp.Close()
	dep, err := a.c.Deploy.DeployZip(context.Background(), s, tmp.Name(), user(r).Username)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "site.deploy", s.Name, "zip "+hdr.Filename)
	writeJSON(w, http.StatusAccepted, dep)
}

func (a *API) deployGit(w http.ResponseWriter, r *http.Request) {
	s := a.site(w, r)
	if s == nil {
		return
	}
	var in struct {
		Branch string `json:"branch"`
	}
	if r.ContentLength > 0 {
		decode(r, &in)
	}
	dep, err := a.c.Deploy.DeployGit(context.Background(), s, in.Branch, "git", user(r).Username)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "site.deploy", s.Name, "git")
	writeJSON(w, http.StatusAccepted, dep)
}

func (a *API) activateDeployment(w http.ResponseWriter, r *http.Request) {
	s := a.site(w, r)
	if s == nil {
		return
	}
	dep, err := a.c.Deploy.Activate(r.Context(), s, chi.URLParam(r, "dep"))
	if err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "site.rollback", s.Name, dep.ID)
	writeJSON(w, http.StatusOK, dep)
}

func (a *API) deploymentLog(w http.ResponseWriter, r *http.Request) {
	s := a.site(w, r)
	if s == nil {
		return
	}
	data, err := a.c.Deploy.Log(s.ID, filepath.Base(chi.URLParam(r, "dep")))
	if err != nil {
		writeErr(w, http.StatusNotFound, "log not found")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(data)
}

func (a *API) deploymentLogStream(w http.ResponseWriter, r *http.Request) {
	s := a.site(w, r)
	if s == nil {
		return
	}
	depID := filepath.Base(chi.URLParam(r, "dep"))
	// Live output is keyed by deployment alone: make sure it is this
	// site's, or a user of one site could follow another site's deploy.
	if dep, err := a.c.Store.GetDeployment(r.Context(), depID); err != nil || dep.SiteID != s.ID {
		writeErr(w, http.StatusNotFound, "log not found")
		return
	}
	// Send what has been written so far, then follow. The backlog ends
	// exactly where the live lines begin, or a line written while this
	// request starts would be sent twice.
	backlog, lines, done, cancel, running := a.c.Deploy.Subscribe(depID)
	defer cancel()
	if !running {
		backlog, _ = a.c.Deploy.Log(s.ID, depID)
	}
	stream := newSSE(w)
	if len(backlog) > 0 {
		stream.send("log", string(backlog))
	}
	finish := func() {
		if dep, err := a.c.Store.GetDeployment(r.Context(), depID); err == nil {
			stream.send("done", dep)
		}
	}
	if !running {
		finish()
		return
	}
	ctx, stop := a.siteStreamContext(r, s.ID, model.RoleViewer)
	defer stop()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case l := <-lines:
			if stream.send("log", l) != nil {
				return
			}
		case <-done:
		drain: // anything still buffered
			for {
				select {
				case l := <-lines:
					stream.send("log", l)
				default:
					break drain
				}
			}
			finish()
			return
		case <-ping.C:
			if stream.ping() != nil {
				return
			}
		}
	}
}

// webhookDeploy handles push webhooks from GitHub, GitLab, Gitea and others.
func (a *API) webhookDeploy(w http.ResponseWriter, r *http.Request) {
	s, err := a.c.Site(chi.URLParam(r, "id"))
	if err != nil || s.Deploy.WebhookSecret == "" {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 5<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "cannot read body")
		return
	}
	// Fail closed: a secret that cannot be decrypted (for example after a
	// restore onto another machine) must not degrade into an empty HMAC key.
	secret, err := a.c.Box.Unseal(s.Deploy.WebhookSecret)
	if err != nil || secret == "" {
		a.log.Warn("webhook secret unreadable; re-enter it in the site's deployment settings", "site", s.Name)
		writeErr(w, http.StatusServiceUnavailable, "webhook secret is not usable")
		return
	}
	if !verifyWebhook(r, body, secret) {
		a.c.AddAudit(r.Context(), model.AuditEntry{Time: time.Now(), User: "webhook", IP: clientIP(r), Action: "webhook.rejected", Target: s.Name})
		writeErr(w, http.StatusUnauthorized, "invalid signature")
		return
	}
	var payload struct {
		Ref string `json:"ref"`
	}
	json.Unmarshal(body, &payload)
	branch := s.Deploy.Git.Branch
	if payload.Ref != "" && branch != "" && payload.Ref != "refs/heads/"+branch {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": "push to " + payload.Ref})
		return
	}
	dep, err := a.c.Deploy.DeployGit(context.Background(), s, "", "webhook", "webhook")
	if err != nil {
		a.fail(w, err)
		return
	}
	a.c.AddAudit(r.Context(), model.AuditEntry{Time: time.Now(), User: "webhook", IP: clientIP(r), Action: "site.deploy", Target: s.Name, Detail: "webhook"})
	writeJSON(w, http.StatusAccepted, dep)
}

func verifyWebhook(r *http.Request, body []byte, secret string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	for _, h := range []string{"X-Hub-Signature-256", "X-Gitea-Signature", "X-Gogs-Signature"} {
		if v := strings.TrimPrefix(r.Header.Get(h), "sha256="); v != "" {
			return hmac.Equal([]byte(v), []byte(want))
		}
	}
	for _, v := range []string{r.Header.Get("X-Gitlab-Token"), r.URL.Query().Get("secret")} {
		if v != "" {
			return subtle.ConstantTimeCompare([]byte(v), []byte(secret)) == 1
		}
	}
	return false
}
