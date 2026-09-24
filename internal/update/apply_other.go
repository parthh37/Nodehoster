//go:build !windows

package update

import "errors"

// Supported reports whether this installation can update itself.
func Supported() (bool, string) {
	return false, "automatic updates are for Windows installations made with setup"
}

// Launch starts the updater; see the Windows implementation.
func Launch(dir, setup string) error { return errors.ErrUnsupported }

// RunUpdater is the updater's entry point; see the Windows implementation.
func RunUpdater(args []string) int { return 1 }
