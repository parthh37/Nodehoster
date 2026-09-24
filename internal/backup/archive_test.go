package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func init() { ScryptLogN = 10 } // fast key derivation for tests

func encrypt(t *testing.T, plain []byte, pass string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewEncrypter(&buf, pass)
	if err != nil {
		t.Fatal(err)
	}
	// Uneven writes, to cross chunk boundaries in the middle of a write.
	for p := plain; len(p) > 0; {
		n := min(len(p), 10007)
		if _, err := w.Write(p[:n]); err != nil {
			t.Fatal(err)
		}
		p = p[n:]
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func decrypt(data []byte, pass string) ([]byte, error) {
	r, err := NewDecrypter(bytes.NewReader(data), pass)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func TestEncryptionRoundTrip(t *testing.T) {
	t.Parallel()
	for _, size := range []int{0, 1, chunkSize - 1, chunkSize, chunkSize + 1, 3 * chunkSize, 3*chunkSize + 17} {
		plain := make([]byte, size)
		rand.Read(plain)
		enc := encrypt(t, plain, "correct horse")
		if bytes.Contains(enc, plain[:min(size, 64)]) && size > 16 {
			t.Errorf("size %d: ciphertext contains the plaintext", size)
		}
		got, err := decrypt(enc, "correct horse")
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("size %d: round trip differs", size)
		}
	}
}

func TestEncryptionRejectsWrongPassphraseAndDamage(t *testing.T) {
	t.Parallel()
	plain := make([]byte, 2*chunkSize+100)
	rand.Read(plain)
	enc := encrypt(t, plain, "right")

	if _, err := decrypt(enc, "wrong"); !errors.Is(err, ErrWrongPassphrase) {
		t.Errorf("wrong passphrase: err = %v", err)
	}
	if _, err := decrypt([]byte("PK\x03\x04 not encrypted at all, long enough for a header"), "right"); !errors.Is(err, ErrNotEncrypted) {
		t.Errorf("plain data: err = %v", err)
	}
	// Flipped byte in the second chunk.
	bad := bytes.Clone(enc)
	bad[headerSize+chunkSize+16+5] ^= 1
	if _, err := decrypt(bad, "right"); err == nil || errors.Is(err, ErrWrongPassphrase) {
		t.Errorf("damaged chunk: err = %v", err)
	}
	// Cut exactly at a chunk boundary: the second chunk looks like the last.
	cut := enc[:headerSize+2*(chunkSize+16)]
	if _, err := decrypt(cut, "right"); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Errorf("truncated at a boundary: err = %v", err)
	}
	// Cut in the middle of a chunk.
	if _, err := decrypt(enc[:len(enc)-50], "right"); err == nil {
		t.Error("truncated mid-chunk: no error")
	}
	// A header parameter changed (it is authenticated).
	hdr := bytes.Clone(enc)
	hdr[39] = 7
	if _, err := decrypt(hdr, "right"); err == nil {
		t.Error("changed header: no error")
	}
}

// scrypt parameters come from the header before it can be authenticated:
// only those NodeHoster writes are accepted, so a planted archive cannot
// make a restore allocate gigabytes (128·N·r bytes; r = 255, N = 2^20 is
// ~34 GB). Checked with a small N, so that nothing large is allocated.
func TestEncryptionRejectsCostlyParameters(t *testing.T) {
	t.Parallel()
	enc := encrypt(t, []byte("payload"), "right")
	for _, tc := range []struct {
		name string
		i    int
		v    byte
	}{
		{"r = 255", 10, 255},
		{"r = 16", 10, 16},
		{"p = 16", 11, 16},
		{"p = 2", 11, 2},
		{"log2 N = 21", 9, 21},
	} {
		bad := bytes.Clone(enc)
		bad[tc.i] = tc.v
		if _, err := decrypt(bad, "right"); err == nil || !strings.Contains(err.Error(), "unsupported encryption parameters") {
			t.Errorf("%s: err = %v", tc.name, err)
		}
	}
}

// buildArchive writes a small archive and returns its path.
func buildArchive(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	p := filepath.Join(dir, "a.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	w := NewWriter(context.Background(), f, Manifest{Version: "test", Hostname: "web01", Created: time.Now().UTC()})
	for name, body := range files {
		if err := w.AddBytes(name, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return p
}

func TestArchiveWriteReadExtract(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	shared := filepath.Join(dir, "shared")
	os.MkdirAll(filepath.Join(shared, "uploads", "2024"), 0o750)
	os.WriteFile(filepath.Join(shared, ".env"), []byte("A=1\n"), 0o640)
	os.WriteFile(filepath.Join(shared, "uploads", "2024", "pic.png"), []byte("png"), 0o640)
	if err := os.Symlink("/etc", filepath.Join(shared, "escape")); err != nil {
		t.Log("symlinks unavailable:", err)
	}

	p := filepath.Join(dir, "a.zip")
	f, _ := os.Create(p)
	w := NewWriter(context.Background(), f, Manifest{Version: "test", Hostname: "web01", Created: time.Now().UTC()})
	w.AddBytes(ConfigFile, []byte(`{"sites":[]}`))
	if _, err := w.AddDir("sites/abc/shared", shared); err != nil {
		t.Fatal(err)
	}
	w.Contents().SharedSites = []string{"abc"}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	a, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.Manifest.Hostname != "web01" || len(a.Manifest.Contents.SharedSites) != 1 {
		t.Errorf("manifest = %+v", a.Manifest)
	}
	if a.Has("sites/abc/shared/escape") {
		t.Error("a symbolic link was followed into the archive")
	}
	data, err := a.ReadFile(ConfigFile)
	if err != nil || string(data) != `{"sites":[]}` {
		t.Fatalf("ReadFile = %q, %v", data, err)
	}
	out := filepath.Join(dir, "out")
	n, err := a.Extract("sites/abc/shared", out)
	if err != nil || n != 2 {
		t.Fatalf("Extract = %d, %v", n, err)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "uploads", "2024", "pic.png")); string(b) != "png" {
		t.Errorf("extracted file = %q", b)
	}
}

func TestArchiveEncryptedRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	inner := buildArchive(t, dir, map[string]string{ConfigFile: `{"x":1}`, SecretsFile: `{}`})
	outer := filepath.Join(dir, "enc.zip")
	f, _ := os.Create(outer)
	if err := Encrypt(context.Background(), f, inner, Manifest{Version: "test", Hostname: "web01", Created: time.Now().UTC()}, "pw"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	m, err := Inspect(outer)
	if err != nil || !m.Encrypted || m.Contents != nil || m.Hostname != "web01" {
		t.Fatalf("Inspect = %+v, %v", m, err)
	}
	if _, err := Open(outer); err == nil {
		t.Error("an encrypted archive opened without decryption")
	}
	var buf bytes.Buffer
	if err := Decrypt(context.Background(), outer, "nope", &buf); !errors.Is(err, ErrWrongPassphrase) {
		t.Errorf("wrong passphrase: %v", err)
	}
	dec := filepath.Join(dir, "dec.zip")
	df, _ := os.Create(dec)
	if err := Decrypt(context.Background(), outer, "pw", df); err != nil {
		t.Fatal(err)
	}
	df.Close()
	a, err := Open(dec)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if b, _ := a.ReadFile(ConfigFile); string(b) != `{"x":1}` {
		t.Errorf("config = %q", b)
	}
}

// rawZip writes a zip whose manifest lists every entry (with its real
// checksum), so only the names are under test.
func rawZip(t *testing.T, names ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "evil.zip")
	f, _ := os.Create(p)
	zw := zip.NewWriter(f)
	var sums []FileSum
	for _, n := range names {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte("x"))
		sums = append(sums, FileSum{Path: n, Size: 1, SHA256: "2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881"})
	}
	m := Manifest{Format: FormatName, FormatVersion: 1, Files: sums, Contents: &Contents{}}
	w, _ := zw.Create(ManifestFile)
	b, _ := json.Marshal(m)
	w.Write(b)
	zw.Close()
	f.Close()
	return p
}

func TestArchiveRejectsZipSlip(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"../evil.txt",
		"sites/abc/shared/../../../evil.txt",
		"sites/abc/shared/../../x",
		"/etc/passwd",
		"sites\\abc\\shared\\..\\..\\evil",
		"C:/Windows/evil.dll",
		"sites/abc/other/file",
		"sites/../shared/file",
		"certs/abc/../../key.pem",
		"certs/abc/id_rsa",
		"random.txt",
		"sites/abc/shared/./x",
	} {
		p := rawZip(t, ConfigFile, name)
		if a, err := Open(p); err == nil {
			a.Close()
			t.Errorf("%q: archive accepted", name)
		}
	}
	// A well-formed one opens.
	a, err := Open(rawZip(t, ConfigFile, "sites/abc/shared/uploads/a.txt", "certs/abc/cert.pem"))
	if err != nil {
		t.Fatalf("valid archive: %v", err)
	}
	a.Close()
}

