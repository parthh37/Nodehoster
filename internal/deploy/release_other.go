//go:build !windows

package deploy

import "os"

// createClosedRelease creates a release folder. Elsewhere than on Windows
// NodeHoster does not manage the site folder's permissions for a run-as
// account (see release_windows.go), so there is nothing to close.
func createClosedRelease(dir string) error { return os.Mkdir(dir, 0o750) }

func openRelease(string) error { return nil }
