package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/parthh37/nodehoster/internal/backup"
	"github.com/parthh37/nodehoster/internal/config"
	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/secrets"
	"github.com/parthh37/nodehoster/internal/store"
)

// Scheduled backups: an archive of the configuration, certificates and
// (optionally) the sites' shared folders, copied to one or more
// destinations and pruned there. One backup or restore runs at a time.

const (
	backupHistoryKey = "backup.history"
	backupHistoryMax = 50
)

// ErrBackupBusy is returned while another backup or restore runs.
var ErrBackupBusy = errors.New("a backup or restore is already running")

// ErrShuttingDown is returned for a backup or restore asked for while the
// service stops.
var ErrShuttingDown = errors.New("the service is shutting down")

type backupState struct {
	mu      sync.Mutex
	running bool
	closed  bool // Shutdown started: no new runs
	since   time.Time
	what    string // schedule | manual | restore | download
}

// begin claims the single backup/restore slot. A run that goes on in its
// own goroutine passes the WaitGroup Shutdown waits on: it is added under
// the same lock close takes, so a run is either refused or added before
// Shutdown starts waiting, never during the wait.
func (b *backupState) begin(what string, wg *sync.WaitGroup) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrShuttingDown
	}
	if b.running {
		return ErrBackupBusy
	}
	b.running, b.since, b.what = true, time.Now(), what
	if wg != nil {
		wg.Add(1)
	}
	return nil
}

// close refuses every later begin.
func (b *backupState) close() {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
}

func (b *backupState) end() {
	b.mu.Lock()
	b.running = false
	b.mu.Unlock()
}

// BackupStatus is what the Backups pages show.
func (c *Core) BackupStatus(ctx context.Context) model.BackupStatus {
	cfg := c.Settings().Backup
	host, _ := os.Hostname()
	st := model.BackupStatus{Enabled: cfg.Enabled, Encrypted: cfg.Passphrase != "", Hostname: backup.SafeHost(host), History: c.backupHistory(ctx)}
	c.backups.mu.Lock()
	if c.backups.running {
		since := c.backups.since
		st.Running, st.RunningSince, st.RunningWhat = true, &since, c.backups.what
	}
	c.backups.mu.Unlock()
	if cfg.Enabled && len(cfg.Destinations) > 0 {
		if next, ok := backup.Next(cfg.Time, cfg.Weekdays, time.Now()); ok {
			st.NextRun = &next
		}
	}
	return st
}

func (c *Core) backupHistory(ctx context.Context) []model.BackupRun {
	list := []model.BackupRun{}
	if err := c.Store.GetDoc(ctx, backupHistoryKey, &list); err != nil && !errors.Is(err, store.ErrNotFound) {
		c.Log.Warn("read backup history", "err", err)
	}
	return list
}

func (c *Core) addBackupHistory(run model.BackupRun) {
	ctx := context.Background()
	list := append([]model.BackupRun{run}, c.backupHistory(ctx)...)
	if len(list) > backupHistoryMax {
		list = list[:backupHistoryMax]
	}
	if err := c.Store.PutDoc(ctx, backupHistoryKey, list); err != nil {
		c.Log.Warn("save backup history", "err", err)
	}
}

// backupLoop runs scheduled backups. It checks twice a minute, so a
// schedule change applies without a restart.
func (c *Core) backupLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	s := backup.Scheduler{
		Now:  time.Now,
		Tick: t.C,
		Schedule: func() (bool, string, []int) {
			b := c.Settings().Backup
			return b.Enabled && len(b.Destinations) > 0, b.Time, b.Weekdays
		},
		Run: func(ctx context.Context) {
			if _, err := c.RunBackup(ctx, "schedule"); errors.Is(err, ErrBackupBusy) {
				c.Log.Warn("scheduled backup skipped: " + err.Error())
			}
		},
	}
	s.Loop(ctx)
}

