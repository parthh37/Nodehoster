package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/backup"
	"github.com/parthh37/nodehoster/internal/core"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/secrets"
)

func init() { backup.ScryptLogN = 10 } // fast key derivation for tests

// backupSettings stores backup settings through the API.
func (e *env) backupSettings(admin []opt, fn func(b map[string]any)) map[string]any {
	e.t.Helper()
	rec := e.do(http.MethodGet, "/api/settings", nil, admin...)
	expect(e.t, rec, http.StatusOK)
	s := decodeJSON[map[string]any](e.t, rec)
	b := s["backup"].(map[string]any)
	fn(b)
	rec = e.do(http.MethodPut, "/api/settings", s, admin...)
	expect(e.t, rec, http.StatusOK)
	return decodeJSON[map[string]any](e.t, rec)["backup"].(map[string]any)
}

func folderDest(dir string) map[string]any {
	return map[string]any{"name": "NAS", "type": "folder", "enabled": true, "folder": map[string]any{"path": dir}}
}

// runBackup starts a backup through the API and waits for it.
func (e *env) runBackup(admin []opt) model.BackupRun {
	e.t.Helper()
	expect(e.t, e.do(http.MethodPost, "/api/backups/run", nil, admin...), http.StatusAccepted)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		st := decodeJSON[model.BackupStatus](e.t, e.do(http.MethodGet, "/api/backups", nil, admin...))
		if !st.Running && len(st.History) > 0 {
			return st.History[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatal("the backup did not finish")
	return model.BackupRun{}
}

func (e *env) selfSigned(admin []opt, name string) string {
	e.t.Helper()
	rec := e.do(http.MethodPost, "/api/certificates/selfsigned", map[string]any{"name": name, "domains": []string{name + ".test"}, "validDays": 30}, admin...)
	expect(e.t, rec, http.StatusCreated)
	return decodeJSON[map[string]any](e.t, rec)["id"].(string)
}

func zipEntries(t *testing.T, path string) map[string][]byte {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		out[f.Name], _ = io.ReadAll(rc)
		rc.Close()
	}
	return out
}

func onlyArchive(t *testing.T, dir string) string {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	var found []string
	for _, e := range entries {
		if _, _, ok := backup.ParseName(e.Name()); ok {
			found = append(found, filepath.Join(dir, e.Name()))
		}
	}
	if len(found) != 1 {
		t.Fatalf("archives in %s: %v", dir, found)
	}
	return found[0]
}

func TestBackupSettingsSecretsAreSealedAndMasked(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	got := e.backupSettings(admin, func(b map[string]any) {
		b["passphrase"] = "very secret phrase"
		b["destinations"] = []any{
			map[string]any{"name": "R2", "type": "s3", "enabled": true, "s3": map[string]any{
				"endpoint": "https://acc.r2.cloudflarestorage.com", "region": "auto", "bucket": "nh", "accessKeyId": "AKID", "secretAccessKey": "s3-secret-value"}},
			map[string]any{"name": "Box", "type": "sftp", "enabled": true, "sftp": map[string]any{
				"host": "backup.example", "username": "nh", "password": "sftp-password", "hostKey": "SHA256:" + strings.Repeat("a", 43)}},
		}
	})
	raw, _ := json.Marshal(got)
	for _, secret := range []string{"very secret phrase", "s3-secret-value", "sftp-password"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("settings response contains %q", secret)
		}
	}
	if got["passphrase"] != secrets.Mask {
		t.Errorf("passphrase = %v", got["passphrase"])
	}
	stored := e.c.Settings().Backup
	s3 := stored.Destinations[0].S3
	if !secrets.IsSealed(s3.SecretAccessKey) || e.c.Box.MustUnseal(s3.SecretAccessKey) != "s3-secret-value" {
		t.Errorf("S3 secret stored as %q", s3.SecretAccessKey)
	}
	if stored.Destinations[1].SFTP.Port != 22 || stored.Destinations[0].ID == "" {
		t.Errorf("defaults not applied: %+v", stored.Destinations)
	}
	// Saving the masked settings back keeps every secret.
	e.backupSettings(admin, func(b map[string]any) {})
	stored = e.c.Settings().Backup
	if e.c.Box.MustUnseal(stored.Passphrase) != "very secret phrase" || e.c.Box.MustUnseal(stored.Destinations[1].SFTP.Password) != "sftp-password" {
		t.Error("masked secrets were not kept")
	}

	// Validation: an SFTP destination needs the host key.
	rec := e.do(http.MethodGet, "/api/settings", nil, admin...)
	s := decodeJSON[map[string]any](t, rec)
	b := s["backup"].(map[string]any)
	b["destinations"].([]any)[1].(map[string]any)["sftp"].(map[string]any)["hostKey"] = ""
	rec = e.do(http.MethodPut, "/api/settings", s, admin...)
	expect(t, rec, http.StatusUnprocessableEntity)
	if f := decodeJSON[map[string]string](t, rec)["field"]; f != "backup.destinations[1].sftp.hostKey" {
		t.Errorf("field = %q", f)
	}
}

