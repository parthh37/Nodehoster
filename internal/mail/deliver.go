package mail

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
)

// deliveryError is how a delivery attempt failed for a recipient.
type deliveryError struct {
	permanent bool // 5xx, or no mail server: do not retry
	msg       string
}

func (e *deliveryError) Error() string { return e.msg }

func temporary(format string, a ...any) *deliveryError {
	return &deliveryError{msg: fmt.Sprintf(format, a...)}
}

// classify turns an SMTP reply error into a delivery error.
func classify(err error, what string) *deliveryError {
	var te *textproto.Error
	if errors.As(err, &te) {
		return &deliveryError{permanent: te.Code >= 500, msg: fmt.Sprintf("%s: %d %s", what, te.Code, te.Msg)}
	}
	return temporary("%s: %v", what, err)
}

// attempt tries to deliver a message to its pending recipients once and
// records the outcome.
func (s *Server) attempt(ctx context.Context, m model.MailMessage) {
	cfg := s.config()
	now := time.Now()
	m.Attempts++
	m.LastAttempt = &now

	data, err := s.q.content(m.ID)
	if err == nil {
		data = s.sign(data, m.From, cfg)
	}
	results := map[string]*deliveryError{} // recipient -> nil when delivered
	if err != nil {
		for _, r := range m.Recipients {
			results[r.Address] = temporary("reading the message: %v", err)
		}
	} else {
		for _, g := range s.groups(m, cfg) {
			for rcpt, e := range s.deliverGroup(ctx, cfg, m.From, g, data) {
				results[rcpt] = e
			}
		}
	}

	expired := time.Since(m.ReceivedAt) > time.Duration(cfg.ExpireHours)*time.Hour
	m.LastError = ""
	pending := 0
	for i := range m.Recipients {
		r := &m.Recipients[i]
		e, tried := results[r.Address]
		if r.State != model.RcptPending || !tried {
			continue
		}
		switch {
		case e == nil:
			r.State, r.Error, r.DeliveredAt = model.RcptDelivered, "", &now
			s.delivered.Add(1)
			s.logf("delivered %s to <%s>", m.ID, r.Address)
		case e.permanent || expired:
			r.State, r.Error = model.RcptFailed, e.msg
			if !e.permanent {
				r.Error = "gave up after " + strconv.Itoa(cfg.ExpireHours) + " hours: " + e.msg
			}
			s.bounced.Add(1)
			m.LastError = r.Error
			s.logf("failed %s to <%s>: %s", m.ID, r.Address, r.Error)
		default:
			r.Error = e.msg
			m.LastError = e.msg
			pending++
			s.logf("deferred %s to <%s>: %s", m.ID, r.Address, e.msg)
		}
	}
	if pending > 0 {
		next := now.Add(retryDelay(m.Attempts))
		m.NextAttempt = &next
	} else {
		m.NextAttempt = nil
	}
	failed, err := s.q.finish(m)
	if err != nil {
		s.opt.Log.Error("mail queue", "id", m.ID, "err", err)
	}
	if failed {
		var to []string
		for _, r := range m.Recipients {
			if r.State == model.RcptFailed {
				to = append(to, r.Address)
			}
		}
		s.opt.Bus.Warn(events.MailFailed, "", "Mail from %s to %s could not be delivered: %s", orNull(m.From), strings.Join(to, ", "), m.LastError)
	}
}

func orNull(from string) string {
	if from == "" {
		return "<>"
	}
	return from
}

// groups splits the pending recipients into one delivery per mail server:
// by domain for direct delivery, all together through a smart host.
func (s *Server) groups(m model.MailMessage, cfg model.MailSettings) [][]string {
	byKey := map[string][]string{}
	var keys []string
	for _, r := range m.Recipients {
		if r.State != model.RcptPending {
			continue
		}
		key := ""
		if cfg.Delivery == "direct" {
			_, key, _ = strings.Cut(strings.ToLower(r.Address), "@")
		}
		if _, ok := byKey[key]; !ok {
			keys = append(keys, key)
		}
		byKey[key] = append(byKey[key], r.Address)
	}
	out := make([][]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, byKey[k])
	}
	return out
}

// sign adds a DKIM signature when an enabled key belongs to the sender's
// domain. Signing problems never hold mail back; they are logged.
func (s *Server) sign(data []byte, from string, cfg model.MailSettings) []byte {
	// Receivers align DKIM with the From header, so sign for its domain
	// and fall back to the envelope sender's.
	domain := headerDomain(data)
	if domain == "" {
		_, domain, _ = strings.Cut(strings.ToLower(from), "@")
	}
	for _, k := range cfg.DKIM {
		if !k.Enabled || k.Domain != domain {
			continue
		}
		key, err := ParseDKIMKey(s.opt.Unseal(k.PrivateKey))
		if err != nil {
			s.opt.Log.Warn("DKIM key unusable", "domain", k.Domain, "err", err)
			return data
		}
		signed, err := signDKIM(data, k.Domain, k.Selector, key)
		if err != nil {
			s.opt.Log.Warn("DKIM signing failed", "domain", k.Domain, "err", err)
			return data
		}
		return signed
	}
	return data
}