// StartBackup runs a backup in the background, for the API. The run is
// cancelled when the service stops (Shutdown waits for it).
func (c *Core) StartBackup() error {
	if err := c.backups.begin("manual", &c.wg); err != nil {
		return err
	}
	ctx := c.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		defer c.wg.Done()
		defer c.backups.end()
		c.runBackup(ctx, "manual")
	}()
	return nil
}

// RunBackup makes an archive and copies it to every enabled destination.
func (c *Core) RunBackup(ctx context.Context, trigger string) (*model.BackupRun, error) {
	if err := c.backups.begin(trigger, nil); err != nil {
		return nil, err
	}
	defer c.backups.end()
	return c.runBackup(ctx, trigger), nil
}

func (c *Core) runBackup(ctx context.Context, trigger string) *model.BackupRun {
	cfg := c.Settings().Backup
	run := model.BackupRun{ID: uuid.NewString(), Trigger: trigger, StartedAt: time.Now(), Contents: []string{}, Destinations: []model.BackupDestResult{}}
	finish := func() *model.BackupRun {
		run.FinishedAt = time.Now()
		c.addBackupHistory(run)
		if run.Status == model.BackupSuccess {
			c.Bus.Info(events.BackupCompleted, "", "Backup %s (%s) copied to %s", run.File, sizeText(run.Size), destNames(run.Destinations))
		} else {
			msg := run.Error
			if msg == "" {
				var failed []string
				for _, d := range run.Destinations {
					if !d.OK {
						failed = append(failed, d.Name+": "+d.Error)
					}
				}
				msg = strings.Join(failed, "; ")
			}
			c.Bus.Error(events.BackupFailed, "", "Backup %s: %s", run.Status, msg)
		}
		return &run
	}

	var dests []model.BackupDestination
	for _, d := range cfg.Destinations {
		if d.Enabled {
			dests = append(dests, d)
		}
	}
	if len(dests) == 0 {
		run.Status, run.Error = model.BackupFailed, "no backup destination is enabled"
		return finish()
	}
	a, err := c.buildArchive(ctx, cfg)
	if err != nil {
		run.Status, run.Error = model.BackupFailed, "could not build the archive: "+err.Error()
		return finish()
	}
	defer os.Remove(a.path)
	run.File, run.Size, run.Encrypted, run.Contents = a.name, a.size, a.encrypted, a.contents

	ok := 0
	for _, d := range dests {
		res := c.sendBackup(ctx, d, a, cfg)
		if res.OK {
			ok++
		}
		run.Destinations = append(run.Destinations, res)
	}
	switch ok {
	case len(dests):
		run.Status = model.BackupSuccess
	case 0:
		run.Status = model.BackupFailed
	default:
		run.Status = model.BackupPartial
	}
	return finish()
}

func sizeText(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
}

func destNames(list []model.BackupDestResult) string {
	var names []string
	for _, d := range list {
		names = append(names, d.Name)
	}
	return strings.Join(names, ", ")
}

// sendBackup uploads the archive to one destination, then applies the
// retention rules there.
func (c *Core) sendBackup(ctx context.Context, d model.BackupDestination, a *builtArchive, cfg model.BackupSettings) model.BackupDestResult {
	res := model.BackupDestResult{ID: d.ID, Name: d.Name}
	plain, err := c.unsealDestination(d)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Hour)
	defer cancel()
	t, err := backup.New(ctx, plain)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer t.Close()
	if err := t.Put(ctx, a.name, a.path); err != nil {
		res.Error = err.Error()
		return res
	}
	res.OK = true
	list, err := t.List(ctx)
	if err != nil {
		res.Error = "uploaded, but old archives could not be listed for retention: " + err.Error()
		return res
	}
	host, _ := os.Hostname()
	for _, o := range backup.Expired(list, host, cfg.KeepLast, cfg.KeepDays, time.Now()) {
		if o.Name == a.name {
			continue
		}
		if err := t.Delete(ctx, o.Name); err != nil {
			res.Error = "uploaded, but " + o.Name + " could not be deleted for retention: " + err.Error()
			continue
		}
		res.Pruned++
	}
	return res
}

