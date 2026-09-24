package winacl

import (
	"fmt"
	"path/filepath"
	"strings"
)

// NodeHoster runs as LocalSystem. Every program it runs as the service (an
// interpreter asked its version, a runtime that builds a release) must be
// one only administrators can change: a program, or a DLL next to it, that
// any local user can write or put in place is a way for them to run code as
// SYSTEM. CheckProgram tells the two apart before NodeHoster runs anything
// it found on the server rather than installed itself.
//
// What counts, on Windows, is what an account other than SYSTEM,
// Administrators or TrustedInstaller (or the account NodeHoster runs as) is
// allowed:
//
//   - on the program: to write it, delete it, or change its permissions or
//     owner;
//   - on its folder (and on the extra folders the caller names, such as
//     Python's Lib): to add, delete or change files in it;
//   - on every folder above: to delete or rename what is in it, or change
//     its permissions or owner. Adding folders is allowed there: every
//     local user may create folders in C:\, which does not let them touch
//     C:\Program Files.
//
// And every one of them must be owned by one of those accounts, since an
// owner can always change the permissions. Deny entries are not taken into
// account (the check errs on the side of refusing), nor is membership: a
// file writable by a named administrator's account rather than by the
// Administrators group is refused like any other account's.

// Access rights, as in an access control entry's mask.
const (
	rightWriteData   = 0x00000002 // FILE_WRITE_DATA; FILE_ADD_FILE on a folder
	rightAppendData  = 0x00000004 // FILE_APPEND_DATA; FILE_ADD_SUBDIRECTORY on a folder
	rightDeleteChild = 0x00000040 // FILE_DELETE_CHILD
	rightDelete      = 0x00010000
	rightWriteDAC    = 0x00040000
	rightWriteOwner  = 0x00080000
	rightGenericAll  = 0x10000000
	rightGenericWr   = 0x40000000
)

// The rights that let an account change a program, the folder it is in, and
// a folder further up.
const (
	programRights  = rightWriteData | rightAppendData | rightDelete | rightWriteDAC | rightWriteOwner | rightGenericAll | rightGenericWr
	folderRights   = programRights | rightDeleteChild
	ancestorRights = rightDeleteChild | rightDelete | rightWriteDAC | rightWriteOwner | rightGenericAll
)

// Accounts trusted with a program NodeHoster runs.
const (
	sidSystem           = "S-1-5-18"
	sidAdministrators   = "S-1-5-32-544"
	sidTrustedInstaller = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"
	// CREATOR OWNER in an entry that applies to the object itself matches
	// no one: it is only a template for what is created below. OWNER
	// RIGHTS is the owner, which is checked on its own.
	sidCreatorOwner = "S-1-3-0"
	sidOwnerRights  = "S-1-3-4"
)

// ace is an access control entry as the check sees it.
type ace struct {
	allow       bool   // an allow entry (of any kind); deny entries are skipped
	inheritOnly bool   // passed down to what is created below, not applied here
	sid         string // "" when the entry's kind has no plain SID
	mask        uint32
}

// trustedSID reports whether an account may change programs NodeHoster
// runs: SYSTEM, Administrators, TrustedInstaller, or the account
// NodeHoster itself runs as (self; a developer running it unelevated).
func trustedSID(sid, self string) bool {
	switch sid {
	case sidSystem, sidAdministrators, sidTrustedInstaller:
		return true
	}
	return self != "" && sid == self
}

// writers lists the accounts, other than trusted ones, that the entries
// allow any of rights, and the owner when it is not trusted; name turns a
// SID into what the list shows. An allow entry without a plain SID cannot
// be told to be harmless and is listed as such.
func writers(owner string, aces []ace, rights uint32, self string, name func(sid string) string) []string {
	var out []string
	add := func(who string) {
		for _, w := range out {
			if w == who {
				return
			}
		}
		out = append(out, who)
	}
	if owner == "" || !trustedSID(owner, self) {
		add("its owner " + orUnknown(owner, name))
	}
	for _, e := range aces {
		if !e.allow || e.inheritOnly || e.mask&rights == 0 {
			continue
		}
		switch {
		case e.sid == "":
			add("an entry of an unusual type")
		case e.sid == sidCreatorOwner || e.sid == sidOwnerRights || trustedSID(e.sid, self):
		default:
			add(name(e.sid))
		}
	}
	return out
}

func orUnknown(sid string, name func(string) string) string {
	if sid == "" {
		return "(unknown)"
	}
	return name(sid)
}

// UntrustedError says who, besides administrators, can change a program
// NodeHoster was about to run.
type UntrustedError struct {
	Path string   // the file or folder that is not protected
	Who  []string // accounts (names where known, else SIDs) and reasons
}

func (e *UntrustedError) Error() string {
	return fmt.Sprintf("%s can be changed by %s, not only by administrators", e.Path, strings.Join(e.Who, ", "))
}

// node is a file or folder CheckProgram looks at, and the rights no one
// but administrators may have on it. An optional one (an extra folder) is
// skipped when it does not exist.
type node struct {
	path     string
	rights   uint32
	optional bool
}

// chain lists what CheckProgram looks at for a program: the program, its
// folder, the extra folders, and every folder above the program's.
func chain(exe string, extra []string) []node {
	exe = filepath.Clean(exe)
	dir := filepath.Dir(exe)
	nodes := []node{{exe, programRights, false}, {dir, folderRights, false}}
	for _, d := range extra {
		nodes = append(nodes, node{filepath.Clean(d), folderRights, true})
	}
	for p := dir; ; {
		up := filepath.Dir(p)
		if up == p {
			break
		}
		nodes = append(nodes, node{up, ancestorRights, false})
		p = up
	}
	return nodes
}

// CheckServiceProgram is CheckProgram when this process is privileged
// (the NodeHoster service, LocalSystem; root elsewhere), and nil
// otherwise: a program other users could replace only lets them run code
// as an account more powerful than theirs when the server runs as one.
// A server run as an ordinary account (development, CI) runs what it
// finds, as that account.
func CheckServiceProgram(exe string, dirs ...string) error {
	if !Privileged() {
		return nil
	}
	return CheckProgram(exe, dirs...)
}
