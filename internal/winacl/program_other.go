//go:build !windows

package winacl

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// CheckProgram returns nil when only root (or the account this process
// runs as) can change the program at exe, the folder it is in, the extra
// folders dirs (those that exist), and every folder above: each must be
// owned by one of them, and writable by no one else (group 0 aside), except
// a folder above with the sticky bit, like /tmp: others may add to it but
// not replace what is not theirs. A program reached
// through a link is checked both where the link is and where it points;
// links on the way are not checked themselves, their folders are.
func CheckProgram(exe string, dirs ...string) error {
	self := os.Geteuid()
	paths := []string{filepath.Clean(exe)}
	if real, err := filepath.EvalSymlinks(exe); err == nil && real != paths[0] {
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

func checkNode(n node, self int) error {
	fi, err := os.Lstat(n.path)
	if err != nil {
		if n.optional && errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read the permissions of %s: %w", n.path, err)
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return nil // what it points to is checked on its own
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("read the owner of %s", n.path)
	}
	var who []string
	if uid := int(st.Uid); uid != 0 && uid != self {
		who = append(who, "its owner (uid "+strconv.Itoa(uid)+")")
	}
	perm := fi.Mode().Perm()
	sticky := n.rights == ancestorRights && fi.Mode()&fs.ModeSticky != 0
	if perm&0o020 != 0 && st.Gid != 0 && !sticky {
		who = append(who, "group "+strconv.Itoa(int(st.Gid)))
	}
	if perm&0o002 != 0 && !sticky {
		who = append(who, "everyone")
	}
	if len(who) > 0 {
		return &UntrustedError{Path: n.path, Who: who}
	}
	return nil
}
