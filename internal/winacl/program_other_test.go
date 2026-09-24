//go:build !windows

package winacl

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckProgramModes(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0o755)
	bin := filepath.Join(dir, "bin")
	os.Mkdir(bin, 0o755)
	exe := filepath.Join(bin, "python3")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CheckProgram(exe); err != nil {
		// The temporary folder's own parents are the system's; if they are
		// not protected on this machine there is nothing to test.
		t.Skipf("the temporary folder is not protected here: %v", err)
	}
	lib := filepath.Join(dir, "lib")

	refused := func(what string, wantPath string) {
		t.Helper()
		err := CheckProgram(exe, lib)
		var ue *UntrustedError
		if !errors.As(err, &ue) || ue.Path != wantPath {
			t.Fatalf("%s: %v", what, err)
		}
	}
	os.Chmod(exe, 0o757)
	refused("a program anyone can write", exe)
	if os.Getegid() != 0 { // group 0's members are trusted
		os.Chmod(exe, 0o775)
		refused("a program its group can write", exe)
	}
	os.Chmod(exe, 0o755)

	os.Chmod(bin, 0o777|os.ModeSticky)
	refused("a program in a folder anyone can add to", bin)
	os.Chmod(bin, 0o755)

	// Above the program's folder, a sticky folder anyone can add to (like
	// /tmp) is fine; one without the bit is not.
	os.Chmod(dir, 0o777|os.ModeSticky)
	if err := CheckProgram(exe); err != nil {
		t.Fatalf("sticky folder above: %v", err)
	}
	os.Chmod(dir, 0o777)
	refused("a folder above that anyone can rename in", dir)
	os.Chmod(dir, 0o755)

	// Extra folders are checked when they exist.
	if err := CheckProgram(exe, lib); err != nil {
		t.Fatalf("missing extra folder: %v", err)
	}
	os.Mkdir(lib, 0o755)
	os.Chmod(lib, 0o777)
	refused("an extra folder anyone can write", lib)
	os.Chmod(lib, 0o755)

	// A link is checked where it points too.
	other := filepath.Join(dir, "other")
	os.Mkdir(other, 0o755)
	target := filepath.Join(other, "real-python")
	os.WriteFile(target, nil, 0o755)
	link := filepath.Join(bin, "python")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := CheckProgram(link); err != nil {
		t.Fatalf("link: %v", err)
	}
	os.Chmod(target, 0o777)
	if err := CheckProgram(link); err == nil || !strings.Contains(err.Error(), "real-python") {
		t.Fatalf("a link to a program anyone can write: %v", err)
	}

	if err := CheckProgram(filepath.Join(bin, "missing")); err == nil {
		t.Fatal("a missing program passed")
	}
}