type builtArchive struct {
	path, name string
	size       int64
	encrypted  bool
	contents   []string
}

// buildArchive writes an archive to the temporary folder.
func (c *Core) buildArchive(ctx context.Context, cfg model.BackupSettings) (*builtArchive, error) {
	pass, err := c.Box.Unseal(cfg.Passphrase)
	if err != nil {
		return nil, fmt.Errorf("the backup passphrase cannot be decrypted; enter it again: %w", err)
	}
	host, _ := os.Hostname()
	now := time.Now()
	out := &builtArchive{name: backup.ArchiveName(host, now), encrypted: pass != ""}

	inner, err := os.CreateTemp(c.Paths.Tmp, "backup-*.zip")
	if err != nil {
		return nil, err
	}
	innerPath := inner.Name()
	ok := false
	defer func() {
		if !ok || pass != "" {
			os.Remove(innerPath)
		}
	}()
	m := backup.Manifest{Version: config.Version, Hostname: host, Created: now.UTC()}
	w := backup.NewWriter(ctx, inner, m)
	err = c.writeArchive(ctx, w, cfg, pass, &out.contents)
	if err == nil {
		err = w.Close()
	}
	if cerr := inner.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	out.path = innerPath
	if pass != "" {
		outer, err := os.CreateTemp(c.Paths.Tmp, "backup-*.zip")
		if err != nil {
			return nil, err
		}
		err = backup.Encrypt(ctx, outer, innerPath, m, pass)
		if cerr := outer.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(outer.Name())
			return nil, err
		}
		out.path = outer.Name()
	}
	out.size, _ = fileSizeOf(out.path)
	ok = true
	return out, nil
}