func TestBackupRunToFolderRetentionAndRestore(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	site := e.createSite(admin, redirectSite("shop", 0))
	certID := e.selfSigned(admin, "shop")
	shared := model.SharedDir(e.c.Paths.Sites, site.ID)
	os.MkdirAll(filepath.Join(shared, "uploads"), 0o750)
	os.WriteFile(filepath.Join(shared, "uploads", "a.txt"), []byte("original"), 0o640)

	dest := t.TempDir()
	host, _ := os.Hostname()
	// Older archives of this server, another server's, and a stranger.
	old := []string{
		backup.ArchiveName(host, time.Now().AddDate(0, 0, -3)),
		backup.ArchiveName(host, time.Now().AddDate(0, 0, -2)),
		backup.ArchiveName(host, time.Now().AddDate(0, 0, -1)),
		backup.ArchiveName("other-server", time.Now().AddDate(0, 0, -30)),
		"keep-me.txt",
	}
	for _, n := range old {
		os.WriteFile(filepath.Join(dest, n), []byte("old"), 0o640)
	}
	e.backupSettings(admin, func(b map[string]any) {
		b["includeShared"] = true
		b["keepLast"] = 2
		b["keepDays"] = 0
		b["destinations"] = []any{folderDest(dest)}
	})

	run := e.runBackup(admin)
	if run.Status != model.BackupSuccess || len(run.Destinations) != 1 || run.Destinations[0].Pruned != 2 || run.Encrypted {
		t.Fatalf("run = %+v", run)
	}
	for i, n := range old {
		_, err := os.Stat(filepath.Join(dest, n))
		if deleted := i < 2; deleted != os.IsNotExist(err) {
			t.Errorf("%s: deleted = %v, want %v", n, os.IsNotExist(err), deleted)
		}
	}
	archive := filepath.Join(dest, run.File)
	files := zipEntries(t, archive)
	for _, name := range []string{"manifest.json", "backup.json", "certs/" + certID + "/cert.pem", "certs/" + certID + "/key.pem.sealed", "sites/" + site.ID + "/shared/uploads/a.txt"} {
		if _, ok := files[name]; !ok {
			t.Errorf("archive lacks %s (has %v)", name, keys(files))
		}
	}
	if _, ok := files["certs/"+certID+"/key.pem"]; ok {
		t.Error("an unencrypted archive holds a plain private key")
	}
	for name, body := range files {
		if bytes.Contains(body, []byte("PRIVATE KEY")) {
			t.Errorf("%s contains a plain private key", name)
		}
	}
	evs, _ := e.c.Store.ListEvents(context.Background(), "", 50)
	found := false
	for _, ev := range evs {
		found = found || ev.Type == "backup.completed"
	}
	if !found {
		t.Error("no backup.completed event")
	}

	// The files at the destination, and a restore from there.
	rec := e.do(http.MethodGet, "/api/backups/destinations/"+e.c.Settings().Backup.Destinations[0].ID+"/files", nil, admin...)
	expect(t, rec, http.StatusOK)
	list := decodeJSON[[]backup.Object](t, rec)
	if len(list) != 3 || list[0].Name != run.File || list[2].Host != "other-server" {
		t.Errorf("files = %+v", list)
	}

	os.WriteFile(filepath.Join(shared, "uploads", "a.txt"), []byte("changed"), 0o640)
	os.WriteFile(filepath.Join(shared, "new.txt"), []byte("new"), 0o640)
	rec = e.do(http.MethodPost, "/api/backups/destinations/"+e.c.Settings().Backup.Destinations[0].ID+"/restore", map[string]string{"file": run.File}, admin...)
	expect(t, rec, http.StatusOK)
	res := decodeJSON[model.RestoreResult](t, rec)
	if res.Format != "archive" || len(res.SharedSites) != 1 || res.Certificates != 0 {
		t.Errorf("restore = %+v", res)
	}
	if b, _ := os.ReadFile(filepath.Join(shared, "uploads", "a.txt")); string(b) != "original" {
		t.Errorf("shared file = %q", b)
	}
	if _, err := os.Stat(filepath.Join(shared, "new.txt")); !os.IsNotExist(err) {
		t.Error("the shared folder was merged, not replaced")
	}
	if b, _ := os.ReadFile(filepath.Join(shared+".pre-restore", "new.txt")); string(b) != "new" {
		t.Error("the previous shared folder was not kept")
	}

	// Only archive names can be restored (no paths through the folder).
	rec = e.do(http.MethodPost, "/api/backups/destinations/"+e.c.Settings().Backup.Destinations[0].ID+"/restore", map[string]string{"file": "../../etc/passwd"}, admin...)
	expect(t, rec, http.StatusUnprocessableEntity)

	// The archive also restores through an upload.
	data, _ := os.ReadFile(archive)
	rec = e.upload("/api/restore", "file", run.File, data, admin...)
	expect(t, rec, http.StatusOK)
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Two servers with different master keys: an archive made with a
// passphrase restores every secret on the other one; without, it cannot.
func TestBackupRestoresOnAnotherMachine(t *testing.T) {
	t.Parallel()
	a, b := newEnv(t), newEnv(t)
	adminA, adminB := a.adminSession(), b.adminSession()
	body := redirectSite("portable", 0)
	body["deploy"] = map[string]any{"webhookSecret": "hook-secret-1", "git": map[string]any{"repo": "https://x.invalid/r.git", "token": "git-token-1"}}
	site := a.createSite(adminA, body)
	certID := a.selfSigned(adminA, "portable")
	dest := t.TempDir()
	a.backupSettings(adminA, func(s map[string]any) {
		s["passphrase"] = "correct horse battery staple"
		s["destinations"] = []any{folderDest(dest)}
	})
	run := a.runBackup(adminA)
	if run.Status != model.BackupSuccess || !run.Encrypted {
		t.Fatalf("run = %+v", run)
	}
	archive := onlyArchive(t, dest)
	data, _ := os.ReadFile(archive)
	for _, secret := range []string{"git-token-1", "hook-secret-1", "PRIVATE KEY", "correct horse"} {
		if bytes.Contains(data, []byte(secret)) {
			t.Errorf("the encrypted archive contains %q", secret)
		}
	}
	if files := zipEntries(t, archive); len(files) != 2 || files["payload.enc"] == nil {
		t.Errorf("encrypted archive entries = %v", keys(files))
	}

	upload := func(pass string) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("file", filepath.Base(archive))
		fw.Write(data)
		if pass != "" {
			mw.WriteField("passphrase", pass)
		}
		mw.Close()
		return b.do(http.MethodPost, "/api/restore", buf.Bytes(), append(append([]opt{}, adminB...), withHeader("Content-Type", mw.FormDataContentType()))...)
	}
	rec := upload("")
	expect(t, rec, http.StatusUnprocessableEntity)
	if f := decodeJSON[map[string]string](t, rec)["field"]; f != "passphrase" {
		t.Errorf("no passphrase: field = %q", f)
	}
	expect(t, upload("wrong horse"), http.StatusUnprocessableEntity)
	if len(b.c.Sites()) != 0 {
		t.Fatal("a failed restore changed the configuration")
	}
	rec = upload("correct horse battery staple")
	expect(t, rec, http.StatusOK)
	res := decodeJSON[model.RestoreResult](t, rec)
	if !res.Encrypted || res.Certificates != 1 || len(res.Warnings) != 0 {
		t.Errorf("restore = %+v", res)
	}
	restored, err := b.c.Site(site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := b.c.Box.MustUnseal(restored.Deploy.Git.Token); got != "git-token-1" {
		t.Errorf("git token on the new server = %q", got)
	}
	if got := b.c.Box.MustUnseal(restored.Deploy.WebhookSecret); got != "hook-secret-1" {
		t.Errorf("webhook secret on the new server = %q", got)
	}
	if b.c.Certs.Get(certID) == nil {
		t.Error("the certificate was not restored with its key")
	}
	// The backup settings came along, readable with the new key.
	if got := b.c.Box.MustUnseal(b.c.Settings().Backup.Passphrase); got != "correct horse battery staple" {
		t.Errorf("passphrase on the new server = %q", got)
	}

	// Without a passphrase the archive is bound to server A.
	os.Remove(archive)
	a.backupSettings(adminA, func(s map[string]any) { s["passphrase"] = "" })
	run = a.runBackup(adminA)
	if run.Encrypted {
		t.Fatal("encrypted without a passphrase")
	}
	c := newEnv(t)
	adminC := c.adminSession()
	data, _ = os.ReadFile(onlyArchive(t, dest))
	rec = c.upload("/api/restore", "file", "x.zip", data, adminC...)
	expect(t, rec, http.StatusOK)
	res = decodeJSON[model.RestoreResult](t, rec)
	if len(res.Warnings) < 2 || !strings.Contains(strings.Join(res.Warnings, "\n"), "cleared") || res.Certificates != 0 {
		t.Errorf("restore without passphrase = %+v", res)
	}
	restored, _ = c.c.Site(site.ID)
	if restored == nil || restored.Deploy.Git.Token != "" {
		t.Errorf("an unreadable secret was kept: %+v", restored)
	}
}

func TestBackupRestoreRejectsZipSlip(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("manifest.json")
	w.Write([]byte(`{"format":"nodehoster-backup","formatVersion":1,"files":[{"path":"backup.json","size":2,"sha256":"44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a"},{"path":"../../evil.txt","size":1,"sha256":"2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881"}]}`))
	w, _ = zw.Create("backup.json")
	w.Write([]byte("{}"))
	w, _ = zw.Create("../../evil.txt")
	w.Write([]byte("x"))
	zw.Close()
	rec := e.upload("/api/restore", "file", "evil.zip", buf.Bytes(), admin...)
	expect(t, rec, http.StatusBadRequest)
	if !strings.Contains(rec.Body.String(), "not allowed") {
		t.Errorf("body = %s", rec.Body)
	}
	if _, err := os.Stat(filepath.Join(e.root, "..", "evil.txt")); err == nil {
		t.Error("a file was written outside the data folder")
	}
}

func TestBackupDownloadArchiveAndTest(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	admin := e.adminSession()
	rec := e.do(http.MethodGet, "/api/backup?format=zip", nil, admin...)
	expect(t, rec, http.StatusOK)
	if !bytes.HasPrefix(rec.Body.Bytes(), []byte("PK")) || !strings.Contains(rec.Header().Get("Content-Disposition"), "nodehoster-backup-") {
		t.Errorf("download: %q %q", rec.Header().Get("Content-Disposition"), rec.Body.Bytes()[:min(8, rec.Body.Len())])
	}

	dir := t.TempDir()
	rec = e.do(http.MethodPost, "/api/backups/test", folderDest(dir), admin...)
	expect(t, rec, http.StatusOK)
	if res := decodeJSON[core.BackupTestResult](t, rec); !res.OK {
		t.Errorf("folder test = %+v", res)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("the test left files: %v", entries)
	}
	// A path that is a file cannot be written to.
	file := filepath.Join(dir, "file")
	os.WriteFile(file, nil, 0o640)
	rec = e.do(http.MethodPost, "/api/backups/test", folderDest(file), admin...)
	expect(t, rec, http.StatusOK)
	if res := decodeJSON[core.BackupTestResult](t, rec); res.OK || res.Error == "" {
		t.Errorf("unwritable folder test = %+v", res)
	}
	// Invalid settings are a validation error.
	expect(t, e.do(http.MethodPost, "/api/backups/test", map[string]any{"name": "x", "type": "s3", "s3": map[string]any{}}, admin...), http.StatusUnprocessableEntity)

	// Nothing enabled: a failed run, reported.
	run := e.runBackup(admin)
	if run.Status != model.BackupFailed || !strings.Contains(run.Error, "no backup destination") {
		t.Errorf("run without destinations = %+v", run)
	}
}

func TestBackupEndpointsAreForAdmins(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	op := session(e.login(e.user("op", model.RoleOperator, false).Username))
	for _, ep := range []struct{ method, path string }{
		{"GET", "/api/backups"},
		{"POST", "/api/backups/run"},
		{"POST", "/api/backups/test"},
		{"GET", "/api/backups/shared-sizes"},
		{"GET", "/api/backups/destinations/x/files"},
		{"POST", "/api/backups/destinations/x/restore"},
	} {
		expect(t, e.do(ep.method, ep.path, map[string]any{}, op...), http.StatusForbidden)
	}
}
