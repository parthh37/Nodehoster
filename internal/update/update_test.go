package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

func TestParseAndCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.3", 0},
		{"v1.2.3", "1.2.3", 0},
		{"1.2.4", "1.2.3", 1},
		{"1.10.0", "1.9.9", 1},
		{"2.0.0", "1.99.99", 1},
		{"1.2.0-rc1", "1.2.0", -1},
		{"1.2.0-rc2", "1.2.0-rc1", 1},
		{"0.0.0-dev.12", "0.0.1", -1},
	} {
		a, ok1 := ParseVersion(tc.a)
		b, ok2 := ParseVersion(tc.b)
		if !ok1 || !ok2 {
			t.Fatalf("parse %q / %q", tc.a, tc.b)
		}
		if got := a.Compare(b); sign(got) != tc.want {
			t.Errorf("%s vs %s = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
	for _, bad := range []string{"", "dev", "1.2", "1.2.3.4", "1.x.3", "1..3", "-1.2.3", "1.2.3456789"} {
		if _, ok := ParseVersion(bad); ok {
			t.Errorf("ParseVersion(%q) accepted", bad)
		}
	}
	if v, _ := ParseVersion("0.0.0-dev.3"); v.IsRelease() {
		t.Error("a development build counts as a release")
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// feedServer serves a release feed like the S3 bucket's: latest.json, its
// signature, and the setup in a folder named after the version.
type feedServer struct {
	*httptest.Server
	manifest []byte
	sig      []byte
	setup    []byte
	version  string
}

func newFeedServer(t *testing.T, priv ed25519.PrivateKey, version string) *feedServer {
	t.Helper()
	f := &feedServer{setup: []byte("MZ pretend setup for " + version), version: version}
	sum := sha256.Sum256(f.setup)
	name := "NodeHoster-" + version + "-setup.exe"
	m := Manifest{Version: version, Published: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Files: []File{
		{Name: "NodeHoster-" + version + "-windows-x64.zip", Key: "x/" + version + "/zip", Size: 10, SHA256: strings.Repeat("0", 64)},
		{Name: name, Key: "nodehoster/releases/" + version + "/" + name, Size: int64(len(f.setup)), SHA256: hex.EncodeToString(sum[:])},
	}}
	f.manifest, _ = json.MarshalIndent(m, "", "  ")
	f.sig = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, f.manifest)) + "\n")
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest.json":
			w.Write(f.manifest)
		case "/releases/latest.json.sig":
			if f.sig == nil {
				http.NotFound(w, r)
				return
			}
			w.Write(f.sig)
		case "/releases/" + version + "/" + name:
			w.Write(f.setup)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func testFeed(url string, keys ...ed25519.PublicKey) *Feed {
	return &Feed{URL: url + "/releases/latest.json", Keys: keys, Client: http.DefaultClient}
}

func TestFeedVerifiesAndDownloads(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	srv := newFeedServer(t, priv, "1.4.0")
	f := testFeed(srv.URL, pub)

	rel, err := f.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version.String() != "1.4.0" || !strings.HasSuffix(rel.URL, "/releases/1.4.0/NodeHoster-1.4.0-setup.exe") {
		t.Fatalf("release = %+v", rel)
	}
	dir := t.TempDir()
	path, err := f.Download(context.Background(), rel, dir)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != string(srv.setup) {
		t.Fatalf("downloaded %q", b)
	}
	// A second download reuses the verified file.
	srv.setup = []byte("changed")
	if again, err := f.Download(context.Background(), rel, dir); err != nil || again != path {
		t.Fatalf("second download: %v %s", err, again)
	}
}

func TestFeedRejectsBadSignatures(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	otherPub, otherPriv, _ := ed25519.GenerateKey(rand.Reader)

	for _, tc := range []struct {
		name   string
		mutate func(f *feedServer)
		keys   []ed25519.PublicKey
	}{
		{"unsigned", func(f *feedServer) { f.sig = nil }, []ed25519.PublicKey{pub}},
		{"signed by someone else", func(f *feedServer) {
			f.sig = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(otherPriv, f.manifest)))
		}, []ed25519.PublicKey{pub}},
		{"manifest changed after signing", func(f *feedServer) {
			f.manifest = []byte(strings.Replace(string(f.manifest), `"1.4.0"`, `"9.9.9"`, 1))
		}, []ed25519.PublicKey{pub}},
		{"garbage signature", func(f *feedServer) { f.sig = []byte("not base64!") }, []ed25519.PublicKey{pub}},
		{"no trusted keys", func(*feedServer) {}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newFeedServer(t, priv, "1.4.0")
			tc.mutate(srv)
			if _, err := testFeed(srv.URL, tc.keys...).Latest(context.Background()); !errors.Is(err, ErrSignature) {
				t.Fatalf("err = %v, want ErrSignature", err)
			}
		})
	}
	// Any of several keys will do (key rotation).
	srv := newFeedServer(t, priv, "1.4.0")
	if _, err := testFeed(srv.URL, otherPub, pub).Latest(context.Background()); err != nil {
		t.Fatalf("second key: %v", err)
	}
}

