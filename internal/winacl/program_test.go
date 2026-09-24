package winacl

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestWriters(t *testing.T) {
	const self, users, alice = "S-1-5-21-1-2-3-1001", "S-1-5-32-545", "S-1-5-21-1-2-3-1002"
	name := func(sid string) string { return "<" + sid + ">" }
	// Program Files: administrators and the system change it, users read.
	programFiles := []ace{
		{allow: true, sid: sidTrustedInstaller, mask: 0x1f01ff},
		{allow: true, sid: sidSystem, mask: 0x1301bf},
		{allow: true, sid: sidAdministrators, mask: 0x1301bf},
		{allow: true, sid: users, mask: 0x1200a9},
		{allow: true, sid: sidCreatorOwner, mask: rightGenericAll, inheritOnly: true},
	}
	cases := []struct {
		name   string
		owner  string
		aces   []ace
		rights uint32
		want   []string
	}{
		{"program files", sidTrustedInstaller, programFiles, folderRights, nil},
		{"owned by this process's account", self, programFiles, programRights, nil},
		{"owned by a local user", alice, programFiles, programRights, []string{"its owner <" + alice + ">"}},
		{"no owner", "", programFiles, programRights, []string{"its owner (unknown)"}},
		// C:\ lets every signed-in user create folders (AD) in it, and gives
		// them Modify on what they create (inherit-only): harmless above a
		// program, fatal for the program's own folder.
		{`C:\ above a program`, sidTrustedInstaller, []ace{
			{allow: true, sid: sidAdministrators, mask: 0x1f01ff},
			{allow: true, sid: "S-1-5-11", mask: rightAppendData},
			{allow: true, sid: "S-1-5-11", mask: 0x1301bf, inheritOnly: true},
		}, ancestorRights, nil},
		{`C:\Python39, made in C:\`, sidAdministrators, []ace{
			{allow: true, sid: sidAdministrators, mask: 0x1f01ff},
			{allow: true, sid: "S-1-5-11", mask: 0x1301bf},
		}, folderRights, []string{"<S-1-5-11>"}},
		{"a folder that lets users delete what is in it", sidSystem, []ace{
			{allow: true, sid: users, mask: rightDeleteChild | 0x1200a9},
		}, ancestorRights, []string{"<" + users + ">"}},
		{"deny entries and other rights do not count", sidSystem, []ace{
			{allow: false, sid: users, mask: 0x1f01ff},
			{allow: true, sid: users, mask: 0x1200a9 | 0x100 /* write attributes */},
		}, programRights, nil},
		{"generic write", sidSystem, []ace{{allow: true, sid: alice, mask: rightGenericWr}}, programRights, []string{"<" + alice + ">"}},
		{"an object entry", sidSystem, []ace{{allow: true, mask: rightWriteData}}, programRights, []string{"an entry of an unusual type"}},
		{"owner rights and creator owner", sidAdministrators, []ace{
			{allow: true, sid: sidOwnerRights, mask: 0x1f01ff},
			{allow: true, sid: sidCreatorOwner, mask: 0x1f01ff},
		}, folderRights, nil},
		{"listed once", alice, []ace{
			{allow: true, sid: alice, mask: rightWriteData},
			{allow: true, sid: alice, mask: rightDelete},
		}, programRights, []string{"its owner <" + alice + ">", "<" + alice + ">"}},
	}
	for _, c := range cases {
		if got := writers(c.owner, c.aces, c.rights, self, name); !slices.Equal(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestChain(t *testing.T) {
	root := filepath.VolumeName(t.TempDir()) + string(filepath.Separator)
	exe := filepath.Join(root, "opt", "python", "bin", "python3")
	lib := filepath.Join(root, "opt", "python", "lib")
	var got []string
	for _, n := range chain(exe, []string{lib}) {
		kind := map[uint32]string{programRights: "program", folderRights: "folder", ancestorRights: "above"}[n.rights]
		opt := ""
		if n.optional {
			opt = "?"
		}
		got = append(got, kind+" "+filepath.ToSlash(strings.TrimPrefix(n.path, root))+opt)
	}
	want := []string{"program opt/python/bin/python3", "folder opt/python/bin", "folder opt/python/lib?", "above opt/python", "above opt", "above "}
	if !slices.Equal(got, want) {
		t.Errorf("chain:\n got %q\nwant %q", got, want)
	}
}
