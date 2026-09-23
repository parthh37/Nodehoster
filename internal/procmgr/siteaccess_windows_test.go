//go:build windows

package procmgr

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parthh37/nodehoster/internal/winacl"
	"golang.org/x/sys/windows"
)

const modify = 0x1301bf

// A site's run-as account gets Modify on the site's folder, and loses it
// when the site's identity changes or is turned off.
func TestSiteAccess(t *testing.T) {
	// Well-known accounts nothing else in the temporary folder grants to.
	first, _ := windows.CreateWellKnownSid(windows.WinLocalServiceSid)
	second, _ := windows.CreateWellKnownSid(windows.WinNetworkServiceSid)
	dir := filepath.Join(t.TempDir(), "sites", "s1")

	if err := siteAccess(dir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("a site without a run-as account got a folder: %v", err)
	}

	if err := siteAccess(dir, first); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "shared", "upload.txt")
	if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	checkSiteAccess(t, dir, file, first, second)

	if err := siteAccess(dir, second); err != nil {
		t.Fatal(err)
	}
	checkSiteAccess(t, dir, file, second, first)

	if err := siteAccess(dir, nil); err != nil {
		t.Fatal(err)
	}
	checkSiteAccess(t, dir, file, nil, second)
}

func checkSiteAccess(t *testing.T, dir, file string, granted, revoked *windows.SID) {
	t.Helper()
	for _, path := range []string{dir, file} {
		entries, _, err := winacl.Read(path)
		if err != nil {
			t.Fatal(err)
		}
		m := winacl.Access(entries)
		if granted != nil && m[granted.String()] != modify {
			t.Errorf("%s: %s has %#x, want Modify (%#x)", path, granted, m[granted.String()], modify)
		}
		if mask := m[revoked.String()]; mask != 0 {
			t.Errorf("%s: %s still has %#x", path, revoked, mask)
		}
	}
}
