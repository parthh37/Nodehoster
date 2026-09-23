package mail

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/smtp"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-msgauth/dkim"
	gosmtp "github.com/emersion/go-smtp"
	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// remote is a fake receiving mail server.
type remote struct {
	addr   string
	mu     sync.Mutex
	got    []received
	reject map[string]int // recipient -> reply code
}

type received struct {
	from string
	to   []string
	data string
}

type remoteSession struct {
	r    *remote
	from string
	to   []string
}

func (s *remoteSession) Reset()        { s.from, s.to = "", nil }
func (s *remoteSession) Logout() error { return nil }
func (s *remoteSession) Mail(from string, _ *gosmtp.MailOptions) error {
	s.from = from
	return nil
}
func (s *remoteSession) Rcpt(to string, _ *gosmtp.RcptOptions) error {
	s.r.mu.Lock()
	code := s.r.reject[to]
	s.r.mu.Unlock()
	if code != 0 {
		return &gosmtp.SMTPError{Code: code, Message: "no thanks"}
	}
	s.to = append(s.to, to)
	return nil
}
func (s *remoteSession) Data(r io.Reader) error {
	b, _ := io.ReadAll(r)
	s.r.mu.Lock()
	s.r.got = append(s.r.got, received{s.from, s.to, string(b)})
	s.r.mu.Unlock()
	return nil
}

func newRemote(t *testing.T) *remote {
	r := &remote{reject: map[string]int{}}
	srv := gosmtp.NewServer(gosmtp.BackendFunc(func(c *gosmtp.Conn) (gosmtp.Session, error) { return &remoteSession{r: r}, nil }))
	srv.Domain = "remote.test"
	srv.AllowInsecureAuth = true
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r.addr = ln.Addr().String()
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return r
}

func (r *remote) messages() []received {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]received(nil), r.got...)
}

type harness struct {
	s   *Server
	cfg model.MailSettings
	mu  sync.Mutex
}

func (h *harness) settings() model.MailSettings {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg
}

func (h *harness) set(fn func(*model.MailSettings)) {
	h.mu.Lock()
	fn(&h.cfg)
	cfg := h.cfg
	h.mu.Unlock()
	h.s.Apply(cfg)
}

func newHarness(t *testing.T, rem *remote, opts ...func(*Options)) *harness {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.New(st, log, model.DefaultSettings, func(string) string { return "" })

	h := &harness{cfg: model.DefaultSettings().Mail}
	h.cfg.Enabled = true
	h.cfg.ListenIP, h.cfg.Port = "127.0.0.1", 0
	h.cfg.Hostname = "mail.nodehoster.test"
	if rem != nil {
		host, port, _ := net.SplitHostPort(rem.addr)
		h.cfg.Delivery = "smarthost"
		h.cfg.SmartHost = model.MailSmartHost{Host: host, Security: "none"}
		fmt.Sscan(port, &h.cfg.SmartHost.Port)
	}
	o := Options{
		Dir: filepath.Join(dir, "mail"), LogFile: filepath.Join(dir, "smtp.log"), Log: log, Bus: bus,
		Settings: h.settings, Unseal: func(s string) string { return s },
	}
	for _, fn := range opts {
		fn(&o)
	}
	h.s, err = New(o)
	if err != nil {
		t.Fatal(err)
	}
	h.s.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h.s.Shutdown(ctx)
	})
	if !h.s.Status().Listening {
		t.Fatalf("not listening: %+v", h.s.Status())
	}
	return h
}

func (h *harness) addr() string { return h.s.Status().Addr }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

const msg = "From: App <app@example.com>\r\nTo: a@x.test\r\nSubject: =?utf-8?q?Hall=C3=B6?=\r\n\r\nHello there.\r\n"

