//go:build windows

package deploy

import "github.com/parthh37/nodehoster/internal/winacl"

// createClosedRelease creates a release folder that only the service (and
// administrators) can open, even though the site's run-as account has
// Modify access to the site's folder around it. The service fills it,
// extracting the upload or cloning with git, and links its shared paths;
// were the account able to reach it meanwhile, it could swap a folder of
// the release for a junction and have SYSTEM write where it likes, or add
// a git hook for SYSTEM to run. The folder has its permissions from the
// moment it exists (winacl.Mkdir), so no handle opened before can be kept.
func createClosedRelease(dir string) error {
	sddl, err := winacl.ServiceOnlyForCaller()
	if err != nil {
		return err
	}
	return winacl.Mkdir(dir, sddl)
}

// openRelease gives a release what the site's folder passes down, the
// run-as account's Modify access included, for the install and build
// commands that run as that account.
func openRelease(dir string) error {
	return winacl.Set(dir, winacl.Inherited)
}
