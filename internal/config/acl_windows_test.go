//go:build windows

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parthh37/nodehoster/internal/winacl"
	"golang.org/x/sys/windows"
)

const (
	sidSystem = "S-1-5-18"
	sidAdmins = "S-1-5-32-544"
	sidUsers  = "S-1-5-32-545"

	fullAccess  = 0x1f01ff // FA
	readExecute = 0x1200a9
)

// %ProgramData%'s own entries for local users: read and execute on
// everything below, and create files and folders (WD, AD, WEA, WA) in
// every folder below.
const programDataUsers = "D:(A;OICI;0x1200a9;;;BU)(A;CI;0x116;;;BU)"

func TestEnsureRestrictsDataDir(t *testing.T) {
	parent := t.TempDir()
	if err := winacl.Set(parent, programDataUsers); err != nil {
		t.Fatal(err)
	}
	p := NewPaths(filepath.Join(parent, "NodeHoster"))

	// An installation from before this change: a database local users
	// can read.
	if err := os.MkdirAll(p.Data, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.DB, []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	if access(t, p.DB)[sidUsers]&windows.FILE_READ_DATA == 0 {
		t.Fatal("setup: local users cannot read the database even before Ensure")
	}

	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	// A file written afterwards, like the initial administrator password.
	pw := filepath.Join(p.Data, "initial-admin-password.txt")
	if err := WriteFileAtomic(pw, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	checkServiceOnly(t, p, pw)

	if _, protected, err := winacl.Read(p.Data); err != nil || !protected {
		t.Errorf("%s inherits from its parent (err %v)", p.Data, err)
	}
	for _, d := range []string{p.Node, p.Run} {
		m := access(t, d)
		if m[sidUsers] != readExecute {
			t.Errorf("%s: local users have %#x, want read and execute (%#x)", d, m[sidUsers], readExecute)
		}
		if m[sidSystem] != fullAccess || m[sidAdmins] != fullAccess {
			t.Errorf("%s: SYSTEM %#x, Administrators %#x, want full control", d, m[sidSystem], m[sidAdmins])
		}
	}

	// Ensure runs at every start and puts back what was changed.
	if err := winacl.Set(p.Data, winacl.ServiceOnly+"(A;OICI;FA;;;BU)"); err != nil {
		t.Fatal(err)
	}
	if access(t, p.DB)[sidUsers] == 0 {
		t.Fatal("setup: granting users access to the data directory did not reach the database")
	}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	checkServiceOnly(t, p, pw)
}

// checkServiceOnly checks that only SYSTEM and Administrators (and, running
// unelevated, the test's own account) can use the sensitive parts of the
// data directory.
func checkServiceOnly(t *testing.T, p Paths, extra ...string) {
	t.Helper()
	sddl, err := winacl.ServiceOnlyForCaller()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, _ := sd.DACL()
	want, _ := winacl.Entries(dacl, false)
	allowed := map[string]bool{}
	for _, e := range want {
		allowed[e.SID] = true
	}
	for _, path := range append([]string{p.Data, p.DB, p.Config, p.Logs, p.SiteLogs, p.Certs, p.ACME, p.Sites, p.Mail, p.Tmp}, extra...) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue // nodehoster.json: not written by Ensure
		}
		m := access(t, path)
		if m[sidSystem] != fullAccess || m[sidAdmins] != fullAccess {
			t.Errorf("%s: SYSTEM %#x, Administrators %#x, want full control", path, m[sidSystem], m[sidAdmins])
		}
		for sid, mask := range m {
			if !allowed[sid] {
				t.Errorf("%s: %s has access %#x", path, sid, mask)
			}
		}
	}
}

func access(t *testing.T, path string) map[string]windows.ACCESS_MASK {
	t.Helper()
	entries, _, err := winacl.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	return winacl.Access(entries)
}