func TestArchiveDetectsTampering(t *testing.T) {
	t.Parallel()
	p := rawZip(t, ConfigFile)
	// rawZip's checksums are right; rewrite one entry with other content.
	dir := t.TempDir()
	q := filepath.Join(dir, "t.zip")
	zr, _ := zip.OpenReader(p)
	f, _ := os.Create(q)
	zw := zip.NewWriter(f)
	for _, e := range zr.File {
		w, _ := zw.Create(e.Name)
		if e.Name == ConfigFile {
			w.Write([]byte("y"))
			continue
		}
		rc, _ := e.Open()
		io.Copy(w, rc)
		rc.Close()
	}
	zw.Close()
	f.Close()
	zr.Close()
	a, err := Open(q)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := a.ReadFile(ConfigFile); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Errorf("tampered entry: err = %v", err)
	}
}

func TestArchiveNames(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 3, 1, 2, 30, 5, 0, time.UTC)
	name := ArchiveName("WEB-01.corp", ts)
	if name != "nodehoster-backup-WEB-01.corp-20260301-023005.zip" {
		t.Errorf("ArchiveName = %q", name)
	}
	host, got, ok := ParseName(name)
	if !ok || host != "WEB-01.corp" || !got.Equal(ts) {
		t.Errorf("ParseName = %q %v %v", host, got, ok)
	}
	for _, n := range []string{"nodehoster-backup-web-20260301-0230.zip", "other.zip", "nodehoster-backup-web-20260301-023005.zip.partial", "nodehoster-backup-a/b-20260301-023005.zip"} {
		if _, _, ok := ParseName(n); ok {
			t.Errorf("ParseName(%q) matched", n)
		}
	}
	if SafeHost("héllo wörld") != "h_llo_w_rld" {
		t.Errorf("SafeHost = %q", SafeHost("héllo wörld"))
	}
}
