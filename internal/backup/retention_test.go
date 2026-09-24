package backup

import (
	"slices"
	"testing"
	"time"
)

func TestExpired(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 3, 20, 3, 0, 0, 0, time.UTC)
	var objs []Object
	// Daily archives of web01 for 10 days, plus things retention must leave.
	for d := 0; d < 10; d++ {
		objs = append(objs, Object{Name: ArchiveName("web01", now.AddDate(0, 0, -d).Add(-30*time.Minute))})
	}
	objs = append(objs,
		Object{Name: ArchiveName("web02", now.AddDate(0, 0, -30))}, // another server's
		Object{Name: "notes.txt"},
		Object{Name: "nodehoster-backup-web01-20200101-000000.zip.partial"},
		Object{Name: "nodehoster-backup-web01-2020.zip"},
	)
	names := func(list []Object) []string {
		var out []string
		for _, o := range list {
			out = append(out, o.Name)
		}
		slices.Sort(out)
		return out
	}
	day := func(d int) string { return ArchiveName("web01", now.AddDate(0, 0, -d).Add(-30*time.Minute)) }

	for _, tc := range []struct {
		name               string
		keepLast, keepDays int
		want               []string
	}{
		{"no rules keep everything", 0, 0, nil},
		{"keep last 7", 7, 0, []string{day(7), day(8), day(9)}},
		{"keep 5 days", 0, 5, []string{day(5), day(6), day(7), day(8), day(9)}},
		{"either rule keeps", 3, 6, []string{day(6), day(7), day(8), day(9)}},
		{"either rule keeps (count wins)", 8, 2, []string{day(8), day(9)}},
		{"keep last 1", 1, 0, names(func() []Object {
			var o []Object
			for d := 1; d < 10; d++ {
				o = append(o, Object{Name: day(d)})
			}
			return o
		}())},
	} {
		got := names(Expired(objs, "web01", tc.keepLast, tc.keepDays, now))
		want := append([]string(nil), tc.want...)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s:\n got  %v\n want %v", tc.name, got, want)
		}
	}
	// The newest archive is never deleted, even when every rule says so.
	if got := Expired(objs[:1], "web01", 0, 1, now.AddDate(1, 0, 0)); len(got) != 0 {
		t.Errorf("the only archive expired: %v", got)
	}
	// Host names compare case-insensitively (Windows computer names).
	if got := Expired(objs, "WEB01", 7, 0, now); len(got) != 3 {
		t.Errorf("upper-case host: %d expired", len(got))
	}
}

func TestArchivesSortsNewestFirst(t *testing.T) {
	t.Parallel()
	a := Object{Name: "nodehoster-backup-x-20260101-000000.zip"}
	b := Object{Name: "nodehoster-backup-x-20260301-000000.zip"}
	c := Object{Name: "readme.md"}
	got := Archives([]Object{a, c, b})
	if len(got) != 2 || got[0].Name != b.Name || got[0].Host != "x" || got[0].Created.Month() != 3 {
		t.Errorf("Archives = %+v", got)
	}
}