func TestDownloadRejectsTamperedSetup(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	srv := newFeedServer(t, priv, "1.4.0")
	f := testFeed(srv.URL, pub)
	rel, err := f.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	srv.setup = []byte("MZ evil setup of the same...") // other bytes
	dir := t.TempDir()
	if _, err := f.Download(context.Background(), rel, dir); err == nil {
		t.Fatal("a setup that does not match the manifest was accepted")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("left files behind: %v", entries)
	}
}

func TestEmbeddedKeysParse(t *testing.T) {
	if f := NewFeed(DefaultFeed); len(f.Keys) == 0 {
		t.Fatal("no public keys")
	}
}

func TestOutcome(t *testing.T) {
	start := time.Date(2026, 9, 1, 3, 0, 0, 0, time.UTC)
	pending := &model.UpdateResult{From: "1.1.0", To: "1.2.0", StartedAt: start}

	if Outcome(nil, nil, "1.2.0") != nil {
		t.Fatal("an outcome without an installation")
	}
	// The new version runs: success, before the updater wrote anything.
	if out := Outcome(pending, nil, "1.2.0"); !out.OK {
		t.Fatalf("new version running: %+v", out)
	}
	// The old version runs and the updater said why.
	res := *pending
	res.ExitCode, res.Error = 7, "setup refused"
	if out := Outcome(pending, &res, "1.1.0"); out.OK || out.Error != "setup refused" || out.ExitCode != 7 {
		t.Fatalf("failed update: %+v", out)
	}
	// A result from another installation is ignored.
	stale := res
	stale.StartedAt = start.Add(-time.Hour)
	if out := Outcome(pending, &stale, "1.1.0"); out.OK || out.Error == "setup refused" || out.Error == "" {
		t.Fatalf("stale result: %+v", out)
	}
	// Interrupted: no result, old version.
	if out := Outcome(pending, nil, "1.1.0"); out.OK || out.Error == "" {
		t.Fatalf("interrupted: %+v", out)
	}
}

func TestStateFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "updates")
	if r, err := ReadPending(dir); r != nil || err != nil {
		t.Fatalf("empty: %v %v", r, err)
	}
	p := &model.UpdateResult{From: "1.0.0", To: "1.1.0", StartedAt: time.Now().UTC().Truncate(time.Second)}
	if err := WritePending(dir, p); err != nil {
		t.Fatal(err)
	}
	if err := WriteResult(dir, p); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPending(dir)
	if err != nil || got.To != "1.1.0" || !got.StartedAt.Equal(p.StartedAt) {
		t.Fatalf("read back %+v %v", got, err)
	}
	ClearInstall(dir)
	if r, _ := ReadPending(dir); r != nil {
		t.Fatal("pending.json survived ClearInstall")
	}
	if r, _ := ReadResult(dir); r != nil {
		t.Fatal("result.json survived ClearInstall")
	}
}
