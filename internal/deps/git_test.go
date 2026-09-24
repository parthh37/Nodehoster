package deps

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMinGitAssetNames(t *testing.T) {
	for name, arch := range map[string]string{
		"MinGit-2.51.0-64-bit.zip":         "64-bit",
		"MinGit-2.51.0.2-64-bit.zip":       "64-bit", // a v2.51.0.windows.2 release
		"MinGit-2.51.0-arm64.zip":          "arm64",
		"MinGit-2.51.0-busybox-64-bit.zip": "",
		"MinGit-2.51.0-32-bit.zip":         "",
		"Git-2.51.0-64-bit.exe":            "",
		"MinGit-2.51.0-64-bit.zip.sig":     "",
	} {
		m := minGitRe.FindStringSubmatch(name)
		got := ""
		if m != nil {
			got = m[1]
		}
		if got != arch {
			t.Errorf("%s: arch %q, want %q", name, got, arch)
		}
	}
}

func TestExtractZipRefusesEscapes(t *testing.T) {
	dir := t.TempDir()
	write := func(entries ...string) string {
		p := filepath.Join(dir, "a.zip")
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		w := zip.NewWriter(f)
		for _, e := range entries {
			fw, _ := w.Create(e)
			fw.Write([]byte("x"))
		}
		w.Close()
		f.Close()
		return p
	}
	dest := filepath.Join(dir, "out")
	if err := extractZip(write("cmd/git.exe", "etc/gitconfig"), dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "cmd", "git.exe")); err != nil {
		t.Fatal(err)
	}
	err := extractZip(write("cmd/git.exe", "../evil.txt"), filepath.Join(dir, "out2"))
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("escaping entry: %v", err)
	}
}

func TestFindGitOnPath(t *testing.T) {
	bin := t.TempDir()
	name := "git"
	if os.PathSeparator == '\\' {
		name = "git.exe"
	}
	if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if p, err := FindGit(t.TempDir()); err != nil || filepath.Dir(p) != bin {
		t.Fatalf("FindGit = %q, %v", p, err)
	}
	// Nor in Git for Windows' folder (GitHub's Windows runners have one).
	t.Setenv("PATH", t.TempDir())
	t.Setenv("ProgramFiles", t.TempDir())
	t.Setenv("ProgramW6432", t.TempDir())
	if _, err := FindGit(t.TempDir()); err == nil || !strings.Contains(err.Error(), "nodehoster deps install git") {
		t.Fatalf("FindGit without git: %v", err)
	}
}