func fileSizeOf(p string) (int64, error) {
	st, err := os.Stat(p)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

func (c *Core) writeArchive(ctx context.Context, w *backup.Writer, cfg model.BackupSettings, pass string, contents *[]string) error {
	data, err := c.Backup(ctx)
	if err != nil {
		return err
	}
	if err := w.AddBytes(backup.ConfigFile, data); err != nil {
		return err
	}
	w.Contents().Configuration = true
	*contents = append(*contents, "configuration")
	if pass != "" {
		// The whole archive is encrypted with the passphrase, so the
		// secrets can travel in clear inside it: that is what lets a
		// replacement server, with another master key, restore them.
		sec, err := c.portableSecrets(data)
		if err != nil {
			return err
		}
		if err := w.AddBytes(backup.SecretsFile, sec); err != nil {
			return err
		}
		w.Contents().PortableSecrets = true
	}
	if cfg.IncludeCertificates {
		list, err := c.Store.ListCertificates(ctx)
		if err != nil {
			return err
		}
		n := 0
		for _, cert := range list {
			dir := filepath.Join(c.Paths.Certs, cert.ID)
			certPEM, err1 := os.ReadFile(filepath.Join(dir, "cert.pem"))
			keyPEM, err2 := os.ReadFile(filepath.Join(dir, "key.pem"))
			if err1 != nil || err2 != nil {
				continue // not issued yet
			}
			prefix := "certs/" + cert.ID + "/"
			if err := w.AddBytes(prefix+"cert.pem", certPEM); err != nil {
				return err
			}
			if pass != "" {
				err = w.AddBytes(prefix+"key.pem", keyPEM)
			} else {
				// Without a passphrase nothing in the archive may be a
				// usable secret: the key is sealed with this server's
				// master key, like every other secret.
				var sealed string
				if sealed, err = c.Box.Seal(string(keyPEM)); err == nil {
					err = w.AddBytes(prefix+"key.pem.sealed", []byte(sealed))
				}
			}
			if err != nil {
				return err
			}
			w.Contents().Certificates = append(w.Contents().Certificates, cert.ID)
			n++
		}
		*contents = append(*contents, fmt.Sprintf("certificates (%d)", n))
	}
	if cfg.IncludeShared {
		for _, s := range c.Sites() {
			if len(cfg.SharedSiteIDs) > 0 && !slices.Contains(cfg.SharedSiteIDs, s.ID) {
				continue
			}
			dir := model.SharedDir(c.Paths.Sites, s.ID)
			if st, err := os.Stat(dir); err != nil || !st.IsDir() {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, err := w.AddDir("sites/"+s.ID+"/shared", dir); err != nil {
				return fmt.Errorf("shared folder of %s: %w", s.Name, err)
			}
			w.Contents().SharedSites = append(w.Contents().SharedSites, s.ID)
			*contents = append(*contents, "shared: "+s.Name)
		}
	}
	return nil
}

// portableSecrets maps every sealed value in a configuration export to its
// plain text.
func (c *Core) portableSecrets(data []byte) ([]byte, error) {
	var doc any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	out := map[string]string{}
	walkStrings(doc, func(s string) string {
		if secrets.IsSealed(s) {
			if plain, err := c.Box.Unseal(s); err == nil {
				out[s] = plain
			}
		}
		return s
	})
	return json.Marshal(out)
}

// walkStrings calls fn for every string in a decoded JSON document,
// replacing it with the result.
func walkStrings(v any, fn func(string) string) any {
	switch t := v.(type) {
	case string:
		return fn(t)
	case map[string]any:
		for k, x := range t {
			t[k] = walkStrings(x, fn)
		}
	case []any:
		for i, x := range t {
			t[i] = walkStrings(x, fn)
		}
	}
	return v
}

// BackupArchive builds an archive for download with the configured
// contents and passphrase. The caller removes the file.
func (c *Core) BackupArchive(ctx context.Context) (path, name string, err error) {
	if err := c.backups.begin("download", nil); err != nil {
		return "", "", err
	}
	defer c.backups.end()
	a, err := c.buildArchive(ctx, c.Settings().Backup)
	if err != nil {
		return "", "", err
	}
	return a.path, a.name, nil
}

// ---- destinations

// destSecrets are the secret fields of a destination, in a fixed order.
func destSecrets(d *model.BackupDestination) []*string {
	switch {
	case d.Type == model.BackupS3 && d.S3 != nil:
		return []*string{&d.S3.SecretAccessKey}
	case d.Type == model.BackupAzure && d.Azure != nil:
		return []*string{&d.Azure.SASToken, &d.Azure.AccountKey}
	case d.Type == model.BackupSFTP && d.SFTP != nil:
		return []*string{&d.SFTP.Password, &d.SFTP.PrivateKey, &d.SFTP.Passphrase}
	}
	return nil
}

// cloneDest copies a destination deeply (its sections are pointers).
func cloneDest(d model.BackupDestination) model.BackupDestination {
	if d.Folder != nil {
		v := *d.Folder
		d.Folder = &v
	}
	if d.S3 != nil {
		v := *d.S3
		d.S3 = &v
	}
	if d.Azure != nil {
		v := *d.Azure
		d.Azure = &v
	}
	if d.SFTP != nil {
		v := *d.SFTP
		d.SFTP = &v
	}
	return d
}

// mergeDestSecrets replaces masked secrets of d with the stored (sealed)
// ones of the destination with the same ID and type.
func mergeDestSecrets(d *model.BackupDestination, stored []model.BackupDestination) {
	var old *model.BackupDestination
	for i := range stored {
		if stored[i].ID == d.ID && d.ID != "" && stored[i].Type == d.Type {
			o := cloneDest(stored[i])
			old = &o
		}
	}
	var olds []*string
	if old != nil {
		olds = destSecrets(old)
	}
	for i, p := range destSecrets(d) {
		if *p != secrets.Mask {
			continue
		}
		*p = ""
		if i < len(olds) {
			*p = *olds[i]
		}
	}
}

// prepareBackup validates backup settings and seals their secrets,
// keeping masked ones from cur.
func (c *Core) prepareBackup(in *model.BackupSettings, cur model.BackupSettings) error {
	if in.Time == "" {
		in.Time = model.DefaultBackup().Time
	}
	if in.Weekdays == nil {
		in.Weekdays = []int{}
	}
	if in.SharedSiteIDs == nil {
		in.SharedSiteIDs = []string{}
	}
	if in.Destinations == nil {
		in.Destinations = []model.BackupDestination{}
	}
	if in.Passphrase == secrets.Mask {
		in.Passphrase = cur.Passphrase
	}
	for i := range in.Destinations {
		d := &in.Destinations[i]
		*d = cloneDest(*d)
		if d.ID == "" {
			d.ID = uuid.NewString()
		}
		mergeDestSecrets(d, cur.Destinations)
	}
	if err := in.Validate(); err != nil {
		return err
	}
	seal := func(p *string) error {
		v, err := c.Box.Seal(*p)
		*p = v
		return err
	}
	if err := seal(&in.Passphrase); err != nil {
		return err
	}
	for i := range in.Destinations {
		for _, p := range destSecrets(&in.Destinations[i]) {
			if err := seal(p); err != nil {
				return err
			}
		}
	}
	return nil
}

// maskBackup hides the secrets of backup settings for the API.
func maskBackup(b *model.BackupSettings) {
	mask := func(p *string) {
		if *p != "" {
			*p = secrets.Mask
		}
	}
	mask(&b.Passphrase)
	dests := make([]model.BackupDestination, len(b.Destinations))
	for i, d := range b.Destinations {
		d = cloneDest(d)
		for _, p := range destSecrets(&d) {
			mask(p)
		}
		dests[i] = d
	}
	b.Destinations = dests
	if b.Weekdays == nil {
		b.Weekdays = []int{}
	}
	if b.SharedSiteIDs == nil {
		b.SharedSiteIDs = []string{}
	}
}

// unsealDestination returns a copy of d with its secrets in plain text.
func (c *Core) unsealDestination(d model.BackupDestination) (model.BackupDestination, error) {
	d = cloneDest(d)
	for _, p := range destSecrets(&d) {
		v, err := c.Box.Unseal(*p)
		if err != nil {
			return d, fmt.Errorf("a secret of destination %q cannot be decrypted; enter it again", d.Name)
		}
		*p = v
	}
	return d, nil
}

func (c *Core) destination(id string) (model.BackupDestination, error) {
	for _, d := range c.Settings().Backup.Destinations {
		if d.ID == id {
			return d, nil
		}
	}
	return model.BackupDestination{}, store.ErrNotFound
}

// BackupTestResult is the outcome of testing a destination. HostKey is the
// SFTP server's fingerprint, for the administrator to confirm.
type BackupTestResult struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	HostKey string `json:"hostKey,omitempty"`
}

// TestBackupDestination connects to a destination as edited (masked
// secrets are taken from the saved destination with the same ID), lists
// it, and writes and deletes a small file. An SFTP destination without a
// host key yet is not written to: the result carries the server's key.
func (c *Core) TestBackupDestination(ctx context.Context, in model.BackupDestination) (BackupTestResult, error) {
	d := cloneDest(in)
	mergeDestSecrets(&d, c.Settings().Backup.Destinations)
	plain, err := c.unsealDestination(d)
	if err != nil {
		return BackupTestResult{}, err
	}
	if plain.Name == "" {
		plain.Name = "test"
	}
	if err := plain.Validate("destination"); err != nil {
		var ve *model.ValidationError
		if !(errors.As(err, &ve) && strings.HasSuffix(ve.Field, ".sftp.hostKey") && plain.SFTP != nil && plain.SFTP.HostKey == "") {
			return BackupTestResult{}, err
		}
	}
	t, err := backup.New(ctx, plain)
	if err != nil {
		var hk *backup.HostKeyError
		if errors.As(err, &hk) {
			return BackupTestResult{Error: err.Error(), HostKey: hk.Presented}, nil
		}
		return BackupTestResult{Error: err.Error()}, nil
	}
	defer t.Close()
	res := BackupTestResult{OK: true}
	if plain.SFTP != nil {
		res.HostKey = plain.SFTP.HostKey
	}
	if err := backup.Probe(ctx, t, c.Paths.Tmp); err != nil {
		res.OK, res.Error = false, err.Error()
	}
	return res, nil
}

// BackupFiles lists the archives at a saved destination, from every
// server, newest first.
func (c *Core) BackupFiles(ctx context.Context, destID string) ([]backup.Object, error) {
	d, err := c.destination(destID)
	if err != nil {
		return nil, err
	}
	plain, err := c.unsealDestination(d)
	if err != nil {
		return nil, err
	}
	t, err := backup.New(ctx, plain)
	if err != nil {
		return nil, err
	}
	defer t.Close()
	list, err := t.List(ctx)
	if err != nil {
		return nil, err
	}
	return backup.Archives(list), nil
}

// SharedSize is the size of a site's shared folder.
type SharedSize struct {
	SiteID   string `json:"siteId"`
	SiteName string `json:"siteName"`
	Bytes    int64  `json:"bytes"`
	Files    int    `json:"files"`
	Partial  bool   `json:"partial"` // counting took too long and stopped
}

// SharedSizes measures each site's shared folder, for the size warning.
// The walk is bounded: a huge folder reports what it counted in time.
func (c *Core) SharedSizes(ctx context.Context) []SharedSize {
	deadline := time.Now().Add(5 * time.Second)
	out := []SharedSize{}
	for _, s := range c.Sites() {
		dir := model.SharedDir(c.Paths.Sites, s.ID)
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		sz := SharedSize{SiteID: s.ID, SiteName: s.Name}
		filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if time.Now().After(deadline) || ctx.Err() != nil {
				sz.Partial = true
				return fs.SkipAll
			}
			if d.Type()&fs.ModeSymlink != 0 && d.IsDir() {
				return fs.SkipDir
			}
			if d.Type().IsRegular() {
				if info, err := d.Info(); err == nil {
					sz.Bytes += info.Size()
					sz.Files++
				}
			}
			return nil
		})
		out = append(out, sz)
	}
	return out
}

