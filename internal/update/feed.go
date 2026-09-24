package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultFeed is where CI publishes the latest release's manifest.
const DefaultFeed = "https://s3.store1.gc.parthh.com/nodehoster-release/nodehoster/releases/latest.json"

// PublicKeys verify the feed's signature (base64 Ed25519 keys). The private
// half of each is only in CI's UPDATE_SIGNING_KEY secret. Several may be
// listed while a key is being replaced: a release that knows the new key
// has to be out before CI signs with it.
var PublicKeys = []string{
	"Cu6zb1WJlHDhOHh1DCRawslS0yyDeluFLEjw7XKN5H0=",
}

// ReleaseNotes is the page describing a version.
func ReleaseNotes(v string) string {
	return "https://github.com/parthh37/Nodehoster/releases/tag/v" + v
}

// Limits on what the feed may send.
const (
	maxManifest = 1 << 20
	maxSig      = 1 << 10
	maxSetup    = 512 << 20
)

// ErrSignature means the manifest is not signed by a key this build
// trusts: it is ignored, whatever it says.
var ErrSignature = errors.New("the release feed's signature is missing or invalid")

// Manifest is latest.json, as tools/s3publish writes it.
type Manifest struct {
	Version   string    `json:"version"`
	Commit    string    `json:"commit"`
	Published time.Time `json:"published"`
	Files     []File    `json:"files"`
}

type File struct {
	Name   string `json:"name"`
	Key    string `json:"key"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Release is the setup a verified manifest offers.
type Release struct {
	Version   Version
	Published time.Time
	Setup     File
	URL       string // of the setup
}

// Feed reads a signed release feed.
type Feed struct {
	URL    string
	Keys   []ed25519.PublicKey
	Client *http.Client
}

// NewFeed is the feed at url, verified with PublicKeys.
func NewFeed(url string) *Feed {
	f := &Feed{URL: url, Client: &http.Client{Timeout: 10 * time.Minute}}
	for _, k := range PublicKeys {
		b, err := base64.StdEncoding.DecodeString(k)
		if err != nil || len(b) != ed25519.PublicKeySize {
			panic("update: bad public key " + k)
		}
		f.Keys = append(f.Keys, ed25519.PublicKey(b))
	}
	return f
}

// Latest fetches the manifest and its signature (URL + ".sig") and returns
// the release's setup. Nothing in a manifest is trusted before its
// signature is.
func (f *Feed) Latest(ctx context.Context) (*Release, error) {
	body, err := f.get(ctx, f.URL, maxManifest)
	if err != nil {
		return nil, err
	}
	sigText, err := f.get(ctx, f.URL+".sig", maxSig)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSignature, err)
	}
	if !Verify(f.Keys, body, sigText) {
		return nil, ErrSignature
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("the release feed is not valid: %w", err)
	}
	v, ok := ParseVersion(m.Version)
	if !ok {
		return nil, fmt.Errorf("the release feed names an invalid version %q", m.Version)
	}
	want := "NodeHoster-" + v.String() + "-setup.exe"
	for _, file := range m.Files {
		if file.Name != want {
			continue
		}
		if len(file.SHA256) != 64 || file.Size <= 0 || file.Size > maxSetup {
			return nil, fmt.Errorf("the release feed describes %s incorrectly", want)
		}
		// Release files are in a folder named after the version, next
		// to latest.json.
		base, err := url.Parse(f.URL)
		if err != nil {
			return nil, err
		}
		u := base.ResolveReference(&url.URL{Path: v.String() + "/" + want})
		return &Release{Version: v, Published: m.Published, Setup: file, URL: u.String()}, nil
	}
	return nil, fmt.Errorf("release %s has no %s", v, want)
}

// Verify reports whether sigText (base64) is a signature of body by one
// of keys.
func Verify(keys []ed25519.PublicKey, body, sigText []byte) bool {
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigText)))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	for _, k := range keys {
		if ed25519.Verify(k, body, sig) {
			return true
		}
	}
	return false
}

func (f *Feed) get(ctx context.Context, u string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", u, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s is too large", u)
	}
	return b, nil
}

// Download saves the release's setup in dir and returns its path. The file
// must have the size and SHA-256 of the signed manifest; one already there
// with the right contents is reused. dir must be writable by
// administrators only: the file is run as SYSTEM.
func (f *Feed) Download(ctx context.Context, r *Release, dir string) (string, error) {
	path := filepath.Join(dir, r.Setup.Name)
	if sum, n, err := hashFile(path); err == nil && n == r.Setup.Size && strings.EqualFold(sum, r.Setup.SHA256) {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := f.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", r.URL, resp.Status)
	}
	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name()) // after the rename, a no-op
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, r.Setup.Size+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("download %s: %w", r.URL, err)
	}
	if n != r.Setup.Size {
		return "", fmt.Errorf("download %s: got %d bytes, expected %d", r.URL, n, r.Setup.Size)
	}
	if sum := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(sum, r.Setup.SHA256) {
		return "", fmt.Errorf("download %s: SHA-256 %s does not match the signed manifest", r.URL, sum)
	}
	os.Remove(path)
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}

func hashFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	h := sha256.New()
	n, err := io.Copy(h, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
