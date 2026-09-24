package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
)

// symlinkDir plants a symbolic link to a folder, as a site's run-as
// account (or a git repository) could; skipped where this process may not
// create one (Windows, unelevated, without Developer Mode).
func symlinkDir(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symbolic links here: %v", err)
	}
}

// TestLinkSharedRefusesPlantedLinks: a link where a shared path, or a
// folder on the way to it, is does not make the service create or move
// anything outside the site's folder; the deployment fails instead.
func TestLinkSharedRefusesPlantedLinks(t *testing.T) {
	t.Parallel()
	testLinkSharedRefusesLinks(t, symlinkDir)
}

// testLinkSharedRefusesLinks runs the cases with plant making the link to
// a folder (a symbolic link, or on Windows a junction).
func testLinkSharedRefusesLinks(t *testing.T, plant func(t *testing.T, target, link string)) {
	cases := []struct {
		name  string
		paths []string
		// setup plants the link, given the site's shared folder, the
		// release and the folder outside that the link points to.
		setup func(t *testing.T, shared, release, outside string)
	}{
		{"shared folder", []string{".env", "uploads"}, func(t *testing.T, shared, release, outside string) {
			os.Remove(shared)
			plant(t, outside, shared)
		}},
		{"folder in shared", []string{"config/app.json"}, func(t *testing.T, shared, release, outside string) {
			plant(t, outside, filepath.Join(shared, "config"))
		}},
		{"shared path", []string{"uploads"}, func(t *testing.T, shared, release, outside string) {
			plant(t, outside, filepath.Join(shared, "uploads"))
		}},
		{"folder in the release", []string{"public/uploads"}, func(t *testing.T, shared, release, outside string) {
			plant(t, outside, filepath.Join(release, "public"))
		}},
		{"releases folder", []string{".env"}, func(t *testing.T, shared, release, outside string) {
			// The account can rename releases\ and put a junction there.
			releases := filepath.Dir(release)
			moved := filepath.Join(outside, filepath.Base(release))
			if err := os.Rename(release, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(releases); err != nil {
				t.Fatal(err)
			}
			plant(t, outside, releases)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			site := h.staticSite(t, "planted", func(s *model.Site) { s.Deploy.SharedPaths = c.paths })
			shared := model.SharedDir(h.sitesDir, site.ID)
			release := model.ReleaseDir(h.sitesDir, site.ID, "r1")
			outside := filepath.Join(h.root, "outside")
			for _, dir := range []string{shared, release, outside} {
				if err := os.MkdirAll(dir, 0o750); err != nil {
					t.Fatal(err)
				}
			}
			// The release ships a .env that would seed the shared copy.
			if err := os.WriteFile(filepath.Join(release, ".env"), []byte("A=1"), 0o640); err != nil {
				t.Fatal(err)
			}
			c.setup(t, shared, release, outside)
			before := listTree(t, outside)

			err := h.d.linkShared(site, release, discardDepLog(t, h))
			if err == nil || !strings.Contains(err.Error(), "do not follow") {
				t.Fatalf("linkShared: %v, want a refusal", err)
			}
			if after := listTree(t, outside); !slicesEqual(before, after) {
				t.Errorf("the folder outside changed:\nbefore %q\nafter  %q", before, after)
			}
		})
	}
}

// TestLinkSharedIgnoresALinkInTheRelease: a shared path that the release
// ships as a link (a git repository can hold one) is not moved into the
// shared folder: the shared copy starts empty, and what the link pointed
// to is untouched.
func TestLinkSharedIgnoresALinkInTheRelease(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	site := h.staticSite(t, "rellink", func(s *model.Site) { s.Deploy.SharedPaths = []string{".env", "uploads"} })
	release := model.ReleaseDir(h.sitesDir, site.ID, "r1")
	outside := filepath.Join(h.root, "outside")
	os.MkdirAll(release, 0o750)
	os.MkdirAll(outside, 0o750)
	secret := filepath.Join(outside, "secret.txt")
	os.WriteFile(secret, []byte("outside"), 0o640)
	if err := os.Symlink(secret, filepath.Join(release, ".env")); err != nil {
		t.Skipf("cannot create symbolic links here: %v", err)
	}
	symlinkDir(t, outside, filepath.Join(release, "uploads"))

	if err := h.d.linkShared(site, release, discardDepLog(t, h)); err != nil {
		t.Fatal(err)
	}
	shared := model.SharedDir(h.sitesDir, site.ID)
	if got := readFile(t, filepath.Join(shared, ".env")); got != "" {
		t.Errorf("shared .env = %q, want a new empty file", got)
	}
	if fi, err := os.Lstat(filepath.Join(shared, "uploads")); err != nil || !fi.IsDir() {
		t.Errorf("shared uploads: %v", err)
	}
	if got := readFile(t, secret); got != "outside" {
		t.Errorf("the file the link pointed to = %q", got)
	}
	if got := listTree(t, outside); !slicesEqual(got, []string{"secret.txt"}) {
		t.Errorf("outside: %q", got)
	}
	// The release now points into the shared folder, by a relative link.
	os.WriteFile(filepath.Join(shared, ".env"), []byte("B=2"), 0o640)
	if got := readFile(t, filepath.Join(release, ".env")); got != "B=2" {
		t.Errorf("release .env = %q", got)
	}
	if target, err := os.Readlink(filepath.Join(release, ".env")); err == nil && filepath.IsAbs(target) {
		t.Errorf("link target %q is absolute", target)
	}
}

func discardDepLog(t *testing.T, h *harness) *depLog {
	t.Helper()
	f, err := os.CreateTemp(h.root, "link-*.log")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return &depLog{f: f, subs: map[chan string]struct{}{}}
}

// listTree lists what is in dir, recursively, without following links.
func listTree(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if rel, _ := filepath.Rel(dir, p); rel != "." {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func slicesEqual(a, b []string) bool { return strings.Join(a, "\x00") == strings.Join(b, "\x00") }
