package mail

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"blitiri.com.ar/go/spf"
	"github.com/parthh37/nodehoster/internal/model"
)

// ipBlacklists are the DNS blocklists receivers most often consult for
// the sending address; domainBlacklists for the domain in From.
var (
	ipBlacklists = []string{
		"zen.spamhaus.org", "bl.spamcop.net", "b.barracudacentral.org",
		"bl.mailspike.net", "psbl.surriel.com", "dnsbl-1.uceprotect.net",
	}
	domainBlacklists = []string{"dbl.spamhaus.org"}
)

// portProbeDomain is whose mail servers the outbound port 25 test
// connects to: any large provider will do, and nothing is sent.
const portProbeDomain = "gmail.com"

// Health checks whether mail from this server will be accepted: the
// server's address, reverse DNS, host name, outbound port 25 and
// blacklists, and each sending domain's SPF, DKIM, DMARC and MX records.
// domains adds to the ones configured (DKIM keys and allowed sender
// domains). Nothing is sent: only DNS lookups and, for the port test, a
// connection that is closed after the greeting.
func (s *Server) Health(ctx context.Context, domains []string) model.MailHealth {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	cfg := s.config()
	r := s.resolver()
	h := model.MailHealth{Hostname: hostnameFor(cfg), Delivery: cfg.Delivery, CheckedAt: time.Now()}

	ip, ipCheck := s.publicIP(cfg)
	if ip != nil {
		h.PublicIP = ip.String()
	}
	h.Server = append(h.Server, ipCheck)
	direct := cfg.Delivery == "direct"
	if !direct {
		h.Server = append(h.Server, model.MailCheck{Name: "Delivery", Status: model.CheckInfo,
			Detail: fmt.Sprintf("Mail is handed to the smart host %s, so receivers judge its address and reverse DNS, not this server's. "+
				"Add the provider's SPF include to each domain, as its documentation says.", cfg.SmartHost.Host)})
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	add := func(c ...model.MailCheck) {
		mu.Lock()
		h.Server = append(h.Server, c...)
		mu.Unlock()
	}
	if direct {
		wg.Add(3)
		go func() { defer wg.Done(); add(checkHELO(ctx, r, h.Hostname, ip)) }()
		go func() { defer wg.Done(); add(checkPTR(ctx, r, ip, h.Hostname)) }()
		go func() { defer wg.Done(); add(s.checkPort25(ctx, r, h.Hostname)) }()
		if ip != nil && isPublic(ip) {
			wg.Add(1)
			go func() { defer wg.Done(); add(checkIPBlacklists(ctx, r, ip)) }()
		}
	}

	list := healthDomains(cfg, domains)
	h.Domains = make([]model.MailDomainHealth, len(list))
	for i, d := range list {
		h.Domains[i].Domain = d
		wg.Add(1)
		go func(i int, d string) {
			defer wg.Done()
			h.Domains[i].Checks = s.domainChecks(ctx, r, cfg, d, ip, direct)
		}(i, d)
	}
	wg.Wait()
	order := []string{"Public address", "Delivery", "Host name (HELO)", "Reverse DNS (PTR)", "Outbound port 25", "Blacklists"}
	slices.SortStableFunc(h.Server, func(a, b model.MailCheck) int {
		return slices.Index(order, a.Name) - slices.Index(order, b.Name)
	})
	return h
}

func (s *Server) resolver() Resolver {
	if s.opt.Resolver != nil {
		return s.opt.Resolver
	}
	return net.DefaultResolver
}

// healthDomains are the domains to check: the ones asked for, then those
// with DKIM keys, then the allowed sender domains.
func healthDomains(cfg model.MailSettings, extra []string) []string {
	var out []string
	addD := func(d string) {
		d = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d), "."))
		if d != "" && !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	for _, d := range extra {
		addD(d)
	}
	for _, k := range cfg.DKIM {
		addD(k.Domain)
	}
	for _, d := range cfg.AllowedSenderDomains {
		addD(d)
	}
	return out
}