// ---- restore

// RestoreFromDestination downloads an archive from a saved destination and
// restores it.
func (c *Core) RestoreFromDestination(ctx context.Context, destID, name, passphrase string) (*model.RestoreResult, error) {
	if _, _, ok := backup.ParseName(name); !ok {
		return nil, &model.ValidationError{Field: "file", Message: "not a backup archive name"}
	}
	d, err := c.destination(destID)
	if err != nil {
		return nil, err
	}
	plain, err := c.unsealDestination(d)
	if err != nil {
		return nil, err
	}
	t, err := backup.New(ctx, plain)
	if err != nil {
		return nil, err
	}
	defer t.Close()
	rc, err := t.Open(ctx, name)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	f, err := os.CreateTemp(c.Paths.Tmp, "restore-*.zip")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	_, err = io.Copy(f, rc)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", name, err)
	}
	return c.RestoreFile(ctx, f.Name(), passphrase)
}

// RestoreFile restores a backup: a configuration export (.json) or an
// archive (.zip), encrypted or not.
//
// What is restored, and how:
//   - Settings are replaced; sites in the backup overwrite those with the
//     same ID; other sites are kept (as with the JSON restore).
//   - Secrets sealed with another server's master key are re-sealed with
//     this one's from the archive's portable copy (passphrase-protected
//     archives); without that copy they are cleared, to be re-entered.
//   - Certificate files are written for certificates that are missing
//     here or have no files; a certificate this server already has is
//     kept, since it may have been renewed since the backup.
//   - A site's shared folder is swapped whole: the archive's copy is
//     unpacked next to it first, the site is stopped if it runs, the
//     current folder is kept as shared.pre-restore (replacing an older
//     one), and the site is started again.
func (c *Core) RestoreFile(ctx context.Context, path, passphrase string) (*model.RestoreResult, error) {
	if err := c.backups.begin("restore", nil); err != nil {
		return nil, err
	}
	defer c.backups.end()
	head := make([]byte, 512)
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	n, _ := io.ReadFull(f, head)
	f.Close()
	head = bytes.TrimLeft(head[:n], " \t\r\n\ufeff")
	switch {
	case bytes.HasPrefix(head, []byte("{")):
		data, err := readLimited(path, 64<<20)
		if err != nil {
			return nil, err
		}
		b, err := c.restoreConfig(ctx, data, nil)
		if err != nil {
			return nil, err
		}
		return &model.RestoreResult{Format: "json", Hostname: b.Hostname, Sites: len(b.Sites), SharedSites: []string{}, Warnings: []string{}}, nil
	case bytes.HasPrefix(head, []byte("PK")):
		return c.restoreArchive(ctx, path, passphrase)
	}
	return nil, errors.New("not a NodeHoster backup: upload a nodehoster-backup-….zip or .json file")
}

