//go:build windows

package config

import "github.com/parthh37/nodehoster/internal/winacl"

// secure keeps the data directory to the service. A folder under
// %ProgramData% inherits read access (and the right to create files) for
// every local user, and file modes mean nothing on Windows: without this any
// user could read the database, the certificate and ACME keys, the mail
// spool, the logs and the initial administrator password, or plant files
// the service would later load. The data directory admits only SYSTEM and
// Administrators, and nothing is inherited from %ProgramData%.
//
// Two folders are then opened for reading again, because a site with a
// run-as identity runs node under that account: node\ (the Node.js
// runtimes) and run\ (the agent script node loads). Neither holds anything
// secret, and neither is writable by those accounts. A site's own folder,
// sites\<id>\, is granted to its run-as account when the site starts (see
// procmgr). Logs, including the applications' own output in logs\sites\,
// are written by the service, so they stay closed.
func (p Paths) secure() error {
	root, err := winacl.ServiceOnlyForCaller()
	if err != nil {
		return err
	}
	if err := winacl.Set(p.Data, root); err != nil {
		return err
	}
	for _, d := range []string{p.Node, p.Run} {
		if err := winacl.Set(d, winacl.UsersRead); err != nil {
			return err
		}
	}
	return nil
}