// publicIP is the configured public address or, failing that, the address
// of the interface that reaches the internet. Behind NAT that one is
// private and the checks that depend on it cannot run.
func (s *Server) publicIP(cfg model.MailSettings) (net.IP, model.MailCheck) {
	c := model.MailCheck{Name: "Public address"}
	if cfg.PublicIP != "" {
		ip := net.ParseIP(cfg.PublicIP)
		c.Status, c.Detail = model.CheckInfo, "Mail is sent from "+cfg.PublicIP+" (as configured)."
		return ip, c
	}
	var ip net.IP
	if s.opt.LocalIP != nil {
		ip = s.opt.LocalIP()
	} else if conn, err := net.Dial("udp", "192.0.2.1:9"); err == nil { // no packet is sent
		ip = conn.LocalAddr().(*net.UDPAddr).IP
		conn.Close()
	}
	switch {
	case ip == nil:
		c.Status, c.Detail = model.CheckWarn, "This server's address could not be determined; set the public address in the SMTP settings."
	case !isPublic(ip):
		c.Status = model.CheckWarn
		c.Detail = fmt.Sprintf("This server's address %s is private: it is behind NAT or a firewall, and receivers see another address. "+
			"Set the public address in the SMTP settings so SPF, reverse DNS and blacklists can be checked.", ip)
		return nil, c
	default:
		c.Status, c.Detail = model.CheckInfo, "Mail is sent from "+ip.String()+" (detected)."
	}
	return ip, c
}

// cgnat is the carrier-grade NAT range (RFC 6598), private in practice.
var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

func isPublic(ip net.IP) bool {
	return !(ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || cgnat.Contains(ip))
}

// checkHELO: the name the server introduces itself with must be a real,
// fully-qualified name that resolves to its address.
func checkHELO(ctx context.Context, r Resolver, host string, ip net.IP) model.MailCheck {
	c := model.MailCheck{Name: "Host name (HELO)", Record: host}
	if !strings.Contains(host, ".") {
		c.Status = model.CheckFail
		c.Detail = fmt.Sprintf("The server introduces itself as %q, which is not a fully-qualified domain name. Many receivers reject that.", host)
		c.Fix = "Set the SMTP host name to a name such as mail.example.com, with an A record pointing at this server."
		return c
	}
	addrs, err := r.LookupIPAddr(ctx, host)
	if err != nil || len(addrs) == 0 {
		c.Status, c.Detail = model.CheckFail, fmt.Sprintf("%s does not resolve to an address.", host)
		if ip != nil {
			c.Fix, c.FixDNS = "A "+ip.String(), host
		}
		return c
	}
	if ip == nil {
		c.Status, c.Detail = model.CheckInfo, fmt.Sprintf("%s resolves to %s; the public address is unknown, so it cannot be compared.", host, joinIPs(addrs))
		return c
	}
	for _, a := range addrs {
		if a.IP.Equal(ip) {
			c.Status, c.Detail = model.CheckPass, fmt.Sprintf("%s resolves to %s.", host, ip)
			return c
		}
	}
	c.Status = model.CheckWarn
	c.Detail = fmt.Sprintf("%s resolves to %s, not to this server's address %s.", host, joinIPs(addrs), ip)
	c.Fix, c.FixDNS = "A "+ip.String(), host
	return c
}

