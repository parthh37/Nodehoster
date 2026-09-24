//go:build windows

package deploy

import (
	"os"
	"os/exec"
	"testing"
)

// junction plants a directory junction, which any account that can create
// folders somewhere can make, unlike a symbolic link.
func junction(t *testing.T, target, link string) {
	t.Helper()
	if out, err := exec.Command("cmd", "/d", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J %s %s: %v\n%s", link, target, err, out)
	}
	if fi, err := os.Lstat(link); err != nil || fi.IsDir() {
		t.Fatalf("the junction reads as a folder: %v", err)
	}
}

// TestLinkSharedRefusesPlantedJunctions: as for symbolic links, with the
// junctions a site's run-as account can create without any privilege.
func TestLinkSharedRefusesPlantedJunctions(t *testing.T) {
	t.Parallel()
	testLinkSharedRefusesLinks(t, junction)
}
