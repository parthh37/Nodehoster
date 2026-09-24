//go:build windows

package winacl

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Allow entry types besides ACCESS_ALLOWED_ACE_TYPE. A callback (conditional)
// entry has its SID where a plain one does and is read as if unconditional;
// object entries do not.
const (
	accessAllowedObjectACE         = 0x5
	accessAllowedCallbackACE       = 0x9
	accessAllowedCallbackObjectACE = 0xb
)

// CheckProgram returns nil when only SYSTEM, Administrators and
// TrustedInstaller (and the account this process runs as) can change the
// program at exe, the folder it is in, the extra folders dirs (those that
// exist), and what leads to them (see program.go). Otherwise it returns an
// *UntrustedError naming who else can, or the error reading the
// permissions. A program reached through a link is checked both where the
// link is and where it points.
func CheckProgram(exe string, dirs ...string) error {
	self := ""
	if u, err := windows.GetCurrentProcessToken().GetTokenUser(); err == nil {
		self = u.User.Sid.String()
	}
	paths := []string{filepath.Clean(exe)}
	if real, err := filepath.EvalSymlinks(exe); err == nil && !strings.EqualFold(real, paths[0]) {
		paths = append(paths, real)
	}
	for _, p := range paths {
		for _, n := range chain(p, dirs) {
			if err := checkNode(n, self); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkNode(n node, self string) error {
	if n.optional {
		if _, err := os.Stat(n.path); errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	sd, err := windows.GetNamedSecurityInfo(n.path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read the permissions of %s: %w", n.path, err)
	}
	owner := ""
	if o, _, err := sd.Owner(); err == nil && o != nil {
		owner = o.String()
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("read the permissions of %s: %w", n.path, err)
	}
	if dacl == nil { // a NULL DACL: everyone has full control
		return &UntrustedError{Path: n.path, Who: []string{"everyone (it has no access control list)"}}
	}
	aces, err := readACEs(dacl)
	if err != nil {
		return fmt.Errorf("read the permissions of %s: %w", n.path, err)
	}
	if who := writers(owner, aces, n.rights, self, accountName); len(who) > 0 {
		return &UntrustedError{Path: n.path, Who: who}
	}
	return nil
}

func readACEs(acl *windows.ACL) ([]ace, error) {
	var out []ace
	for i := range uint32(acl.AceCount) {
		var a *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &a); err != nil {
			return nil, err
		}
		e := ace{inheritOnly: a.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0, mask: uint32(a.Mask)}
		switch a.Header.AceType {
		case windows.ACCESS_ALLOWED_ACE_TYPE, accessAllowedCallbackACE:
			e.allow = true
			e.sid = (*windows.SID)(unsafe.Pointer(&a.SidStart)).String()
		case accessAllowedObjectACE, accessAllowedCallbackObjectACE:
			e.allow = true
		}
		out = append(out, e)
	}
	return out, nil
}

// accountName shows a SID as DOMAIN\name (SID), or the SID alone when it
// does not resolve.
func accountName(sid string) string {
	s, err := windows.StringToSid(sid)
	if err != nil {
		return sid
	}
	account, domain, _, err := s.LookupAccount("")
	if err != nil || account == "" {
		return sid
	}
	if domain != "" {
		account = domain + `\` + account
	}
	return account + " (" + sid + ")"
}

// Privileged reports whether this process runs as LocalSystem, as the
// NodeHoster service does: the account whose programs other users must
// not be able to plant.
func Privileged() bool {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return true // unknown: be strict
	}
	return u.User.Sid.IsWellKnown(windows.WinLocalSystemSid)
}