// checkPTR: receivers look up the sending address's reverse DNS and
// expect it to name the server (forward-confirmed); Gmail and Outlook
// reject mail from addresses without one.
func checkPTR(ctx context.Context, r Resolver, ip net.IP, host string) model.MailCheck {
	c := model.MailCheck{Name: "Reverse DNS (PTR)"}
	if ip == nil {
		c.Status, c.Detail = model.CheckInfo, "Not checked: the public address is unknown."
		return c
	}
	names, err := r.LookupAddr(ctx, ip.String())
	if err != nil || len(names) == 0 {
		c.Status = model.CheckFail
		c.Detail = fmt.Sprintf("%s has no reverse DNS name. Gmail, Outlook and most providers reject or junk such mail.", ip)
		c.Fix = fmt.Sprintf("Ask whoever provides the address (hosting or internet provider) to set its reverse DNS (PTR) to %s.", host)
		return c
	}
	c.Record = strings.Join(names, ", ")
	for _, n := range names {
		n = strings.ToLower(strings.TrimSuffix(n, "."))
		addrs, _ := r.LookupIPAddr(ctx, n)
		confirmed := slices.ContainsFunc(addrs, func(a net.IPAddr) bool { return a.IP.Equal(ip) })
		switch {
		case confirmed && n == host:
			c.Status, c.Detail = model.CheckPass, fmt.Sprintf("%s points back to %s, the host name the server uses.", ip, n)
			return c
		case confirmed:
			c.Status = model.CheckWarn
			c.Detail = fmt.Sprintf("%s points back to %s, but the server introduces itself as %s.", ip, n, host)
			c.Fix = fmt.Sprintf("Set the SMTP host name to %s, or change the reverse DNS to %s.", n, host)
			return c
		}
	}
	c.Status = model.CheckWarn
	c.Detail = fmt.Sprintf("%s has reverse DNS %s, but that name does not resolve back to %s.", ip, c.Record, ip)
	c.Fix = fmt.Sprintf("Set the reverse DNS of %s to %s, and an A record for %s pointing at %s.", ip, host, host, ip)
	return c
}

// checkPort25 connects to a large provider's mail server: many cloud and
// home connections block outbound port 25, and then direct delivery
// cannot work at all.
func (s *Server) checkPort25(ctx context.Context, r Resolver, host string) model.MailCheck {
	c := model.MailCheck{Name: "Outbound port 25"}
	mxs, err := r.LookupMX(ctx, portProbeDomain)
	if err != nil || len(mxs) == 0 {
		c.Status, c.Detail = model.CheckWarn, "Not checked: DNS lookups for "+portProbeDomain+" failed."
		return c
	}
	slices.SortFunc(mxs, func(a, b *net.MX) int { return int(a.Pref) - int(b.Pref) })
	target := net.JoinHostPort(strings.TrimSuffix(mxs[0].Host, "."), "25")
	dctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	dial := s.opt.Dial
	if dial == nil {
		d := &net.Dialer{}
		dial = func(ctx context.Context, addr string) (net.Conn, error) { return d.DialContext(ctx, "tcp", addr) }
	}
	conn, err := dial(dctx, target)
	if err != nil {
		c.Status = model.CheckFail
		c.Detail = fmt.Sprintf("Could not connect to %s: %v. Outbound port 25 is probably blocked by the hosting provider or a firewall, so mail cannot be delivered directly.", target, err)
		c.Fix = "Ask the provider to unblock outbound port 25, or deliver through a smart host (SMTP settings → Delivery)."
		return c
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(8 * time.Second))
	greeting, _ := bufio.NewReader(conn).ReadString('\n')
	fmt.Fprintf(conn, "QUIT\r\n")
	if !strings.HasPrefix(greeting, "220") {
		c.Status = model.CheckWarn
		c.Detail = fmt.Sprintf("Connected to %s, but it did not greet as a mail server (%q). A firewall may be intercepting port 25.", target, strings.TrimSpace(greeting))
		return c
	}
	c.Status, c.Detail = model.CheckPass, fmt.Sprintf("Connected to %s: this server can deliver mail directly.", target)
	return c
}

// checkIPBlacklists queries the DNS blocklists for the sending address.
func checkIPBlacklists(ctx context.Context, r Resolver, ip net.IP) model.MailCheck {
	v4 := ip.To4()
	if v4 == nil {
		return model.MailCheck{Name: "Blacklists", Status: model.CheckInfo, Detail: "Not checked: blocklists are only queried for IPv4 addresses."}
	}
	rev := fmt.Sprintf("%d.%d.%d.%d", v4[3], v4[2], v4[1], v4[0])
	c := listCheck(ctx, r, rev, ipBlacklists, ip.String())
	c.Name = "Blacklists"
	return c
}

