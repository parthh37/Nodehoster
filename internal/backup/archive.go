// Package backup builds, encrypts and reads NodeHoster backup archives and
// copies them to backup destinations (a folder or network share, S3,
// Azure Blob Storage, SFTP). What goes into an archive, and how it is
// restored, is decided by the core; this package is the mechanics.
package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// An archive is a zip file named nodehoster-backup-<host>-<yyyyMMdd-HHmmss>.zip
// (UTC) holding:
//
//	manifest.json                    what is inside, with SHA-256 checksums
//	backup.json                      the configuration export (core.Backup)
//	secrets.json                     encrypted archives only: the secrets in
//	                                 backup.json, readable with the passphrase
//	certs/<id>/cert.pem              certificate chain
//	certs/<id>/key.pem               private key (encrypted archives), or
//	certs/<id>/key.pem.sealed        the key sealed with this server's key
//	sites/<id>/shared/...            a site's shared folder
//
// An encrypted archive is a zip of manifest.json (without the contents) and
// payload.enc, the archive above encrypted with the passphrase.
const (
	FormatName    = "nodehoster-backup"
	FormatVersion = 1
	NamePrefix    = "nodehoster-backup-"
	nameTime      = "20060102-150405"

	ManifestFile = "manifest.json"
	ConfigFile   = "backup.json"
	SecretsFile  = "secrets.json"
	PayloadFile  = "payload.enc"
)

// Manifest describes an archive.
type Manifest struct {
	Format        string    `json:"format"`
	FormatVersion int       `json:"formatVersion"`
	Version       string    `json:"version"` // NodeHoster version that wrote it
	Hostname      string    `json:"hostname"`
	Created       time.Time `json:"created"`
	Encrypted     bool      `json:"encrypted"`
	Contents      *Contents `json:"contents,omitempty"` // not in an encrypted archive's outer manifest
	Files         []FileSum `json:"files,omitempty"`
}

type Contents struct {
	Configuration bool     `json:"configuration"`
	Certificates  []string `json:"certificates"` // certificate IDs with files
	SharedSites   []string `json:"sharedSites"`  // site IDs with a shared folder
	// PortableSecrets is set when secrets.json carries the secrets for
	// another machine (encrypted archives only).
	PortableSecrets bool `json:"portableSecrets"`
}

type FileSum struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

var nameRE = regexp.MustCompile(`^nodehoster-backup-([A-Za-z0-9._-]+)-([0-9]{8}-[0-9]{6})\.zip$`)

// ArchiveName is the file name of an archive made on host at t.
func ArchiveName(host string, t time.Time) string {
	return NamePrefix + SafeHost(host) + "-" + t.UTC().Format(nameTime) + ".zip"
}

// SafeHost keeps a host name usable in file and object names.
func SafeHost(host string) string {
	var b strings.Builder
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "server"
	}
	return b.String()
}

// ParseName reads the host and time from an archive name; ok is false for
// any other name, which retention never touches.
func ParseName(name string) (host string, t time.Time, ok bool) {
	m := nameRE.FindStringSubmatch(name)
	if m == nil {
		return "", time.Time{}, false
	}
	t, err := time.ParseInLocation(nameTime, m[2], time.UTC)
	if err != nil {
		return "", time.Time{}, false
	}
	return m[1], t, true
}

// ---- writing

// Writer builds an (unencrypted) archive.
type Writer struct {
	ctx context.Context
	zw  *zip.Writer
	m   Manifest
}

// NewWriter starts an archive; ctx ending stops the copying of files.
func NewWriter(ctx context.Context, w io.Writer, m Manifest) *Writer {
	m.Format, m.FormatVersion, m.Encrypted = FormatName, FormatVersion, false
	if m.Contents == nil {
		m.Contents = &Contents{}
	}
	return &Writer{ctx: ctx, zw: zip.NewWriter(w), m: m}
}

// Contents is filled by the caller as it adds files.
func (w *Writer) Contents() *Contents { return w.m.Contents }

func (w *Writer) AddBytes(name string, data []byte) error {
	return w.add(name, bytes.NewReader(data), time.Now(), zip.Deflate)
}

// AddFile adds the file at p.
func (w *Writer) AddFile(name, p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	return w.add(name, f, st.ModTime(), method(name))
}

