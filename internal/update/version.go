// Package update finds, verifies and installs new releases of NodeHoster
// from the release feed (latest.json, which CI publishes next to the
// release files and signs). The service checks the feed and downloads the
// setup; a copy of nodehoster.exe then runs the setup unattended, since
// setup stops the service and replaces the program that started it.
package update

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a release version, X.Y.Z with an optional -suffix.
type Version struct {
	Major, Minor, Patch int
	Pre                 string // "rc1" in 1.2.0-rc1; "" for a release
}

// ParseVersion reads "1.2.3", "v1.2.3" or "1.2.3-rc1".
func ParseVersion(s string) (Version, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	core, pre, _ := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, false
	}
	var n [3]int
	for i, p := range parts {
		if p == "" || len(p) > 6 || strings.TrimLeft(p, "0123456789") != "" {
			return Version{}, false
		}
		n[i], _ = strconv.Atoi(p)
	}
	return Version{Major: n[0], Minor: n[1], Patch: n[2], Pre: pre}, true
}

func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// IsRelease reports whether v is a release rather than a pre-release or
// development build (0.0.0-dev.12).
func (v Version) IsRelease() bool { return v.Pre == "" }

// Compare is <0, 0 or >0. A pre-release sorts before its release.
func (v Version) Compare(o Version) int {
	for _, d := range [...]int{v.Major - o.Major, v.Minor - o.Minor, v.Patch - o.Patch} {
		if d != 0 {
			return d
		}
	}
	switch {
	case v.Pre == o.Pre:
		return 0
	case v.Pre == "":
		return 1
	case o.Pre == "":
		return -1
	}
	return strings.Compare(v.Pre, o.Pre)
}