func readLimited(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err == nil && int64(len(data)) > max {
		err = errors.New("the backup file is too large")
	}
	return data, err
}

func (c *Core) restoreArchive(ctx context.Context, path, passphrase string) (*model.RestoreResult, error) {
	m, err := backup.Inspect(path)
	if err != nil {
		return nil, err
	}
	res := &model.RestoreResult{Format: "archive", Hostname: m.Hostname, Encrypted: m.Encrypted, SharedSites: []string{}, Warnings: []string{}}
	created := m.Created
	res.Created = &created
	inner := path
	if m.Encrypted {
		if passphrase == "" {
			return nil, &model.ValidationError{Field: "passphrase", Message: "this backup is encrypted: enter its passphrase"}
		}
		f, err := os.CreateTemp(c.Paths.Tmp, "restore-*.zip")
		if err != nil {
			return nil, err
		}
		defer os.Remove(f.Name())
		err = backup.Decrypt(ctx, path, passphrase, f)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if errors.Is(err, backup.ErrWrongPassphrase) {
			return nil, &model.ValidationError{Field: "passphrase", Message: err.Error()}
		}
		if err != nil {
			return nil, err
		}
		inner = f.Name()
	}
	a, err := backup.Open(inner)
	if err != nil {
		return nil, err
	}
	defer a.Close()
	data, err := a.ReadFile(backup.ConfigFile)
	if err != nil {
		return nil, fmt.Errorf("the archive has no configuration: %w", err)
	}
	var portable map[string]string
	if a.Has(backup.SecretsFile) {
		raw, err := a.ReadFile(backup.SecretsFile)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &portable); err != nil {
			return nil, fmt.Errorf("secrets.json: %w", err)
		}
	}
	data, lost, err := c.resealSecrets(data, portable)
	if err != nil {
		return nil, err
	}
	if lost > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%d secret(s) were encrypted with another server's key and the backup has no passphrase-protected copy: they were cleared. Enter them again (secret environment variables, DNS provider credentials, passwords, tokens).", lost))
	}

	// Certificate files go in before the configuration, so the records
	// that reference them are restored as issued.
	withFiles := map[string]bool{}
	for _, id := range a.Manifest.Contents.Certificates {
		if c.Certs.Get(id) != nil {
			continue // this server has it, possibly renewed since
		}
		ok, warn := c.restoreCertFiles(a, id)
		if warn != "" {
			res.Warnings = append(res.Warnings, warn)
		}
		if ok {
			withFiles[id] = true
			res.Certificates++
		}
	}
	b, err := c.restoreConfig(ctx, data, withFiles)
	if err != nil {
		return nil, err
	}
	res.Sites = len(b.Sites)
	if len(withFiles) > 0 {
		if err := c.Certs.Load(ctx); err != nil {
			res.Warnings = append(res.Warnings, "reload certificates: "+err.Error())
		}
		c.reload()
	}
	for _, id := range a.Manifest.Contents.SharedSites {
		site, err := c.Site(id)
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("the shared folder of site %s was not restored: the site does not exist", id))
			continue
		}
		if err := c.restoreShared(a, site); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("the shared folder of %s was not restored: %v", site.Name, err))
			continue
		}
		res.SharedSites = append(res.SharedSites, site.Name)
	}
	return res, nil
}