func headerDomain(data []byte) string {
	hdr, err := textproto.NewReader(bufio.NewReader(bytes.NewReader(data))).ReadMIMEHeader()
	if err != nil && len(hdr) == 0 {
		return ""
	}
	a, err := mail.ParseAddress(hdr.Get("From"))
	if err != nil {
		return ""
	}
	_, d, _ := strings.Cut(strings.ToLower(a.Address), "@")
	return d
}

// deliverGroup sends the message to recipients that share a mail server
// and returns the result for each.
func (s *Server) deliverGroup(ctx context.Context, cfg model.MailSettings, from string, rcpts []string, data []byte) map[string]*deliveryError {
	all := func(e *deliveryError) map[string]*deliveryError {
		out := map[string]*deliveryError{}
		for _, r := range rcpts {
			out[r] = e
		}
		return out
	}
	if cfg.Delivery == "smarthost" {
		h := cfg.SmartHost
		c, err := s.connect(ctx, net.JoinHostPort(h.Host, strconv.Itoa(h.Port)), h.Host, cfg, h.Security, h.InsecureSkipVerify)
		if err != nil {
			return all(temporary("smart host %s: %v", h.Host, err))
		}
		defer c.Close()
		if h.Username != "" {
			if err := c.Auth(s.clientAuth(c, h.Username, s.opt.Unseal(h.Password), h.Host)); err != nil {
				return all(classify(err, "smart host login"))
			}
		}
		return send(c, from, rcpts, data)
	}

	_, domain, _ := strings.Cut(rcpts[0], "@")
	hosts, derr := s.mailHosts(ctx, domain)
	if derr != nil {
		return all(derr)
	}
	var last *deliveryError
	for _, host := range hosts {
		c, err := s.connect(ctx, net.JoinHostPort(host, "25"), host, cfg, "opportunistic", true)
		if err != nil {
			last = temporary("%s: %v", host, err)
			continue // try the next mail server
		}
		res := send(c, from, rcpts, data)
		c.Close()
		return res
	}
	if last == nil {
		last = temporary("no mail server for %s answered", domain)
	}
	return all(last)
}

// mailHosts returns the domain's mail servers in preference order: its MX
// records, or the domain itself when it has none (RFC 5321 §5.1).
func (s *Server) mailHosts(ctx context.Context, domain string) ([]string, *deliveryError) {
	r := s.resolver()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	mxs, err := r.LookupMX(ctx, domain)
	var dnsErr *net.DNSError
	if err != nil && !(errors.As(err, &dnsErr) && dnsErr.IsNotFound) {
		return nil, temporary("looking up the mail servers of %s: %v", domain, err)
	}
	slices.SortStableFunc(mxs, func(a, b *net.MX) int { return int(a.Pref) - int(b.Pref) })
	var hosts []string
	for _, mx := range mxs {
		h := strings.TrimSuffix(mx.Host, ".")
		if h == "" {
			// RFC 7505 null MX: the domain accepts no mail.
			return nil, &deliveryError{permanent: true, msg: domain + " does not accept mail (null MX)"}
		}
		hosts = append(hosts, h)
	}
	if len(hosts) == 0 {
		if _, err := r.LookupHost(ctx, domain); err != nil {
			if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
				return nil, &deliveryError{permanent: true, msg: "the domain " + domain + " does not exist"}
			}
			return nil, temporary("looking up %s: %v", domain, err)
		}
		hosts = []string{domain}
	}
	return hosts, nil
}

// connect opens an SMTP session. security is tls (implicit TLS),
// starttls (required), none, or opportunistic: STARTTLS when the server
// offers it, without certificate checks, as mail servers do between
// themselves (RFC 7435); a message is never refused for want of TLS.
func (s *Server) connect(ctx context.Context, addr, host string, cfg model.MailSettings, security string, insecure bool) (*smtp.Client, error) {
	dial := s.opt.Dial
	if dial == nil {
		d := &net.Dialer{Timeout: 30 * time.Second}
		dial = func(ctx context.Context, addr string) (net.Conn, error) { return d.DialContext(ctx, "tcp", addr) }
	}
	tlsConf := &tls.Config{ServerName: host, InsecureSkipVerify: insecure, MinVersion: tls.VersionTLS12}
	conn, err := dial(ctx, addr)
	if err != nil {
		return nil, err
	}
	// The whole session, including a large DATA, must finish in time.
	conn.SetDeadline(time.Now().Add(10 * time.Minute))
	if security == "tls" {
		tc := tls.Client(conn, tlsConf)
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, err
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if err := c.Hello(hostnameFor(cfg)); err != nil {
		c.Close()
		return nil, err
	}
	if security == "starttls" || security == "opportunistic" {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsConf); err != nil {
				c.Close()
				if security == "starttls" {
					return nil, err
				}
				// A broken TLS setup on the other side: send in the clear.
				return s.connect(ctx, addr, host, cfg, "none", insecure)
			}
		} else if security == "starttls" {
			c.Close()
			return nil, errors.New("the server does not offer STARTTLS")
		}
	}
	return c, nil
}

