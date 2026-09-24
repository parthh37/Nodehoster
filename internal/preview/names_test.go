package preview

import (
	"strings"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
)

func TestSlug(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"main":                                   "main",
		"Feature/Login_Page":                     "feature-login-page",
		"fix--double//slash":                     "fix-double-slash",
		"-leading-and-trailing":                  "leading-and-trailing",
		"release/1.2.3":                          "release-1-2-3",
		"dependabot/npm_and_yarn/lodash-4.17.21": "dependabot-npm-and-yarn-lodash-4-17-21",
	} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
	// Nothing usable: a stable, distinct name.
	a, b := Slug("///"), Slug("___")
	if !strings.HasPrefix(a, "branch-") || a == b || a != Slug("///") {
		t.Errorf("Slug of unusable names = %q, %q", a, b)
	}
}

func TestLabel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		pattern, kind string
		number        int
		branch, want  string
	}{
		{"pr-{number}", model.PreviewPR, 42, "fix/login", "pr-42"},
		{"{branch}", model.PreviewPR, 42, "fix/login", "fix-login"},
		{"pr-{number}-{branch}", model.PreviewPR, 7, "Fix/Login", "pr-7-fix-login"},
		// A branch preview: {number} is dropped with the dash it leaves...
		{"pr-{number}-{branch}", model.PreviewBranch, 0, "feature/x", "pr-feature-x"},
		{"{branch}", model.PreviewBranch, 0, "feature/x", "feature-x"},
		// ...and a pattern without {branch} takes the branch as the label.
		{"pr-{number}", model.PreviewBranch, 0, "feature/x", "feature-x"},
	}
	for _, c := range cases {
		if got := Label(c.pattern, c.kind, c.number, c.branch); got != c.want {
			t.Errorf("Label(%q, %s %d %q) = %q, want %q", c.pattern, c.kind, c.number, c.branch, got, c.want)
		}
	}
}

func TestLabelLongBranchesStayDistinct(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("very-long-branch-name-", 5)
	a := Label("preview-{branch}", model.PreviewBranch, 0, long+"one")
	b := Label("preview-{branch}", model.PreviewBranch, 0, long+"two")
	if len(a) > 63 || len(b) > 63 {
		t.Fatalf("labels longer than 63: %d %d", len(a), len(b))
	}
	if a == b {
		t.Fatalf("two long branches got the same label %q", a)
	}
	if !strings.HasPrefix(a, "preview-very-long") || strings.HasSuffix(a, "-") {
		t.Errorf("label = %q", a)
	}
	if a != Label("preview-{branch}", model.PreviewBranch, 0, long+"one") {
		t.Error("the label is not stable")
	}
}

func TestHostCollisionSafe(t *testing.T) {
	t.Parallel()
	const pattern = "{branch}.preview.example.com"
	free := Host(pattern, model.PreviewBranch, 0, "feature/a", nil)
	if free != "feature-a.preview.example.com" {
		t.Fatalf("Host = %q", free)
	}
	// feature-a and feature/a slug the same: the second gets a suffix.
	taken := func(h string) bool { return h == free }
	other := Host(pattern, model.PreviewBranch, 0, "feature-a", taken)
	if other == free || !strings.HasPrefix(other, "feature-a-") || !strings.HasSuffix(other, ".preview.example.com") {
		t.Fatalf("colliding host = %q", other)
	}
	if again := Host(pattern, model.PreviewBranch, 0, "feature-a", taken); again != other {
		t.Errorf("collision suffix not stable: %q then %q", other, again)
	}
	long := Host(pattern, model.PreviewBranch, 0, strings.Repeat("x", 80), func(string) bool { return true })
	label, _, _ := strings.Cut(long, ".")
	if len(label) > 63 {
		t.Errorf("label of a taken long host is %d characters", len(label))
	}
}

func TestSiteName(t *testing.T) {
	t.Parallel()
	if got := SiteName("shop", "pr-42"); got != "shop pr-42" {
		t.Errorf("SiteName = %q", got)
	}
	got := SiteName(strings.Repeat("a", 64), "pr-42")
	if len(got) > 64 || !strings.HasSuffix(got, " pr-42") {
		t.Errorf("long SiteName = %q (%d)", got, len(got))
	}
	u := Unique(got, "pr:42")
	if len(u) > 64 || u == got {
		t.Errorf("Unique = %q", u)
	}
}

func TestURL(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		proto string
		port  int
		want  string
	}{
		{"http", 80, "http://h.example"},
		{"https", 443, "https://h.example"},
		{"http", 8080, "http://h.example:8080"},
		{"https", 80, "https://h.example:80"},
	} {
		if got := URL(c.proto, "h.example", c.port); got != c.want {
			t.Errorf("URL(%s, %d) = %q, want %q", c.proto, c.port, got, c.want)
		}
	}
}

func TestMatchBranch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		patterns []string
		branch   string
		want     bool
	}{
		{[]string{"feature/*"}, "feature/login", true},
		{[]string{"feature/*"}, "feature/a/b", false},
		{[]string{"feature/**"}, "feature/a/b", true},
		{[]string{"feature/**"}, "feature", false},
		{[]string{"release-?"}, "release-1", true},
		{[]string{"release-?"}, "release-10", false},
		{[]string{"*"}, "develop", true},
		{[]string{"*"}, "a/b", false},
		{[]string{"**"}, "a/b", true},
		{[]string{"dev", "staging"}, "staging", true},
		{nil, "anything", false},
		{[]string{"hotfix-*-x"}, "hotfix-12-x", true},
	}
	for _, c := range cases {
		if got := MatchBranch(c.patterns, c.branch); got != c.want {
			t.Errorf("MatchBranch(%q, %q) = %v, want %v", c.patterns, c.branch, got, c.want)
		}
	}
}

func TestValidRef(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{"main", "feature/login", "release-1.2", "user+fix", "refs/pull/42/head", "dependabot/npm_and_yarn/x-1.0"} {
		if !ValidRef(ok) {
			t.Errorf("ValidRef(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "-upload-pack=evil", "--help", "a..b", "a//b", "a/", "a.lock", "has space", "semi;colon",
		"back`tick", "$(x)", "a\nb", "/abs", ".hidden", "a/.b", "ref@{1}", strings.Repeat("x", 300)} {
		if ValidRef(bad) {
			t.Errorf("ValidRef(%q) = true", bad)
		}
	}
}
