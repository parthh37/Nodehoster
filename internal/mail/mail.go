// Package mail is NodeHoster's SMTP virtual server: a send-only relay in
// the style of the IIS 6 SMTP service. Applications submit mail over SMTP
// (usually to 127.0.0.1:25, no password needed) or drop .eml files in the
// pickup folder. Messages are spooled on disk, optionally DKIM-signed, and
// delivered directly to the recipients' mail servers or through a smart
// host, with retries until they expire. Mail for local mailboxes is never
// accepted: there are none.
package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emersion/go-smtp"
	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
	"gopkg.in/natefinch/lumberjack.v2"
)

// Options are the server's dependencies.
type Options struct {
	Dir      string // the spool: queue/, failed/, pickup/
	LogFile  string // protocol log, like the IIS SMTP log
	Log      *slog.Logger
	Bus      *events.Bus
	Settings func() model.MailSettings
	Unseal   func(string) string                                      // secrets are sealed in the settings
	Cert     func(id string) *tls.Certificate                         // STARTTLS certificate from the store
	Resolver Resolver                                                 // nil = the system resolver
	Dial     func(ctx context.Context, addr string) (net.Conn, error) // nil = TCP; tests override it
}

// Resolver finds mail servers; *net.Resolver is one.
type Resolver interface {
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupHost(ctx context.Context, host string) ([]string, error)
}

const workers = 4

type Server struct {
	opt Options
	q   *queue
	log *lumberjack.Logger

	mu        sync.Mutex
	cfg       model.MailSettings
	srv       *smtp.Server
	ln        net.Listener
	addr      string
	listenErr error
	reported  string // listen error already reported as an event
	started   bool   // Start was called: listen only when mail is also delivered

	wake      chan struct{}
	accepted  atomic.Int64
	delivered atomic.Int64
	bounced   atomic.Int64
	since     time.Time
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	sem       chan struct{}
}

// New opens the spool. Nothing listens or delivers until Start.
func New(opt Options) (*Server, error) {
	q, err := openQueue(opt.Dir)
	if err != nil {
		return nil, fmt.Errorf("open mail queue: %w", err)
	}
	s := &Server{
		opt: opt, q: q, wake: make(chan struct{}, 1), since: time.Now(), sem: make(chan struct{}, workers),
		cfg: opt.Settings(),
	}
	if opt.LogFile != "" {
		os.MkdirAll(filepath.Dir(opt.LogFile), 0o750)
		s.log = &lumberjack.Logger{Filename: opt.LogFile, MaxSize: 20, MaxBackups: 5, MaxAge: 30, LocalTime: true}
	}
	return s, nil
}

// Start begins delivering queued mail and, when enabled, listening.
func (s *Server) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.mu.Lock()
	s.started = true
	s.mu.Unlock()
	s.Apply(s.opt.Settings())
	s.wg.Add(2)
	go func() { defer s.wg.Done(); s.scheduler(ctx) }()
	go func() { defer s.wg.Done(); s.housekeeping(ctx) }()
}

// Shutdown stops listening and waits for deliveries in progress.
func (s *Server) Shutdown(ctx context.Context) {
	s.mu.Lock()
	s.stopListener()
	s.started = false
	s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
	if s.log != nil {
		s.log.Close()
	}
}

func (s *Server) config() model.MailSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

// Apply takes new settings. The listener is only restarted when the
// address, certificate or enabled state changed, so saving other
// settings never drops a client in the middle of a message.
func (s *Server) Apply(cfg model.MailSettings) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.cfg
	s.cfg = cfg
	want := ""
	if cfg.Enabled && s.started {
		want = net.JoinHostPort(cfg.ListenIP, strconv.Itoa(cfg.Port))
	}
	if s.ln != nil && want == s.addr && old.CertificateID == cfg.CertificateID && old.MaxMessageMB == cfg.MaxMessageMB && old.Hostname == cfg.Hostname {
		return
	}
	s.stopListener()
	s.addr, s.listenErr = want, nil
	if want != "" {
		s.startListener()
	}
	s.kick()
}

// startListener must be called with mu held.
func (s *Server) startListener() {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		s.listenErr = err
		msg := err.Error()
		if s.reported != msg {
			s.reported = msg
			s.opt.Bus.Error(events.MailError, "", "The SMTP server could not listen on %s: %v", s.addr, err)
		}
		return
	}
	s.listenErr, s.reported = nil, ""
	srv := smtp.NewServer(smtp.BackendFunc(s.newSession))
	srv.Domain = hostnameFor(s.cfg)
	srv.MaxMessageBytes = int64(s.cfg.MaxMessageMB) << 20
	srv.MaxRecipients = s.cfg.MaxRecipients
	srv.ReadTimeout = 5 * time.Minute
	srv.WriteTimeout = time.Minute
	srv.AllowInsecureAuth = true // Auth itself insists on TLS except from this computer
	srv.ErrorLog = slogLogger{s.opt.Log}
	if id := s.cfg.CertificateID; id != "" {
		srv.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
				if c := s.opt.Cert(id); c != nil {
					return c, nil // renewed certificates are picked up
				}
				return nil, errors.New("the SMTP certificate is missing")
			},
		}
	}
	s.srv, s.ln = srv, &filterListener{Listener: ln, s: s}
	s.opt.Log.Info("SMTP server listening", "addr", ln.Addr().String())
	go srv.Serve(s.ln)
}

// stopListener must be called with mu held.
func (s *Server) stopListener() {
	if s.srv != nil {
		s.srv.Close()
		s.srv, s.ln = nil, nil
	}
}

