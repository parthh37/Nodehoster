package proxy

import (
	"net/http/httptest"
	"testing"
)

// Basic authentication exclusions take protection away, so a path is only
// excluded when it is under the prefix however an application reads it.
func TestBasicAuthExclusionPaths(t *testing.T) {
	prefixes := []string{"/public", "/assets/"}
	for _, tc := range []struct {
		target string
		want   bool
	}{
		{"/public", true},
		{"/public/", true},
		{"/public/logo.png", true},
		{"/PUBLIC/logo.png", true},
		{"//public/logo.png", true},
		{"/assets", true},
		{"/assets/app.js", true},

		{"/publicity", false},
		{"/public/../admin/x", false},
		{"/public/./../admin", false},
		{"/admin/../public/x", false}, // under /admin as sent
		{"/public/%2e%2e/admin/x", false},
		{"/public%2F..%2Fadmin", false},
		{"/public/..%5Cadmin", false},
		{"/admin", false},
		{"/", false},
	} {
		r := httptest.NewRequest("GET", "http://example.com"+tc.target, nil)
		if got := excluded(r.URL.Path, r.URL.RawPath, prefixes); got != tc.want {
			t.Errorf("excluded(%q) = %v, want %v", tc.target, got, tc.want)
		}
	}
	if excluded("/anything", "", []string{"/a\\b"}) {
		t.Error("an unreadable exclusion must exclude nothing")
	}
}

// Bypassing the response cache is the safe side: any form of the path
// under a prefix bypasses it.
func TestCacheBypassPaths(t *testing.T) {
	c := &responseCache{bypass: []string{"/api/"}}
	for _, tc := range []struct {
		target string
		want   bool
	}{
		{"/api/users", true},
		{"/API/users", true},
		{"/x/../api/users", true},
		{"//api/users", true},
		{"/api%2Fusers", true}, // unreadable: not cached
		{"/apis", false},
		{"/home", false},
	} {
		r := httptest.NewRequest("GET", "http://example.com"+tc.target, nil)
		if got := c.bypassed(r); got != tc.want {
			t.Errorf("bypassed(%q) = %v, want %v", tc.target, got, tc.want)
		}
	}
}