// listCheck looks name up in each blocklist zone. Answers in 127.255.255.x
// are the lists' way of refusing to answer (for example Spamhaus through a
// public resolver such as 8.8.8.8): unknown, not listed.
func listCheck(ctx context.Context, r Resolver, name string, zones []string, what string) model.MailCheck {
	type res struct {
		zone, answer    string
		listed, unknown bool
	}
	out := make([]res, len(zones))
	var wg sync.WaitGroup
	for i, z := range zones {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lctx, cancel := context.WithTimeout(ctx, 6*time.Second)
			defer cancel()
			addrs, err := r.LookupHost(lctx, name+"."+z)
			out[i].zone = z
			var dnsErr *net.DNSError
			switch {
			case err != nil && errors.As(err, &dnsErr) && dnsErr.IsNotFound:
			case err != nil:
				out[i].unknown, out[i].answer = true, "no answer"
			default:
				out[i].answer = strings.Join(addrs, ", ")
				if strings.HasPrefix(addrs[0], "127.255.255.") {
					out[i].unknown = true
				} else {
					out[i].listed = true
				}
			}
		}()
	}
	wg.Wait()
	var listed, unknown []string
	for _, o := range out {
		switch {
		case o.listed:
			listed = append(listed, fmt.Sprintf("%s (%s)", o.zone, o.answer))
		case o.unknown:
			unknown = append(unknown, o.zone)
		}
	}
	c := model.MailCheck{}
	switch {
	case len(listed) > 0:
		c.Status = model.CheckFail
		c.Detail = fmt.Sprintf("%s is listed on %s. Receivers that use these lists reject or junk its mail.", what, strings.Join(listed, ", "))
		c.Fix = "Find out why (an infected machine, an open relay, a previous owner of the address), fix it, then request removal on each list's website."
	default:
		c.Status = model.CheckPass
		c.Detail = fmt.Sprintf("%s is not listed on %d of %d blocklists checked.", what, len(zones)-len(unknown), len(zones))
	}
	if len(unknown) > 0 {
		c.Detail += " Could not check " + strings.Join(unknown, ", ") + " (the list refuses queries from this server's DNS resolver)."
		if c.Status == model.CheckPass && len(unknown) == len(zones) {
			c.Status = model.CheckInfo
		}
	}
	return c
}

func (s *Server) domainChecks(ctx context.Context, r Resolver, cfg model.MailSettings, domain string, ip net.IP, direct bool) []model.MailCheck {
	out := make([][]model.MailCheck, 5)
	var wg sync.WaitGroup
	run := func(i int, fn func() []model.MailCheck) {
		wg.Add(1)
		go func() { defer wg.Done(); out[i] = fn() }()
	}
	run(0, func() []model.MailCheck {
		return []model.MailCheck{checkSPF(ctx, r, domain, ip, hostnameFor(cfg), direct)}
	})
	run(1, func() []model.MailCheck { return s.checkDKIM(ctx, r, cfg, domain) })
	run(2, func() []model.MailCheck { return []model.MailCheck{checkDMARC(ctx, r, domain)} })
	run(3, func() []model.MailCheck { return []model.MailCheck{checkMX(ctx, r, domain)} })
	run(4, func() []model.MailCheck {
		c := listCheck(ctx, r, domain, domainBlacklists, domain)
		c.Name = "Domain blacklist"
		return []model.MailCheck{c}
	})
	wg.Wait()
	return slices.Concat(out...)
}

// txt returns the TXT records at name that start with prefix, each one's
// strings joined as receivers do.
func txt(ctx context.Context, r Resolver, name, prefix string) ([]string, error) {
	recs, err := r.LookupTXT(ctx, name)
	var dnsErr *net.DNSError
	if err != nil && !(errors.As(err, &dnsErr) && dnsErr.IsNotFound) {
		return nil, err
	}
	var out []string
	for _, t := range recs {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(t)), strings.ToLower(prefix)) {
			out = append(out, strings.TrimSpace(t))
		}
	}
	return out, nil
}

