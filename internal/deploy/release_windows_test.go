//go:build windows

package deploy

import (
	"path/filepath"
	"testing"

	"github.com/parthh37/nodehoster/internal/winacl"
	"golang.org/x/sys/windows"
)

// TestClosedRelease: the release of a site with a run-as account is
// closed to that account (which has Modify on the site's folder) while
// the service fills it, and opened for the commands that run as it.
func TestClosedRelease(t *testing.T) {
	site := t.TempDir()
	users, _ := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
	if err := winacl.Set(site, winacl.Modify(users)); err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join(site, "20260925-000000-abcdef")
	if err := createClosedRelease(rel); err != nil {
		t.Fatal(err)
	}
	entries, protected, err := winacl.Read(rel)
	if err != nil {
		t.Fatal(err)
	}
	if !protected || winacl.Access(entries)[users.String()] != 0 {
		t.Fatalf("closed release: protected %v, entries %+v", protected, entries)
	}
	if err := openRelease(rel); err != nil {
		t.Fatal(err)
	}
	entries, protected, err = winacl.Read(rel)
	if err != nil {
		t.Fatal(err)
	}
	if protected || winacl.Access(entries)[users.String()]&windows.FILE_WRITE_DATA == 0 {
		t.Fatalf("opened release: protected %v, entries %+v", protected, entries)
	}
	if err := createClosedRelease(rel); err == nil {
		t.Fatal("an existing folder was taken over")
	}
}
