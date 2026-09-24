//go:build windows

package winacl

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckProgramACLs(t *testing.T) {
	// The temporary folder is in the profile of the account running the
	// tests, which CheckProgram trusts like SYSTEM and Administrators.
	dir := t.TempDir()
	bin := filepath.Join(dir, "Python312")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(bin, "python.exe")
	if err := os.WriteFile(exe, []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CheckProgram(exe); err != nil {
		t.Skipf("the temporary folder is not protected here: %v", err)
	}
	refused := func(what, wantPath string) {
		t.Helper()
		err := CheckProgram(exe, filepath.Join(bin, "Lib"))
		var ue *UntrustedError
		if !errors.As(err, &ue) || !strings.EqualFold(ue.Path, wantPath) {
			t.Fatalf("%s: %v", what, err)
		}
		if !strings.Contains(err.Error(), "Users") {
			t.Errorf("%s: the account is not named: %v", what, err)
		}
	}

	// Users may read and run it: fine.
	if err := Set(bin, UsersRead); err != nil {
		t.Fatal(err)
	}
	if err := CheckProgram(exe); err != nil {
		t.Fatalf("users read: %v", err)
	}
	// Users may modify the program.
	if err := Set(exe, "D:(A;;0x1301bf;;;BU)"); err != nil {
		t.Fatal(err)
	}
	refused("a program users can modify", exe)
	if err := Set(exe, Inherited); err != nil {
		t.Fatal(err)
	}
	// Users may add files (a DLL) to its folder: only "create files", the
	// way C:\ lets them create folders.
	if err := Set(bin, "D:(A;;0x100002;;;BU)"); err != nil {
		t.Fatal(err)
	}
	refused("a folder users can add files to", bin)
	// Above the program's folder, creating folders is harmless, deleting
	// what is in it is not.
	if err := Set(bin, Inherited); err != nil {
		t.Fatal(err)
	}
	if err := Set(dir, "D:(A;;0x100004;;;BU)"); err != nil {
		t.Fatal(err)
	}
	if err := CheckProgram(exe); err != nil {
		t.Fatalf("users may create folders above: %v", err)
	}
	if err := Set(dir, "D:(A;;0x100040;;;BU)"); err != nil {
		t.Fatal(err)
	}
	refused("a folder above where users can delete", dir)
	if err := Set(dir, Inherited); err != nil {
		t.Fatal(err)
	}
	// An extra folder (Python's Lib) is checked when it exists.
	lib := filepath.Join(bin, "Lib")
	if err := os.Mkdir(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Set(lib, "D:(A;OICI;0x1301bf;;;BU)"); err != nil {
		t.Fatal(err)
	}
	refused("an extra folder users can modify", lib)
}
