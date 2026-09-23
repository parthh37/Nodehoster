package mail

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
)

func checksByName(cs []model.MailCheck) map[string]model.MailCheck {
	m := map[string]model.MailCheck{}
	for _, c := range cs {
		m[c.Name] = c
	}
	return m
}

func TestHealth(t *testing.T) {
	key, err := GenerateDKIMKey()
	if err != nil {
		t.Fatal(err)
	}
	record, _ := DKIMRecord(key)
	other, _ := GenerateDKIMKey()
	otherRecord, _ := DKIMRecord(other)
	// Long records come back split into strings with spaces in between.
	split := record[:60] + " " + record[60:]

	dns := fakeDNS{
		mx: map[string][]*net.MX{
			"gmail.com": {{Host: "gmail-smtp-in.l.google.com.", Pref: 5}},
			"good.test": {{Host: "mx.good.test.", Pref: 10}},
		},
		hosts: map[string][]string{
			"mail.good.test": {"203.0.113.7"},
			// 203.0.113.7 is listed on one list; another refuses to answer.
			"7.113.0.203.bl.spamcop.net":   {"127.0.0.2"},
			"7.113.0.203.zen.spamhaus.org": {"127.255.255.254"},
		},
		txt: map[string][]string{
			"good.test":               {"v=spf1 ip4:203.0.113.0/24 -all", "google-site-verification=x"},
			"nh._domainkey.good.test": {split},
			"_dmarc.good.test":        {"v=DMARC1; p=quarantine; rua=mailto:d@good.test"},
			"bad.test":                {"v=spf1 include:_spf.example.net -all"},
			"_spf.example.net":        {"v=spf1 ip4:198.51.100.1 -all"},
			"old._domainkey.bad.test": {otherRecord},
			"two.test":                {"v=spf1 -all", "v=spf1 mx -all"},
		},
		ptr: map[string][]string{"203.0.113.7": {"mail.good.test."}},
	}
	rem := newRemote(t)
	h := newHarness(t, nil, func(o *Options) {
		o.Resolver = dns
		o.LocalIP = func() net.IP { return net.ParseIP("10.0.0.5") }
		o.Dial = func(ctx context.Context, addr string) (net.Conn, error) {
			if addr != "gmail-smtp-in.l.google.com:25" {
				return nil, errors.New("unexpected dial " + addr)
			}
			return net.Dial("tcp", rem.addr)
		}
	})
	h.set(func(c *model.MailSettings) {
		c.Hostname = "mail.good.test"
		c.DKIM = []model.DKIMKey{
			{Domain: "good.test", Selector: "nh", Enabled: true, PrivateKey: key},
			{Domain: "bad.test", Selector: "old", Enabled: true, PrivateKey: key},
		}
	})

	// Behind NAT, without a configured public address.
	r := h.s.Health(context.Background(), nil)
	srv := checksByName(r.Server)
	if c := srv["Public address"]; c.Status != model.CheckWarn || !strings.Contains(c.Detail, "10.0.0.5 is private") {
		t.Errorf("NAT: %+v", c)
	}
	if c := srv["Reverse DNS (PTR)"]; c.Status != model.CheckInfo {
		t.Errorf("PTR without an address: %+v", c)
	}

	h.set(func(c *model.MailSettings) { c.PublicIP = "203.0.113.7" })
	r = h.s.Health(context.Background(), []string{"two.test", "Good.test."})
	srv = checksByName(r.Server)
	for name, want := range map[string]string{
		"Host name (HELO)":  model.CheckPass,
		"Reverse DNS (PTR)": model.CheckPass,
		"Outbound port 25":  model.CheckPass,
		"Blacklists":        model.CheckFail,
	} {
		if srv[name].Status != want {
			t.Errorf("%s = %+v, want %s", name, srv[name], want)
		}
	}
	if d := srv["Blacklists"].Detail; !strings.Contains(d, "bl.spamcop.net (127.0.0.2)") || !strings.Contains(d, "Could not check zen.spamhaus.org") {
		t.Errorf("blacklists: %s", d)
	}
	if len(r.Domains) != 3 || r.Domains[0].Domain != "two.test" || r.Domains[1].Domain != "good.test" || r.Domains[2].Domain != "bad.test" {
		t.Fatalf("domains: %+v", r.Domains)
	}

	good := checksByName(r.Domains[1].Checks)
	for _, name := range []string{"SPF", "DKIM (nh)", "DMARC", "MX", "Domain blacklist"} {
		if good[name].Status != model.CheckPass {
			t.Errorf("good.test %s = %+v", name, good[name])
		}
	}

	bad := checksByName(r.Domains[2].Checks)
	if c := bad["SPF"]; c.Status != model.CheckFail || c.Fix != "v=spf1 ip4:203.0.113.7 include:_spf.example.net -all" || c.FixDNS != "bad.test" {
		t.Errorf("bad SPF: %+v", c)
	}
	if c := bad["DKIM (old)"]; c.Status != model.CheckFail || !strings.Contains(c.Detail, "different key") || c.Fix != record {
		t.Errorf("bad DKIM: %+v", c)
	}
	if c := bad["DMARC"]; c.Status != model.CheckWarn || c.Fix != "v=DMARC1; p=none; rua=mailto:postmaster@bad.test" || c.FixDNS != "_dmarc.bad.test" {
		t.Errorf("no DMARC: %+v", c)
	}
	if c := bad["MX"]; c.Status != model.CheckWarn {
		t.Errorf("no MX: %+v", c)
	}
	two := checksByName(r.Domains[0].Checks)
	if two["SPF"].Status != model.CheckFail || !strings.Contains(two["SPF"].Detail, "more than one") {
		t.Errorf("two SPF records: %+v", two["SPF"])
	}
	if c := two["DKIM"]; c.Status != model.CheckWarn || !strings.Contains(c.Detail, "not signed") {
		t.Errorf("unsigned domain: %+v", c)
	}

	// A HELO name that is not a domain, and port 25 blocked.
	h.set(func(c *model.MailSettings) { c.Hostname = "WIN-SERVER" })
	h.s.opt.Dial = func(context.Context, string) (net.Conn, error) { return nil, errors.New("i/o timeout") }
	srv = checksByName(h.s.Health(context.Background(), nil).Server)
	if srv["Host name (HELO)"].Status != model.CheckFail || srv["Outbound port 25"].Status != model.CheckFail ||
		srv["Reverse DNS (PTR)"].Status != model.CheckWarn {
		t.Errorf("bad server: %+v", srv)
	}

	// Through a smart host, this server's reputation does not matter.
	h.set(func(c *model.MailSettings) {
		c.Delivery = "smarthost"
		c.SmartHost = model.MailSmartHost{Host: "smtp.sendgrid.net", Port: 587, Security: "starttls"}
	})
	r = h.s.Health(context.Background(), nil)
	if _, ok := checksByName(r.Server)["Reverse DNS (PTR)"]; ok || checksByName(r.Server)["Delivery"].Status != model.CheckInfo {
		t.Errorf("smart host server checks: %+v", r.Server)
	}
	if c := checksByName(r.Domains[0].Checks)["SPF"]; c.Status != model.CheckInfo {
		t.Errorf("smart host SPF: %+v", c)
	}
}

func TestAddToSPF(t *testing.T) {
	if got := addToSPF("v=spf1 mx ~all", net.ParseIP("2001:db8::1")); got != "v=spf1 ip6:2001:db8::1 mx ~all" {
		t.Error(got)
	}
	if dkimKeyOf("v=DKIM1; k=rsa; p=AB CD ") != "ABCD" {
		t.Error("dkimKeyOf")
	}
}
