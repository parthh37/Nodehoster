package model

import (
	"errors"
	"testing"
)

func TestLogShippingValidate(t *testing.T) {
	t.Parallel()
	syslog := func(addr, transport, facility string) LogTarget {
		return LogTarget{ID: "1", Name: "sys", Type: LogTargetSyslog, Enabled: true, Sources: []string{"server"}, Syslog: &SyslogTarget{Address: addr, Transport: transport, Facility: facility}}
	}
	for _, tc := range []struct {
		name  string
		t     LogTarget
		field string
	}{
		{"syslog defaults", syslog("logs:514", "", ""), ""},
		{"syslog tls", syslog("logs.example.com:6514", "tls", "local7"), ""},
		{"syslog no port", syslog("logs", "udp", ""), "logShipping.targets[0].syslog.address"},
		{"syslog transport", syslog("logs:514", "quic", ""), "logShipping.targets[0].syslog.transport"},
		{"syslog facility", syslog("logs:514", "tcp", "local9"), "logShipping.targets[0].syslog.facility"},
		{"unknown source", LogTarget{ID: "1", Name: "x", Type: "seq", Sources: []string{"db"}, Seq: &SeqTarget{URL: "https://seq"}}, "logShipping.targets[0].sources"},
		{"enabled without sources", LogTarget{ID: "1", Name: "x", Type: "seq", Enabled: true, Seq: &SeqTarget{URL: "https://seq"}}, "logShipping.targets[0].sources"},
		{"disabled without sources", LogTarget{ID: "1", Name: "x", Type: "seq", Seq: &SeqTarget{URL: "https://seq"}}, ""},
		{"seq url", LogTarget{ID: "1", Name: "x", Type: "seq", Seq: &SeqTarget{URL: "seq:5341"}}, "logShipping.targets[0].seq.url"},
		{"bad level", LogTarget{ID: "1", Name: "x", Type: "seq", MinLevel: "loud", Seq: &SeqTarget{URL: "https://seq"}}, "logShipping.targets[0].minLevel"},
		{"http format", LogTarget{ID: "1", Name: "x", Type: "http", HTTP: &HTTPTarget{URL: "https://c", Format: "xml"}}, "logShipping.targets[0].http.format"},
		{"http header", LogTarget{ID: "1", Name: "x", Type: "http", HTTP: &HTTPTarget{URL: "https://c", Headers: []HTTPHeader{{Name: "Bad Name", Value: "v"}}}}, "logShipping.targets[0].http.headers[0].name"},
		{"header injection", LogTarget{ID: "1", Name: "x", Type: "http", HTTP: &HTTPTarget{URL: "https://c", Headers: []HTTPHeader{{Name: "X", Value: "a\r\nB: c"}}}}, "logShipping.targets[0].http.headers[0].value"},
		{"no name", LogTarget{ID: "1", Type: "http", HTTP: &HTTPTarget{URL: "https://c"}}, "logShipping.targets[0].name"},
		{"no type", LogTarget{ID: "1", Name: "x"}, "logShipping.targets[0].type"},
	} {
		s := LogShippingSettings{Targets: []LogTarget{tc.t}}
		err := s.Validate()
		var ve *ValidationError
		switch {
		case tc.field == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.field != "" && (!errors.As(err, &ve) || ve.Field != tc.field):
			t.Errorf("%s: err = %v, want field %s", tc.name, err, tc.field)
		}
	}
	s := LogShippingSettings{Targets: []LogTarget{syslog("logs:514", "", "")}}
	s.Validate()
	if tg := s.Targets[0]; tg.Syslog.Transport != "udp" || tg.Syslog.Facility != "local0" || tg.MinLevel != "info" || tg.SiteIDs == nil {
		t.Errorf("defaults = %+v %+v", tg, tg.Syslog)
	}
}