// resealSecrets makes every secret in a configuration export readable by
// this server: values sealed with this master key are kept, others are
// re-sealed from their portable copy or, lacking one, cleared.
func (c *Core) resealSecrets(data []byte, portable map[string]string) ([]byte, int, error) {
	var doc any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, 0, fmt.Errorf("not a NodeHoster backup: %w", err)
	}
	lost := 0
	var sealErr error
	doc = walkStrings(doc, func(s string) string {
		if !secrets.IsSealed(s) {
			return s
		}
		if _, err := c.Box.Unseal(s); err == nil {
			return s
		}
		if plain, ok := portable[s]; ok {
			v, err := c.Box.Seal(plain)
			if err != nil {
				sealErr = err
			}
			return v
		}
		lost++
		return ""
	})
	if sealErr != nil {
		return nil, 0, sealErr
	}
	out, err := json.Marshal(doc)
	return out, lost, err
}

// restoreCertFiles writes a certificate's PEM files from the archive.
func (c *Core) restoreCertFiles(a *backup.Archive, id string) (bool, string) {
	prefix := "certs/" + id + "/"
	certPEM, err := a.ReadFile(prefix + "cert.pem")
	if err != nil {
		return false, "certificate " + id + ": " + err.Error()
	}
	var keyPEM []byte
	switch {
	case a.Has(prefix + "key.pem"):
		if keyPEM, err = a.ReadFile(prefix + "key.pem"); err != nil {
			return false, "certificate " + id + ": " + err.Error()
		}
	case a.Has(prefix + "key.pem.sealed"):
		raw, err := a.ReadFile(prefix + "key.pem.sealed")
		if err != nil {
			return false, "certificate " + id + ": " + err.Error()
		}
		plain, err := c.Box.Unseal(string(raw))
		if err != nil {
			return false, "certificate " + id + ": its private key was encrypted with another server's key; import the certificate again, or restore a passphrase-protected backup"
		}
		keyPEM = []byte(plain)
	default:
		return false, "certificate " + id + ": the archive has no private key"
	}
	dir := filepath.Join(c.Paths.Certs, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err.Error()
	}
	if err := config.WriteFileAtomic(filepath.Join(dir, "key.pem"), keyPEM, 0o600); err != nil {
		return false, err.Error()
	}
	if err := config.WriteFileAtomic(filepath.Join(dir, "cert.pem"), certPEM, 0o644); err != nil {
		return false, err.Error()
	}
	return true, ""
}