func TestRelayThroughSmartHost(t *testing.T) {
	rem := newRemote(t)
	rem.reject["bad@x.test"] = 550
	h := newHarness(t, rem)
	key, err := GenerateDKIMKey()
	if err != nil {
		t.Fatal(err)
	}
	h.set(func(c *model.MailSettings) {
		c.DKIM = []model.DKIMKey{{Domain: "example.com", Selector: "nh", Enabled: true, PrivateKey: key}}
	})

	if err := smtp.SendMail(h.addr(), nil, "app@example.com", []string{"a@x.test", "bad@x.test"}, []byte(msg)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the message to fail for one recipient", func() bool { return len(h.s.Queue(model.MailFailed)) == 1 })

	got := rem.messages()
	if len(got) != 1 || got[0].from != "app@example.com" || strings.Join(got[0].to, ",") != "a@x.test" {
		t.Fatalf("remote received %+v", got)
	}
	data := got[0].data
	for _, want := range []string{"Received: from localhost ([127.0.0.1])", "by mail.nodehoster.test (NodeHoster) with ESMTP id ", "Message-ID: <", "Date: ", "DKIM-Signature: "} {
		if !strings.Contains(data, want) {
			t.Errorf("message lacks %q:\n%s", want, data)
		}
	}
	record, _ := DKIMRecord(key)
	verifications, err := dkim.VerifyWithOptions(strings.NewReader(data), &dkim.VerifyOptions{
		LookupTXT: func(domain string) ([]string, error) {
			if domain == "nh._domainkey.example.com" {
				return []string{record}, nil
			}
			return nil, errors.New("no record")
		},
	})
	if err != nil || len(verifications) != 1 || verifications[0].Err != nil {
		t.Fatalf("DKIM verification: %v %+v", err, verifications)
	}

	m := h.s.Queue("")[0]
	if m.State != model.MailFailed || m.Subject != "Hallö" || m.Source != "smtp" || m.ClientIP != "127.0.0.1" {
		t.Errorf("message: %+v", m)
	}
	if m.Recipients[0].State != model.RcptDelivered || m.Recipients[1].State != model.RcptFailed || !strings.Contains(m.Recipients[1].Error, "550") {
		t.Errorf("recipients: %+v", m.Recipients)
	}
	st := h.s.Status()
	if st.Accepted != 1 || st.Delivered != 1 || st.Bounced != 1 || st.Failed != 1 || st.Queued != 0 {
		t.Errorf("status: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(h.s.q.path("failed"), m.ID+".eml")); err != nil {
		t.Errorf("failed message not in failed/: %v", err)
	}

	// A retry sends only to the recipient that failed.
	rem.mu.Lock()
	delete(rem.reject, "bad@x.test")
	rem.mu.Unlock()
	if err := h.s.Retry(m.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the retried message to be delivered", func() bool { return len(h.s.Queue("")) == 0 })
	if got := rem.messages(); len(got) != 2 || strings.Join(got[1].to, ",") != "bad@x.test" {
		t.Fatalf("retry delivered %+v", got)
	}
}

func TestTemporaryFailureStaysQueued(t *testing.T) {
	rem := newRemote(t)
	rem.reject["later@x.test"] = 451
	h := newHarness(t, rem)
	if _, err := h.s.SendTest("", "later@x.test"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the first attempt", func() bool {
		q := h.s.Queue(model.MailQueued)
		return len(q) == 1 && q[0].Attempts == 1 && q[0].NextAttempt != nil
	})
	m := h.s.Queue("")[0]
	if m.Recipients[0].State != model.RcptPending || !strings.Contains(m.LastError, "451") || m.Source != "test" {
		t.Fatalf("after a temporary failure: %+v", m)
	}

	// Past the expiry time, a temporary failure is final.
	h.set(func(c *model.MailSettings) { c.ExpireHours = 1 })
	h.s.q.mu.Lock()
	h.s.q.msg[m.ID].ReceivedAt = time.Now().Add(-2 * time.Hour)
	h.s.q.mu.Unlock()
	h.s.Retry(m.ID)
	waitFor(t, "the message to expire", func() bool { return len(h.s.Queue(model.MailFailed)) == 1 })
	if e := h.s.Queue("")[0].Recipients[0].Error; !strings.HasPrefix(e, "gave up after 1 hours") {
		t.Errorf("expiry error: %q", e)
	}
}

func TestQueueSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	q, err := openQueue(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := model.MailMessage{ID: "m1", From: "a@b.test", State: model.MailQueued, ReceivedAt: time.Now(),
		Recipients: []model.MailRecipient{{Address: "c@d.test", State: model.RcptPending}}}
	if err := q.add(m, []byte(msg)); err != nil {
		t.Fatal(err)
	}
	if claimed := q.claim(time.Now(), 10); len(claimed) != 1 {
		t.Fatalf("claimed %d", len(claimed))
	}
	q.writeMeta(&model.MailMessage{ID: "m1", From: "a@b.test", State: model.MailSending, ReceivedAt: m.ReceivedAt, Recipients: m.Recipients})

	q2, err := openQueue(dir) // the service restarted mid-delivery
	if err != nil {
		t.Fatal(err)
	}
	got, ok := q2.get("m1")
	if !ok || got.State != model.MailQueued {
		t.Fatalf("after restart: %+v %v", got, ok)
	}
	if data, _ := q2.content("m1"); string(data) != msg {
		t.Fatalf("content: %q", data)
	}
}

func TestAccessControl(t *testing.T) {
	rem := newRemote(t)
	h := newHarness(t, rem)
	hash, _ := bcrypt.GenerateFromPassword([]byte("s3cret"), bcrypt.MinCost)
	h.set(func(c *model.MailSettings) {
		c.RequireAuth = true
		c.Users = []model.MailUser{{Username: "app", PasswordHash: string(hash)}}
		c.AllowedSenderDomains = []string{"example.com"}
	})

	err := smtp.SendMail(h.addr(), nil, "app@example.com", []string{"a@x.test"}, []byte(msg))
	if err == nil || !strings.Contains(err.Error(), "530") {
		t.Fatalf("unauthenticated relay: %v", err)
	}
	// Loopback clients may log in without TLS.
	auth := smtp.PlainAuth("", "app", "s3cret", "127.0.0.1")
	if err := smtp.SendMail(h.addr(), auth, "app@example.com", []string{"a@x.test"}, []byte(msg)); err != nil {
		t.Fatalf("authenticated relay: %v", err)
	}
	err = smtp.SendMail(h.addr(), auth, "app@elsewhere.test", []string{"a@x.test"}, []byte(msg))
	if err == nil || !strings.Contains(err.Error(), "550") {
		t.Fatalf("foreign sender domain: %v", err)
	}
	bad := smtp.PlainAuth("", "app", "wrong", "127.0.0.1")
	if err := smtp.SendMail(h.addr(), bad, "app@example.com", []string{"a@x.test"}, []byte(msg)); err == nil {
		t.Fatal("wrong password accepted")
	}

	// AUTH LOGIN, spoken by hand.
	c, err := net.Dial("tcp", h.addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	br := bufio.NewReader(c)
	say := func(line string) string {
		if line != "" {
			fmt.Fprintf(c, "%s\r\n", line)
		}
		var out string
		for {
			l, err := br.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			out += l
			if len(l) < 4 || l[3] != '-' {
				return out
			}
		}
	}
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	say("")
	if r := say("EHLO client.test"); !strings.Contains(r, "AUTH PLAIN LOGIN") {
		t.Fatalf("EHLO: %s", r)
	}
	if r := say("AUTH LOGIN"); !strings.HasPrefix(r, "334 "+b64("Username:")) {
		t.Fatalf("AUTH LOGIN: %s", r)
	}
	say(b64("app"))
	if r := say(b64("s3cret")); !strings.HasPrefix(r, "235") {
		t.Fatalf("LOGIN password: %s", r)
	}

	// Clients outside the allowed addresses are turned away at once.
	h.set(func(c *model.MailSettings) { c.AllowIPs = []string{"10.0.0.0/8"} })
	c2, err := net.Dial("tcp", h.addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	greeting, _ := bufio.NewReader(c2).ReadString('\n')
	if !strings.HasPrefix(greeting, "554 5.7.1") {
		t.Fatalf("refused client got %q", greeting)
	}
}

type fakeDNS struct {
	mx    map[string][]*net.MX
	hosts map[string][]string
	txt   map[string][]string
	ptr   map[string][]string
}

func notFound(name string) error {
	return &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (f fakeDNS) LookupTXT(_ context.Context, name string) ([]string, error) {
	if t, ok := f.txt[strings.TrimSuffix(name, ".")]; ok {
		return t, nil
	}
	return nil, notFound(name)
}

func (f fakeDNS) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	var out []net.IPAddr
	for _, h := range f.hosts[strings.TrimSuffix(host, ".")] {
		out = append(out, net.IPAddr{IP: net.ParseIP(h)})
	}
	if len(out) == 0 {
		return nil, notFound(host)
	}
	return out, nil
}

func (f fakeDNS) LookupAddr(_ context.Context, addr string) ([]string, error) {
	if p, ok := f.ptr[addr]; ok {
		return p, nil
	}
	return nil, notFound(addr)
}

func (f fakeDNS) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	if mx, ok := f.mx[strings.TrimSuffix(name, ".")]; ok {
		return mx, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (f fakeDNS) LookupHost(_ context.Context, name string) ([]string, error) {
	if h, ok := f.hosts[strings.TrimSuffix(name, ".")]; ok {
		return h, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func TestDirectDelivery(t *testing.T) {
	rem := newRemote(t)
	var dialed []string
	var mu sync.Mutex
	h := newHarness(t, nil, func(o *Options) {
		o.Resolver = fakeDNS{
			mx: map[string][]*net.MX{
				"x.test":      {{Host: "mx2.x.test.", Pref: 20}, {Host: "mx1.x.test.", Pref: 10}},
				"nomail.test": {{Host: ".", Pref: 0}},
			},
			hosts: map[string][]string{"bare.test": {"192.0.2.1"}},
		}
		o.Dial = func(ctx context.Context, addr string) (net.Conn, error) {
			mu.Lock()
			dialed = append(dialed, addr)
			mu.Unlock()
			if addr == "mx1.x.test:25" {
				return nil, errors.New("connection refused")
			}
			return net.Dial("tcp", rem.addr)
		}
	})
	if h.cfg.Delivery != "direct" {
		t.Fatalf("default delivery is %q", h.cfg.Delivery)
	}
	if err := smtp.SendMail(h.addr(), nil, "app@example.com", []string{"a@x.test", "b@bare.test", "c@nomail.test", "d@missing.test"}, []byte(msg)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "delivery", func() bool { return len(h.s.Queue(model.MailFailed)) == 1 })
	mu.Lock()
	got := strings.Join(dialed, " ")
	mu.Unlock()
	if got != "mx1.x.test:25 mx2.x.test:25 bare.test:25" {
		t.Errorf("dialed %s", got)
	}
	m := h.s.Queue("")[0]
	states := map[string]string{}
	for _, r := range m.Recipients {
		states[r.Address] = r.State + " " + r.Error
	}
	if !strings.HasPrefix(states["a@x.test"], "delivered") || !strings.HasPrefix(states["b@bare.test"], "delivered") ||
		!strings.Contains(states["c@nomail.test"], "null MX") || !strings.Contains(states["d@missing.test"], "does not exist") {
		t.Errorf("recipients: %v", states)
	}
}

func TestPickupEnvelope(t *testing.T) {
	from, to, body, err := pickupEnvelope([]byte("X-Sender: bounce@example.com\r\nX-Receiver: one@x.test\r\nX-Receiver: two@x.test\r\n" +
		"From: App <app@example.com>\r\nTo: someone@else.test\r\nSubject: Hi\r\n\r\nBody\r\n"))
	if err != nil || from != "bounce@example.com" || strings.Join(to, ",") != "one@x.test,two@x.test" {
		t.Fatalf("%v %s %v", err, from, to)
	}
	if bytes.Contains(body, []byte("X-Sender")) || bytes.Contains(body, []byte("X-Receiver")) || !bytes.HasSuffix(body, []byte("\r\n\r\nBody\r\n")) {
		t.Fatalf("body: %q", body)
	}
	from, to, body, err = pickupEnvelope([]byte("From: a@example.com\nTo: b@x.test, C <c@x.test>\nBcc: hidden@x.test\n\nHi\n"))
	if err != nil || from != "a@example.com" || strings.Join(to, ",") != "b@x.test,c@x.test,hidden@x.test" || bytes.Contains(body, []byte("hidden")) {
		t.Fatalf("%v %s %v %q", err, from, to, body)
	}
}

func TestPickupDirectory(t *testing.T) {
	rem := newRemote(t)
	h := newHarness(t, rem)
	h.set(func(c *model.MailSettings) { c.PickupDirectory = true })
	f := filepath.Join(h.s.q.path("pickup"), "a.eml")
	os.WriteFile(f, []byte("From: app@example.com\r\nTo: p@x.test\r\n\r\nPicked up\r\n"), 0o644)
	old := time.Now().Add(-time.Minute)
	os.Chtimes(f, old, old)
	waitFor(t, "the pickup file to be delivered", func() bool { return len(rem.messages()) == 1 })
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Errorf("pickup file still there: %v", err)
	}
}

func TestDKIMKeys(t *testing.T) {
	key, err := GenerateDKIMKey()
	if err != nil {
		t.Fatal(err)
	}
	rec, err := DKIMRecord(key)
	if err != nil || !strings.HasPrefix(rec, "v=DKIM1; k=rsa; p=MIIBIjAN") {
		t.Fatalf("record %q: %v", rec, err)
	}
	if _, err := ParseDKIMKey("-----BEGIN PRIVATE KEY-----\nnope\n-----END PRIVATE KEY-----"); err == nil {
		t.Fatal("garbage accepted as a key")
	}
	if DKIMName("nh", "example.com") != "nh._domainkey.example.com" {
		t.Fatal("DKIMName")
	}
}
