// Package deps finds and installs the programs NodeHoster relies on but
// does not ship: Git, for deployments from a repository (and npm packages
// that come from one). Node.js is the other; nodeversions installs it.
package deps

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/winacl"
)

// GitDir is where NodeHoster keeps its own Git: MinGit, the distribution
// of Git for Windows made for programs that run git (no shell, no GUI, no
// system-wide configuration), in the data directory next to the Node.js
// runtimes.
func GitDir(data string) string { return filepath.Join(data, "git") }

// checkProgram vets a git before deployments run it (tests replace it).
var checkProgram = winacl.CheckServiceProgram

// FindGit returns the git that deployments run: the one on PATH, else
// NodeHoster's own, else Git for Windows in its standard folder. The last
// two matter because a Windows service keeps the PATH it started with
// until the computer restarts, so a Git installed after that is not on it.
// Deployments run git as the service (LocalSystem), so one that users
// other than administrators could replace (a folder on PATH anyone can
// write to) is passed over (winacl.CheckProgram).
func FindGit(data string) (string, error) {
	var candidates []string
	if p, err := exec.LookPath("git"); err == nil {
		candidates = append(candidates, p)
	}
	if runtime.GOOS == "windows" {
		candidates = append(candidates, filepath.Join(GitDir(data), "cmd", "git.exe"))
		for _, env := range []string{"ProgramFiles", "ProgramW6432"} {
			if pf := os.Getenv(env); pf != "" {
				candidates = append(candidates, filepath.Join(pf, "Git", "cmd", "git.exe"))
			}
		}
	}
	var untrusted error
	for _, p := range candidates {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if err := checkProgram(p); err != nil {
			if untrusted == nil {
				untrusted = err
			}
			continue
		}
		return p, nil
	}
	if untrusted != nil {
		return "", fmt.Errorf("git is not used: %w; install one only administrators can change with: nodehoster deps install git", untrusted)
	}
	return "", errors.New("git is not installed on the server; install it with: nodehoster deps install git")
}

// gitRelease is the part of GitHub's release API that InstallGit reads.
type gitRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name   string `json:"name"`
		URL    string `json:"browser_download_url"`
		Digest string `json:"digest"` // "sha256:<hex>"
	} `json:"assets"`
}

const gitReleaseAPI = "https://api.github.com/repos/git-for-windows/git/releases/latest"

// MinGit-2.51.0-64-bit.zip, MinGit-2.51.0-arm64.zip; not the busybox
// variant, which lacks parts of git that deployments may use.
var minGitRe = regexp.MustCompile(`^MinGit-[0-9][0-9.]*-(64-bit|arm64)\.zip$`)

// InstallGit downloads the latest MinGit into GitDir(data), checking it
// against the SHA-256 GitHub publishes for the file, and returns the path
// of git.exe. It replaces a MinGit installed earlier.
func InstallGit(ctx context.Context, data string, logf func(format string, a ...any)) (string, error) {
	if runtime.GOOS != "windows" {
		return "", errors.New("install git with this system's package manager")
	}
	arch := map[string]string{"amd64": "64-bit", "arm64": "arm64"}[runtime.GOARCH]
	if arch == "" {
		return "", fmt.Errorf("MinGit is not published for %s", runtime.GOARCH)
	}
	client := &http.Client{Timeout: 10 * time.Minute}

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, gitReleaseAPI, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("find the latest Git for Windows: %w", err)
	}
	var rel gitRelease
	err = json.NewDecoder(resp.Body).Decode(&rel)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("find the latest Git for Windows: HTTP %d", resp.StatusCode)
	}
	if err != nil {
		return "", fmt.Errorf("find the latest Git for Windows: %w", err)
	}
	var url, want, name string
	for _, a := range rel.Assets {
		if m := minGitRe.FindStringSubmatch(a.Name); m != nil && m[1] == arch {
			url, name = a.URL, a.Name
			want, _ = strings.CutPrefix(a.Digest, "sha256:")
			break
		}
	}
	if url == "" {
		return "", fmt.Errorf("Git for Windows %s has no MinGit for this computer", rel.TagName)
	}
	// Never run an unverified download: no checksum, no install.
	if len(want) != 64 {
		return "", fmt.Errorf("GitHub published no SHA-256 for %s", name)
	}
	logf("Downloading %s...", name)

	tmpDir := filepath.Join(data, "tmp")
	os.MkdirAll(tmpDir, 0o750)
	tmp, err := os.CreateTemp(tmpDir, "mingit-*.zip")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err = client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: HTTP %d", name, resp.StatusCode)
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
		return "", fmt.Errorf("download %s: %w", name, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want) {
		return "", fmt.Errorf("checksum mismatch for %s: the download is corrupt or was tampered with", name)
	}

	dest := GitDir(data)
	partial := dest + ".partial"
	os.RemoveAll(partial)
	if err := extractZip(tmp.Name(), partial); err != nil {
		os.RemoveAll(partial)
		return "", fmt.Errorf("extract %s: %w", name, err)
	}
	os.RemoveAll(dest)
	if err := os.Rename(partial, dest); err != nil {
		os.RemoveAll(partial)
		return "", err
	}
	exe := filepath.Join(dest, "cmd", "git.exe")
	if _, err := os.Stat(exe); err != nil {
		return "", fmt.Errorf("%s has no cmd\\git.exe", name)
	}
	logf("Installed Git %s in %s", strings.TrimPrefix(rel.TagName, "v"), dest)
	return exe, nil
}

// extractZip unpacks src into dest, refusing entries that would land
// outside it.
func extractZip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		p := filepath.Join(dest, filepath.FromSlash(f.Name))
		if p != dest && !strings.HasPrefix(p, dest+string(os.PathSeparator)) {
			return fmt.Errorf("archive entry %q escapes the destination", f.Name)
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(p, 0o755)
			continue
		}
		os.MkdirAll(filepath.Dir(p), 0o755)
		rc, err := f.Open()
		if err != nil {
			return err
		}
		w, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode()|0o200)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(w, rc)
		rc.Close()
		if cerr := w.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}