// restoreShared swaps a site's shared folder for the archive's copy (see
// RestoreFile).
func (c *Core) restoreShared(a *backup.Archive, site *model.Site) error {
	shared := model.SharedDir(c.Paths.Sites, site.ID)
	tmp := shared + ".restoring"
	os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o750); err != nil {
		return err
	}
	if _, err := a.Extract("sites/"+site.ID+"/shared", tmp); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	// The site's processes hold files open (and Windows refuses to move
	// a folder with open files), so it is stopped for the swap.
	wasRunning := c.IsRunning(site)
	if wasRunning {
		if err := c.StopSite(site.ID); err != nil {
			os.RemoveAll(tmp)
			return fmt.Errorf("stop the site: %w", err)
		}
	}
	prev := shared + ".pre-restore"
	err := os.RemoveAll(prev)
	if err == nil {
		if err = os.Rename(shared, prev); errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
	}
	if err == nil {
		if err = os.Rename(tmp, shared); err != nil {
			os.Rename(prev, shared) // put the current folder back
		}
	}
	if err != nil {
		os.RemoveAll(tmp)
	}
	if wasRunning {
		if serr := c.StartSite(site.ID); serr != nil && err == nil {
			err = fmt.Errorf("restored, but the site did not start again: %w", serr)
		}
	}
	return err
}
