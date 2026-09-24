package desktop

import (
	"slices"
	"testing"
	"time"
)

func TestCompareCells(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"1.5 KB", "612 MB", -1},
		{"612 MB", "1.5 KB", 1},
		{"512 B", "1.0 KB", -1},
		{"2 GB", "2 GB", 0},
		{"9%", "12%", -1},
		{"3.5", "12", -1},
		{"1,234", "999", 1},
		{"site9", "site10", -1},
		{"Site10", "site9", 1},
		{"alpha", "Beta", -1},
		{"–", "0%", -1},
		{"", "a", -1},
		{"a", "", 1},
		{"–", "", 0},
		{"2026-01-02 10:00", "2026-01-02 09:59", 1},
		{"v22.1.0", "v22.10.0", -1},
	} {
		if got := CompareCells(tc.a, tc.b); got != tc.want {
			t.Errorf("CompareCells(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestCompareCellsSorts(t *testing.T) {
	got := []string{"12 MB", "–", "1.5 GB", "800 KB", "3 B"}
	slices.SortFunc(got, CompareCells)
	want := []string{"–", "3 B", "800 KB", "12 MB", "1.5 GB"}
	if !slices.Equal(got, want) {
		t.Errorf("sorted %v, want %v", got, want)
	}
}

func TestExpiryText(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{42*24*time.Hour + time.Hour, "in 42 days"},
		{30 * time.Hour, "in 1 day"},
		{5 * time.Hour, "in less than a day"},
		{-5 * time.Hour, "expired less than a day ago"},
		{-30 * time.Hour, "expired 1 day ago"},
		{-72 * time.Hour, "expired 3 days ago"},
	} {
		if got := ExpiryText(now.Add(tc.d), now); got != tc.want {
			t.Errorf("ExpiryText(now%+v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestPercent(t *testing.T) {
	if got := Percent(1, 3); got != 33 {
		t.Errorf("Percent(1, 3) = %d", got)
	}
	if got := Percent(5, 0); got != 0 {
		t.Errorf("Percent(5, 0) = %d", got)
	}
}

func TestSiteTypeText(t *testing.T) {
	if got := SiteTypeText("worker"); got != "Background worker" {
		t.Errorf("SiteTypeText(worker) = %q", got)
	}
	if got := SiteTypeText("other"); got != "other" {
		t.Errorf("SiteTypeText(other) = %q", got)
	}
}