func checkSPF(ctx context.Context, r Resolver, domain string, ip net.IP, helo string, direct bool) model.MailCheck {
	c := model.MailCheck{Name: "SPF", FixDNS: domain}
	suggest := "v=spf1 mx ~all"
	if ip != nil {
		mech := "ip4:"
		if ip.To4() == nil {
			mech = "ip6:"
		}
		suggest = "v=spf1 " + mech + ip.String() + " mx ~all"
	}
	recs, err := txt(ctx, r, domain, "v=spf1")
	switch {
	case err != nil:
		c.Status, c.Detail, c.FixDNS = model.CheckWarn, "The TXT records could not be read: "+err.Error(), ""
		return c
	case len(recs) == 0:
		c.Status = model.CheckFail
		c.Detail = domain + " has no SPF record, so receivers cannot tell which servers may send its mail."
		c.Fix = suggest
		return c
	case len(recs) > 1:
		c.Status, c.Record = model.CheckFail, strings.Join(recs, "\n")
		c.Detail = domain + " has more than one SPF record, which makes SPF fail everywhere (RFC 7208). Merge them into one."
		return c
	}
	c.Record = recs[0]
	if !direct {
		c.Status, c.FixDNS = model.CheckInfo, ""
		c.Detail = "SPF is published. Mail goes through a smart host: make sure the record includes the provider (for example include:sendgrid.net)."
		return c
	}
	if ip == nil {
		c.Status, c.FixDNS = model.CheckInfo, ""
		c.Detail = "SPF is published, but the public address is unknown, so whether it allows this server was not checked."
		return c
	}
	res, err := spf.CheckHostWithSender(ip, helo, "postmaster@"+domain, spf.WithContext(ctx), spf.WithResolver(r))
	switch res {
	case spf.Pass:
		c.Status, c.Detail, c.FixDNS = model.CheckPass, fmt.Sprintf("SPF allows %s to send mail for %s.", ip, domain), ""
	case spf.SoftFail, spf.Neutral:
		c.Status = model.CheckWarn
		c.Detail = fmt.Sprintf("SPF does not allow %s (result: %s), so mail may be marked as spam.", ip, res)
		c.Fix = addToSPF(recs[0], ip)
	case spf.Fail:
		c.Status = model.CheckFail
		c.Detail = fmt.Sprintf("SPF says %s may not send mail for %s: receivers will reject it.", ip, domain)
		c.Fix = addToSPF(recs[0], ip)
	case spf.TempError:
		c.Status, c.Detail, c.FixDNS = model.CheckWarn, fmt.Sprintf("SPF could not be evaluated right now: %v.", err), ""
	default:
		c.Status, c.Detail = model.CheckFail, fmt.Sprintf("The SPF record is broken (%s: %v).", res, err)
		c.Fix = suggest
	}
	return c
}

// addToSPF suggests the record with this server's address added in front
// of the other mechanisms.
func addToSPF(rec string, ip net.IP) string {
	mech := "ip4:"
	if ip.To4() == nil {
		mech = "ip6:"
	}
	f := strings.Fields(rec)
	return strings.Join(slices.Insert(f, 1, mech+ip.String()), " ")
}

func (s *Server) checkDKIM(ctx context.Context, r Resolver, cfg model.MailSettings, domain string) []model.MailCheck {
	var keys []model.DKIMKey
	for _, k := range cfg.DKIM {
		if k.Domain == domain {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return []model.MailCheck{{Name: "DKIM", Status: model.CheckWarn,
			Detail: "Mail from " + domain + " is not signed. Gmail and Yahoo require DKIM or SPF with DMARC, and signed mail reaches the inbox more often.",
			Fix:    "Add a DKIM key for " + domain + " in the SMTP settings, then publish the record it shows."}}
	}
	var out []model.MailCheck
	for _, k := range keys {
		c := model.MailCheck{Name: "DKIM (" + k.Selector + ")", FixDNS: DKIMName(k.Selector, k.Domain)}
		want := k.DNSRecord
		if want == "" && k.PrivateKey != "" {
			want, _ = DKIMRecord(s.opt.Unseal(k.PrivateKey))
		}
		c.Fix = want
		recs, err := txt(ctx, r, c.FixDNS, "")
		var published string
		for _, t := range recs {
			if strings.Contains(t, "p=") {
				published = t
			}
		}
		switch {
		case err != nil:
			c.Status, c.Detail = model.CheckWarn, "The record could not be read: "+err.Error()
		case published == "":
			c.Status = model.CheckFail
			if !k.Enabled {
				c.Status = model.CheckWarn
			}
			c.Detail = "The key is not published: publish the TXT record below, then receivers can verify the signature."
		case dkimKeyOf(published) != dkimKeyOf(want):
			c.Status, c.Record = model.CheckFail, published
			c.Detail = "A different key is published under this name, so signatures fail. Replace the record with the one below."
		case !k.Enabled:
			c.Status, c.Record, c.Fix, c.FixDNS = model.CheckWarn, published, "", ""
			c.Detail = "The key is published but signing is off. Turn it on in the SMTP settings."
		default:
			c.Status, c.Record, c.Fix, c.FixDNS = model.CheckPass, published, "", ""
			c.Detail = "Mail from " + domain + " is signed and the published key matches."
		}
		out = append(out, c)
	}
	return out
}

// dkimKeyOf extracts the p= tag, without the spaces DNS tools add when
// they split long records.
func dkimKeyOf(rec string) string {
	for _, tag := range strings.Split(rec, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(tag), "=")
		if ok && strings.TrimSpace(k) == "p" {
			return strings.Join(strings.Fields(v), "")
		}
	}
	return ""
}