// AddDir adds the regular files under dir as prefix/<relative path>.
// Symbolic links and junctions are skipped, not followed: a shared folder
// that links elsewhere must not pull that elsewhere into the backup. It
// returns the bytes added.
func (w *Writer) AddDir(prefix, dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if err := w.AddFile(prefix+"/"+filepath.ToSlash(rel), p); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

// Already-compressed files are stored, which is faster and no bigger.
func method(name string) uint16 {
	switch strings.ToLower(path.Ext(name)) {
	case ".zip", ".gz", ".tgz", ".7z", ".rar", ".jpg", ".jpeg", ".png", ".gif", ".webp", ".mp4", ".mp3", ".woff", ".woff2", ".br", ".zst", ".pdf":
		return zip.Store
	}
	return zip.Deflate
}

func (w *Writer) add(name string, r io.Reader, mod time.Time, m uint16) error {
	if err := checkName(name); err != nil {
		return err
	}
	fw, err := w.zw.CreateHeader(&zip.FileHeader{Name: name, Method: m, Modified: mod})
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(fw, h), readerCtx{w.ctx, r})
	if err != nil {
		return err
	}
	w.m.Files = append(w.m.Files, FileSum{Path: name, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))})
	return nil
}

// Close writes the manifest and finishes the zip. It does not close the
// underlying writer.
func (w *Writer) Close() error {
	data, err := json.MarshalIndent(w.m, "", "  ")
	if err != nil {
		return err
	}
	fw, err := w.zw.CreateHeader(&zip.FileHeader{Name: ManifestFile, Method: zip.Deflate, Modified: time.Now()})
	if err != nil {
		return err
	}
	if _, err := fw.Write(data); err != nil {
		return err
	}
	return w.zw.Close()
}

// Encrypt writes to w an encrypted archive wrapping the archive at inner.
func Encrypt(ctx context.Context, w io.Writer, inner string, m Manifest, passphrase string) error {
	f, err := os.Open(inner)
	if err != nil {
		return err
	}
	defer f.Close()
	pub := Manifest{Format: FormatName, FormatVersion: FormatVersion, Version: m.Version, Hostname: m.Hostname, Created: m.Created, Encrypted: true}
	data, _ := json.MarshalIndent(pub, "", "  ")
	zw := zip.NewWriter(w)
	mw, err := zw.Create(ManifestFile)
	if err != nil {
		return err
	}
	if _, err := mw.Write(data); err != nil {
		return err
	}
	pw, err := zw.CreateHeader(&zip.FileHeader{Name: PayloadFile, Method: zip.Store, Modified: m.Created})
	if err != nil {
		return err
	}
	enc, err := NewEncrypter(pw, passphrase)
	if err != nil {
		return err
	}
	if _, err := io.Copy(enc, readerCtx{ctx, f}); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return zw.Close()
}

// ---- reading

// ErrNotArchive means the file is not a NodeHoster backup archive.
var ErrNotArchive = errors.New("not a NodeHoster backup archive")

// Inspect reads an archive's manifest (the outer one if it is encrypted).
func Inspect(p string) (*Manifest, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, ErrNotArchive
	}
	defer zr.Close()
	return readManifest(&zr.Reader)
}

func readManifest(zr *zip.Reader) (*Manifest, error) {
	for _, f := range zr.File {
		if f.Name != ManifestFile {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		var m Manifest
		if err := json.NewDecoder(io.LimitReader(rc, 64<<20)).Decode(&m); err != nil || m.Format != FormatName {
			return nil, ErrNotArchive
		}
		if m.FormatVersion > FormatVersion {
			return nil, fmt.Errorf("the backup was made by a newer NodeHoster (%s); upgrade this server first", m.Version)
		}
		return &m, nil
	}
	return nil, ErrNotArchive
}

// Decrypt writes the inner archive of the encrypted archive at p to w.
func Decrypt(ctx context.Context, p, passphrase string, w io.Writer) error {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return ErrNotArchive
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != PayloadFile {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		dec, err := NewDecrypter(rc, passphrase)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, readerCtx{ctx, dec})
		return err
	}
	return ErrNotArchive
}

// Archive is an open, unencrypted archive whose entry names have all been
// checked.
type Archive struct {
	Manifest Manifest
	zr       *zip.ReadCloser
	files    map[string]*zip.File
	sums     map[string]FileSum
}

var idRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// checkName is the zip-slip guard: an entry is one of the known files, a
// certificate file or a path under a site's shared folder, in clean,
// relative, forward-slash form. Anything else rejects the whole archive.
func checkName(name string) error {
	bad := func() error { return fmt.Errorf("archive entry %q is not allowed", name) }
	if name == "" || strings.ContainsAny(name, "\\\x00:") || strings.HasPrefix(name, "/") || path.Clean(name) != strings.TrimSuffix(name, "/") {
		return bad()
	}
	for _, el := range strings.Split(strings.TrimSuffix(name, "/"), "/") {
		if el == ".." || el == "." || el == "" {
			return bad()
		}
	}
	switch name {
	case ManifestFile, ConfigFile, SecretsFile:
		return nil
	}
	parts := strings.Split(strings.TrimSuffix(name, "/"), "/")
	switch {
	case parts[0] == "certs" && len(parts) == 3 && idRE.MatchString(parts[1]) &&
		(parts[2] == "cert.pem" || parts[2] == "key.pem" || parts[2] == "key.pem.sealed"):
		return nil
	case parts[0] == "sites" && len(parts) >= 3 && idRE.MatchString(parts[1]) && parts[2] == "shared":
		return nil
	}
	return bad()
}

// Open opens the unencrypted archive at p and checks every entry.
func Open(p string) (*Archive, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, ErrNotArchive
	}
	m, err := readManifest(&zr.Reader)
	if err != nil {
		zr.Close()
		return nil, err
	}
	if m.Encrypted {
		zr.Close()
		return nil, errors.New("the archive is encrypted")
	}
	a := &Archive{Manifest: *m, zr: zr, files: map[string]*zip.File{}, sums: map[string]FileSum{}}
	if a.Manifest.Contents == nil {
		a.Manifest.Contents = &Contents{}
	}
	for _, s := range m.Files {
		a.sums[s.Path] = s
	}
	for _, f := range zr.File {
		if err := checkName(f.Name); err != nil {
			zr.Close()
			return nil, err
		}
		if f.Mode()&fs.ModeSymlink != 0 {
			zr.Close()
			return nil, fmt.Errorf("archive entry %q is a symbolic link", f.Name)
		}
		if f.Name == ManifestFile || strings.HasSuffix(f.Name, "/") {
			continue
		}
		if _, ok := a.sums[f.Name]; !ok {
			zr.Close()
			return nil, fmt.Errorf("archive entry %q is not in the manifest", f.Name)
		}
		if _, dup := a.files[f.Name]; dup {
			zr.Close()
			return nil, fmt.Errorf("archive entry %q appears twice", f.Name)
		}
		a.files[f.Name] = f
	}
	return a, nil
}

func (a *Archive) Close() error { return a.zr.Close() }

// Has reports whether the archive has the file.
func (a *Archive) Has(name string) bool { _, ok := a.files[name]; return ok }

// ReadFile returns a file's content after checking its checksum.
func (a *Archive) ReadFile(name string) ([]byte, error) {
	f, ok := a.files[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	var buf bytes.Buffer
	if err := a.copy(f, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (a *Archive) copy(f *zip.File, w io.Writer) error {
	s := a.sums[f.Name]
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(rc, s.Size+1))
	if err != nil {
		return fmt.Errorf("%s: %w", f.Name, err)
	}
	if n != s.Size || hex.EncodeToString(h.Sum(nil)) != s.SHA256 {
		return fmt.Errorf("%s: checksum mismatch; the archive is damaged", f.Name)
	}
	return nil
}

// Extract writes the files under prefix/ to dest, keeping their relative
// paths, and returns how many it wrote.
func (a *Archive) Extract(prefix, dest string) (int, error) {
	dest, err := filepath.Abs(dest)
	if err != nil {
		return 0, err
	}
	n := 0
	for name, f := range a.files {
		rel, ok := strings.CutPrefix(name, prefix+"/")
		if !ok || rel == "" {
			continue
		}
		p := filepath.Join(dest, filepath.FromSlash(rel))
		// checkName already refused "..", this is belt and braces.
		if !strings.HasPrefix(p, dest+string(os.PathSeparator)) {
			return n, fmt.Errorf("archive entry %q escapes the destination", name)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			return n, err
		}
		w, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
		if err != nil {
			return n, err
		}
		err = a.copy(f, w)
		if cerr := w.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return n, err
		}
		if !f.Modified.IsZero() {
			os.Chtimes(p, f.Modified, f.Modified)
		}
		n++
	}
	return n, nil
}
