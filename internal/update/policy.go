package update

// AutoInstall decides whether a release found by the feed may be installed
// unattended, at the scheduled time, when automatic updates are on. A
// release it refuses is still offered: the consoles show it, and an
// administrator installs it with "Install now".
//
// Installing restarts the service, so every hosted site is offline for a
// few seconds, and setup cannot be undone by the updater: an older version
// may not read data a newer one has migrated (setup refuses unattended
// downgrades). Both current and next are releases (IsRelease), and next is
// newer than current.
func AutoInstall(current, next Version) bool {
	// TODO: decide which releases install themselves overnight.
	return true
}