func (s *Server) clientAllowed(a net.Addr) bool {
	ta, ok := a.(*net.TCPAddr)
	if !ok {
		return false
	}
	for _, c := range s.config().AllowIPs {
		if n, err := model.ParseCIDROrIP(c); err == nil && n.Contains(ta.IP) {
			return true
		}
	}
	return false
}

func (s *Server) hostname() string { return hostnameFor(s.config()) }

// hostnameFor is the name the server greets and signs trace headers with.
func hostnameFor(cfg model.MailSettings) string {
	if h := cfg.Hostname; h != "" {
		return h
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return strings.ToLower(h)
	}
	return "localhost"
}

// Status is the server at a glance.
func (s *Server) Status() model.MailStatus {
	s.mu.Lock()
	st := model.MailStatus{Enabled: s.cfg.Enabled, Since: s.since}
	if s.ln != nil {
		st.Listening, st.Addr = true, s.ln.Addr().String()
	} else if s.cfg.Enabled {
		st.Addr = s.addr
	}
	if s.listenErr != nil {
		st.Error = s.listenErr.Error()
	}
	s.mu.Unlock()
	st.Queued, st.Failed = s.q.counts()
	st.Accepted, st.Delivered, st.Bounced = s.accepted.Load(), s.delivered.Load(), s.bounced.Load()
	return st
}

// Queue lists messages in a state ("" = all).
func (s *Server) Queue(state string) []model.MailMessage { return s.q.list(state) }

// Content returns a queued or failed message as it will be sent.
func (s *Server) Content(id string) ([]byte, error) { return s.q.content(id) }

func (s *Server) Retry(id string) error {
	err := s.q.retry(id)
	s.kick()
	return err
}

func (s *Server) RetryAll() {
	s.q.retryAll()
	s.kick()
}

func (s *Server) Delete(id string) error { return s.q.remove(id) }

// SendTest queues a short message, from the console or the manager.
func (s *Server) SendTest(from, to string) (*model.MailMessage, error) {
	if !validAddress(to) {
		return nil, &model.ValidationError{Field: "to", Message: "enter an e-mail address"}
	}
	if from == "" {
		from = "nodehoster@" + s.hostname()
	} else if !validAddress(from) {
		return nil, &model.ValidationError{Field: "from", Message: "enter an e-mail address"}
	}
	body := fmt.Sprintf("From: NodeHoster <%s>\r\nTo: <%s>\r\nSubject: NodeHoster test message\r\nMIME-Version: 1.0\r\n"+
		"Content-Type: text/plain; charset=utf-8\r\n\r\nThis is a test message from the NodeHoster SMTP server on %s.\r\n"+
		"If you can read it, mail from this server is being delivered.\r\n", from, to, s.hostname())
	return s.submit(submission{from: from, to: []string{to}, data: []byte(body), source: "test"})
}

// kick wakes the scheduler without blocking.
func (s *Server) kick() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Server) scheduler(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		for _, m := range s.q.claim(time.Now(), workers) {
			select {
			case s.sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			s.wg.Add(1)
			go func(m model.MailMessage) {
				defer func() { <-s.sem; s.wg.Done() }()
				s.attempt(ctx, m)
			}(m)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.wake:
		}
	}
}

// housekeeping purges old failed mail and picks up dropped files.
func (s *Server) housekeeping(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	purged, retried := time.Time{}, time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		cfg := s.config()
		if cfg.PickupDirectory {
			s.pickup()
		}
		if time.Since(retried) > 30*time.Second {
			// Try again to listen on an address that was taken, e.g. by
			// the IIS SMTP service that is being replaced.
			retried = time.Now()
			s.mu.Lock()
			if s.cfg.Enabled && s.ln == nil && s.addr != "" {
				s.startListener()
			}
			s.mu.Unlock()
		}
		if time.Since(purged) > time.Hour {
			purged = time.Now()
			s.q.purge(time.Duration(cfg.KeepFailedDays) * 24 * time.Hour)
		}
	}
}

// pickup queues the .eml files in the pickup folder, taking the envelope
// from the X-Sender/X-Receiver headers IIS uses or from the From, To, Cc
// and Bcc headers.
func (s *Server) pickup() {
	dir := s.q.path("pickup")
	files, _ := filepath.Glob(filepath.Join(dir, "*.eml"))
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil || time.Since(st.ModTime()) < 2*time.Second {
			continue // still being written
		}
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		from, to, body, err := pickupEnvelope(data)
		if err == nil {
			_, err = s.submit(submission{from: from, to: to, data: body, source: "pickup"})
		}
		if err != nil {
			s.logf("pickup %s: %v", filepath.Base(f), err)
			bad := filepath.Join(s.q.path("failed"), "pickup-"+filepath.Base(f))
			os.Rename(f, bad)
			s.opt.Bus.Warn(events.MailFailed, "", "A file in the mail pickup folder could not be sent: %s: %v", filepath.Base(f), err)
			continue
		}
		os.Remove(f)
	}
}

func (s *Server) logf(format string, a ...any) {
	if s.log == nil {
		return
	}
	fmt.Fprintf(s.log, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, a...))
}

type slogLogger struct{ l *slog.Logger }

func (l slogLogger) Printf(format string, v ...any) { l.l.Debug("smtp: " + fmt.Sprintf(format, v...)) }
func (l slogLogger) Println(v ...any)               { l.l.Debug("smtp: " + fmt.Sprint(v...)) }
