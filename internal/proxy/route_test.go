package proxy

import (
	"net"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
)

func TestHostMatches(t *testing.T) {
	cases := []struct {
		pattern, host string
		want          bool
	}{
		{"", "anything.com", true},
		{"example.com", "example.com", true},
		{"example.com", "www.example.com", false},
		{"*.example.com", "www.example.com", true},
		{"*.example.com", "a.b.example.com", false},
		{"*.example.com", "example.com", false},
	}
	for _, c := range cases {
		if got := hostMatches(c.pattern, c.host); got != c.want {
			t.Errorf("hostMatches(%q, %q) = %v", c.pattern, c.host, got)
		}
	}
}

// TestPrecedence follows IIS: specific IP over all addresses, exact host
// over wildcard host over empty host.
func TestPrecedence(t *testing.T) {
	mk := func(name, ip, host string) *route {
		r := &route{host: host, site: &siteRuntime{site: &model.Site{Name: name}}}
		if ip != "" {
			r.ip = net.ParseIP(ip)
		}
		return r
	}
	routes := []*route{
		mk("catchall", "", ""),
		mk("wildcard", "", "*.example.com"),
		mk("exact", "", "www.example.com"),
		mk("ip-specific", "10.0.0.5", "www.example.com"),
	}
	sortRoutes(routes)
	tbl := &routeTable{byPort: map[int][]*route{80: routes}}
	check := func(local, host, want string) {
		t.Helper()
		r := tbl.match(80, net.ParseIP(local), host)
		if r == nil || r.site.site.Name != want {
			got := "<nil>"
			if r != nil {
				got = r.site.site.Name
			}
			t.Errorf("match(%s, %s) = %s, want %s", local, host, got, want)
		}
	}
	check("10.0.0.5", "www.example.com", "ip-specific")
	check("10.0.0.6", "www.example.com", "exact")
	check("10.0.0.6", "api.example.com", "wildcard")
	check("10.0.0.6", "other.org", "catchall")
	check("10.0.0.6", "WWW.EXAMPLE.COM.", "exact")
}