func checkDMARC(ctx context.Context, r Resolver, domain string) model.MailCheck {
	c := model.MailCheck{Name: "DMARC", FixDNS: "_dmarc." + domain}
	recs, err := txt(ctx, r, "_dmarc."+domain, "v=DMARC1")
	switch {
	case err != nil:
		c.Status, c.Detail, c.FixDNS = model.CheckWarn, "The record could not be read: "+err.Error(), ""
		return c
	case len(recs) == 0:
		c.Status = model.CheckWarn
		c.Detail = domain + " has no DMARC policy. Gmail and Yahoo require one from bulk senders, and it lets you see who sends as your domain."
		c.Fix = "v=DMARC1; p=none; rua=mailto:postmaster@" + domain
		return c
	case len(recs) > 1:
		c.Status, c.Record, c.FixDNS = model.CheckFail, strings.Join(recs, "\n"), ""
		c.Detail = "There is more than one DMARC record, so receivers ignore them all. Keep one."
		return c
	}
	c.Record, c.FixDNS = recs[0], ""
	policy := ""
	for _, tag := range strings.Split(recs[0], ";") {
		if k, v, ok := strings.Cut(strings.TrimSpace(tag), "="); ok && strings.TrimSpace(k) == "p" {
			policy = strings.ToLower(strings.TrimSpace(v))
		}
	}
	switch policy {
	case "reject", "quarantine":
		c.Status, c.Detail = model.CheckPass, fmt.Sprintf("DMARC policy %s: mail failing SPF and DKIM for %s is %s.", policy, domain, map[string]string{"reject": "rejected", "quarantine": "sent to spam"}[policy])
	case "none":
		c.Status, c.Detail = model.CheckPass, "DMARC is published in monitoring mode (p=none). Once SPF and DKIM pass, consider p=quarantine."
	default:
		c.Status, c.Detail = model.CheckFail, "The DMARC record has no valid policy (p=none, quarantine or reject)."
	}
	return c
}

func checkMX(ctx context.Context, r Resolver, domain string) model.MailCheck {
	c := model.MailCheck{Name: "MX"}
	mxs, err := r.LookupMX(ctx, domain)
	var dnsErr *net.DNSError
	switch {
	case err != nil && !(errors.As(err, &dnsErr) && dnsErr.IsNotFound):
		c.Status, c.Detail = model.CheckWarn, "The MX records could not be read: "+err.Error()
	case len(mxs) == 0:
		c.Status = model.CheckWarn
		c.Detail = domain + " receives no mail (no MX record): replies and bounces are lost, and some receivers distrust such senders."
	default:
		var hosts []string
		for _, mx := range mxs {
			hosts = append(hosts, strings.TrimSuffix(mx.Host, "."))
		}
		c.Status, c.Record = model.CheckPass, strings.Join(hosts, ", ")
		c.Detail = domain + " can receive replies and bounces."
	}
	return c
}

func joinIPs(addrs []net.IPAddr) string {
	s := make([]string, len(addrs))
	for i, a := range addrs {
		s[i] = a.IP.String()
	}
	return strings.Join(s, ", ")
}
