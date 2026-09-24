package model

import (
	"strings"
	"testing"
)

func TestIPBanDefaults(t *testing.T) {
	var s IPBanSettings // settings saved before IP banning existed
	s.ApplyDefaults()
	if s.Enabled || s.AuthFailures.Threshold != 10 || s.NotFound.WindowSec != 60 || s.BanMinutes != 15 || s.IPv6Prefix != 64 || len(s.TrapPaths) == 0 {
		t.Fatalf("defaults = %+v", s)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	// A saved configuration keeps its choices: a rule off stays off, an
	// emptied trap list stays empty.
	s = IPBanSettings{Enabled: true, TrapPaths: []string{}}
	s.ApplyDefaults()
	if s.AuthFailures.Threshold != 0 || len(s.TrapPaths) != 0 || s.AuthFailures.WindowSec != 300 || s.MaxBanMinutes != 1440 {
		t.Fatalf("partial = %+v", s)
	}
}

func TestIPBanValidation(t *testing.T) {
	cases := map[string]func(*IPBanSettings){
		"ipBan.authFailures.threshold": func(s *IPBanSettings) { s.AuthFailures.Threshold = 1e6 },
		"ipBan.notFound.windowSec":     func(s *IPBanSettings) { s.NotFound.WindowSec = 90000 },
		"ipBan.trapPaths[1]":           func(s *IPBanSettings) { s.TrapPaths = []string{"/ok", "wp-login.php"} },
		"ipBan.maxBanMinutes":          func(s *IPBanSettings) { s.BanMinutes, s.MaxBanMinutes = 60, 30 },
		"ipBan.allowList[0]":           func(s *IPBanSettings) { s.AllowList = []string{"office"} },
		"ipBan.ipv6Prefix":             func(s *IPBanSettings) { s.IPv6Prefix = 16 },
	}
	for field, mutate := range cases {
		s := DefaultIPBan()
		mutate(&s)
		s.ApplyDefaults()
		err := s.Validate()
		ve, ok := err.(*ValidationError)
		if !ok || !strings.HasPrefix(ve.Field, field) {
			t.Errorf("%s: got %v", field, err)
		}
	}
}