// clientAuth picks PLAIN, or LOGIN for servers that offer only that
// (Microsoft 365, some older relays).
func (s *Server) clientAuth(c *smtp.Client, user, pass, host string) smtp.Auth {
	if ok, mechs := c.Extension("AUTH"); ok && !strings.Contains(" "+strings.ToUpper(mechs)+" ", " PLAIN ") &&
		strings.Contains(" "+strings.ToUpper(mechs)+" ", " LOGIN ") {
		return &loginAuth{user: user, pass: pass}
	}
	return smtp.PlainAuth("", user, pass, host)
}

type loginAuth struct{ user, pass string }

func (a *loginAuth) Start(si *smtp.ServerInfo) (string, []byte, error) {
	if !si.TLS {
		return "", nil, errors.New("refusing to send a password without TLS")
	}
	return "LOGIN", nil, nil
}

func (a *loginAuth) Next(from []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSuffix(string(from), ":")) {
	case "username", "user name":
		return []byte(a.user), nil
	case "password":
		return []byte(a.pass), nil
	}
	return nil, fmt.Errorf("unexpected LOGIN challenge %q", from)
}

// send runs one transaction and reports per recipient.
func send(c *smtp.Client, from string, rcpts []string, data []byte) map[string]*deliveryError {
	out := map[string]*deliveryError{}
	fail := func(e *deliveryError, who []string) map[string]*deliveryError {
		for _, r := range who {
			out[r] = e
		}
		return out
	}
	if err := c.Mail(from); err != nil {
		return fail(classify(err, "sender refused"), rcpts)
	}
	var accepted []string
	for _, r := range rcpts {
		if err := c.Rcpt(r); err != nil {
			out[r] = classify(err, "recipient refused")
			continue
		}
		accepted = append(accepted, r)
	}
	if len(accepted) == 0 {
		c.Reset()
		c.Quit()
		return out
	}
	w, err := c.Data()
	if err != nil {
		return fail(classify(err, "DATA refused"), accepted)
	}
	if _, err := w.Write(data); err != nil {
		return fail(temporary("sending the message: %v", err), accepted)
	}
	if err := w.Close(); err != nil {
		return fail(classify(err, "message refused"), accepted)
	}
	c.Quit()
	return fail(nil, accepted)
}

// pickupEnvelope reads the envelope of a pickup file: X-Sender and
// X-Receiver headers (the IIS format, removed before sending) or else
// From, To, Cc and Bcc (Bcc removed).
func pickupEnvelope(data []byte) (from string, to []string, body []byte, err error) {
	end := bytes.Index(data, []byte("\r\n\r\n"))
	sep := 4
	if end < 0 {
		end, sep = bytes.Index(data, []byte("\n\n")), 2
	}
	if end < 0 {
		return "", nil, nil, errors.New("no header")
	}
	hdr, err := textproto.NewReader(bufio.NewReader(bytes.NewReader(data[:end+sep]))).ReadMIMEHeader()
	if err != nil {
		return "", nil, nil, fmt.Errorf("bad header: %v", err)
	}
	addrs := func(fields ...string) []string {
		var out []string
		for _, f := range fields {
			for _, v := range hdr.Values(f) {
				list, err := mail.ParseAddressList(v)
				if err != nil {
					continue
				}
				for _, a := range list {
					out = append(out, a.Address)
				}
			}
		}
		return out
	}
	drop := []string{}
	if xs := addrs("X-Sender"); len(xs) > 0 {
		from = xs[0]
		drop = append(drop, "X-Sender")
	} else if f := addrs("From"); len(f) > 0 {
		from = f[0]
	}
	if xr := addrs("X-Receiver"); len(xr) > 0 {
		to = xr
		drop = append(drop, "X-Receiver")
	} else {
		to = addrs("To", "Cc", "Bcc")
		drop = append(drop, "Bcc")
	}
	if len(to) == 0 {
		return "", nil, nil, errors.New("no recipients")
	}
	return from, to, stripHeaders(data, end, drop), nil
}

// stripHeaders removes header fields (and their continuation lines).
func stripHeaders(data []byte, end int, names []string) []byte {
	if len(names) == 0 {
		return data
	}
	var out bytes.Buffer
	skip := false
	for _, line := range bytes.SplitAfter(data[:end], []byte("\n")) {
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			if !skip {
				out.Write(line)
			}
			continue
		}
		name, _, _ := bytes.Cut(line, []byte(":"))
		skip = false
		for _, n := range names {
			if strings.EqualFold(strings.TrimSpace(string(name)), n) {
				skip = true
			}
		}
		if !skip {
			out.Write(line)
		}
	}
	if !bytes.HasSuffix(out.Bytes(), []byte("\n")) && out.Len() > 0 {
		out.WriteString("\r\n")
	}
	out.Write(data[end:])
	return out.Bytes()
}
