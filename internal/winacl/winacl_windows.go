//go:build windows

package winacl

import (
	"fmt"
	"slices"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DACLs in SDDL. OICI: the entry is inherited by every file and folder below.
//
// FA is FILE_ALL_ACCESS; 0x1200a9 is FILE_GENERIC_READ | FILE_GENERIC_EXECUTE
// (read and execute, no write); 0x1301bf is that plus FILE_GENERIC_WRITE and
// DELETE, the "Modify" of Explorer's security tab: everything but changing
// permissions or taking ownership.
const (
	// ServiceOnly admits SYSTEM and Administrators. P (protected) stops
	// inheritance from the parent, which for %ProgramData% gives every
	// local user read access and the right to create files.
	ServiceOnly = "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"

	// UsersRead keeps what the folder inherits and lets local users read and
	// execute, but not change, what is in it.
	UsersRead = "D:(A;OICI;0x1200a9;;;BU)"

	// Inherited removes every explicit entry: the folder has exactly what
	// its parent passes down.
	Inherited = "D:"

	modifyMask = "0x1301bf"
)

// Modify keeps what the folder inherits and gives one account Modify access.
func Modify(sid *windows.SID) string {
	return "D:(A;OICI;" + modifyMask + ";;;" + sid.String() + ")"
}

// ServiceOnlyForCaller is ServiceOnly, plus the account running this process
// when it is neither SYSTEM nor an elevated administrator: a developer
// running the server unelevated against their own data folder must not lock
// themselves out of it.
func ServiceOnlyForCaller() (string, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("current user: %w", err)
	}
	if u.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		return ServiceOnly, nil
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return "", err
	}
	// Token 0 is this thread's effective token. Unelevated, Administrators
	// is a deny-only group in it and IsMember reports false.
	if member, err := windows.Token(0).IsMember(admins); err == nil && member {
		return ServiceOnly, nil
	}
	return ServiceOnly + "(A;OICI;FA;;;" + u.User.Sid.String() + ")", nil
}

// Changing a folder's DACL rewrites the inherited entries of everything below
// it, which for a site's node_modules is a long walk: one at a time.
var mu sync.Mutex

// Set gives path the DACL described by sddl: its explicit entries, and
// whether inheritance from the parent is blocked. Entries inherited from the
// parent are kept (unless sddl is protected) and not compared, so Set leaves
// a folder that is already right alone and is cheap to call on every start.
// Otherwise Windows applies the new DACL and re-propagates inherited entries
// through the whole tree below path, replacing whatever it inherited before.
func Set(path, sddl string) error {
	want, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("parse %q: %w", sddl, err)
	}
	wantDACL, _, err := want.DACL()
	if err != nil {
		return fmt.Errorf("parse %q: %w", sddl, err)
	}
	if wantDACL == nil { // would be applied as a NULL DACL: everyone, full control
		return fmt.Errorf("%q has no DACL", sddl)
	}
	ctl, _, err := want.Control()
	if err != nil {
		return err
	}
	protected := ctl&windows.SE_DACL_PROTECTED != 0

	mu.Lock()
	defer mu.Unlock()
	cur, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read permissions of %s: %w", path, err)
	}
	if matches(cur, protected, wantDACL) {
		return nil
	}
	info := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION)
	if protected {
		info = windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, info, nil, nil, wantDACL, nil); err != nil {
		return fmt.Errorf("set permissions of %s: %w", path, err)
	}
	return nil
}

func matches(sd *windows.SECURITY_DESCRIPTOR, protected bool, want *windows.ACL) bool {
	ctl, _, err := sd.Control()
	if err != nil || (ctl&windows.SE_DACL_PROTECTED != 0) != protected {
		return false
	}
	have, _, err := sd.DACL()
	if err != nil || have == nil { // a NULL DACL grants everyone everything
		return false
	}
	h, ok1 := Entries(have, false)
	w, ok2 := Entries(want, false)
	return ok1 && ok2 && slices.Equal(h, w)
}

// Entry is one access control entry. Mask is the access it allows or denies.
type Entry struct {
	Type  byte // ACCESS_ALLOWED_ACE_TYPE (0), ACCESS_DENIED_ACE_TYPE (1), ...
	Flags byte // inheritance flags, including INHERITED_ACE
	Mask  windows.ACCESS_MASK
	SID   string // "S-1-5-18"; empty for entry types without a plain SID
}

// Entries lists acl's entries in order; inherited ones only when asked.
// ok is false when the list could not be read.
func Entries(acl *windows.ACL, inherited bool) (entries []Entry, ok bool) {
	for i := range uint32(acl.AceCount) {
		var a *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &a); err != nil {
			return nil, false
		}
		if a.Header.AceFlags&windows.INHERITED_ACE != 0 && !inherited {
			continue
		}
		e := Entry{Type: a.Header.AceType, Flags: a.Header.AceFlags, Mask: a.Mask}
		// Allowed and denied entries end with the SID; object and
		// callback entries are laid out differently.
		if a.Header.AceType == windows.ACCESS_ALLOWED_ACE_TYPE || a.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			e.SID = (*windows.SID)(unsafe.Pointer(&a.SidStart)).String()
		}
		entries = append(entries, e)
	}
	return entries, true
}

// Read returns every entry of path's DACL, inherited ones included, and
// whether inheritance from the parent is blocked.
func Read(path string) (entries []Entry, protected bool, err error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, false, err
	}
	ctl, _, err := sd.Control()
	if err != nil {
		return nil, false, err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return nil, false, err
	}
	if acl == nil {
		return nil, false, fmt.Errorf("%s has no DACL: everyone has full access", path)
	}
	entries, ok := Entries(acl, true)
	if !ok {
		return nil, false, fmt.Errorf("read the DACL of %s", path)
	}
	return entries, ctl&windows.SE_DACL_PROTECTED != 0, nil
}

// Access sums, per SID, the access the allow entries that apply to path
// itself give (not those only passed down to children).
func Access(entries []Entry) map[string]windows.ACCESS_MASK {
	m := map[string]windows.ACCESS_MASK{}
	for _, e := range entries {
		if e.Type == windows.ACCESS_ALLOWED_ACE_TYPE && e.Flags&windows.INHERIT_ONLY_ACE == 0 {
			m[e.SID] |= e.Mask
		}
	}
	return m
}
