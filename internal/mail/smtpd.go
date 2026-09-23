package mail

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
	"github.com/google/uuid"
	"github.com/parthh37/nodehoster/internal/model"
	"golang.org/x/crypto/bcrypt"
)

// filterListener enforces the connection restrictions (IIS "Connection
// control") before a client sees a greeting.
type filterListener struct {
	net.Listener
	s *Server
}

func (l *filterListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if l.s.clientAllowed(c.RemoteAddr()) {
			return c, nil
		}
		l.s.logf("refused connection from %s", c.RemoteAddr())
		c.SetWriteDeadline(time.Now().Add(5 * time.Second))
		io.WriteString(c, "554 5.7.1 Access denied\r\n")
		c.Close()
	}
}

type session struct {
	s     *Server
	conn  *smtp.Conn
	ip    string
	user  string
	from  string
	rcpts []string
}

func (s *Server) newSession(c *smtp.Conn) (smtp.Session, error) {
	ip := ""
	if a, ok := c.Conn().RemoteAddr().(*net.TCPAddr); ok {
		ip = a.IP.String()
	}
	return &session{s: s, conn: c, ip: ip}, nil
}

func (ss *session) Reset() {
	ss.from, ss.rcpts = "", nil
}

func (ss *session) Logout() error { return nil }

func (ss *session) AuthMechanisms() []string {
	if len(ss.s.config().Users) == 0 {
		return nil
	}
	return []string{sasl.Plain, sasl.Login}
}

// errAuthRequired is the RFC 4954 reply; go-smtp's own uses 502.
var errAuthRequired = &smtp.SMTPError{Code: 530, EnhancedCode: smtp.EnhancedCode{5, 7, 0}, Message: "Authentication required"}

var errEncryptionRequired = &smtp.SMTPError{Code: 538, EnhancedCode: smtp.EnhancedCode{5, 7, 11}, Message: "Encryption required for requested authentication mechanism"}

func (ss *session) Auth(mech string) (sasl.Server, error) {
	if _, tls := ss.conn.TLSConnectionState(); !tls && !isLoopback(ss.ip) {
		return nil, errEncryptionRequired
	}
	check := func(user, pass string) error {
		if !ss.s.checkUser(user, pass) {
			ss.s.logf("authentication failed for %q from %s", user, ss.ip)
			return smtp.ErrAuthFailed
		}
		ss.user = user
		return nil
	}
	switch mech {
	case sasl.Plain:
		return sasl.NewPlainServer(func(identity, user, pass string) error {
			if identity != "" && identity != user {
				return smtp.ErrAuthFailed
			}
			return check(user, pass)
		}), nil
	case sasl.Login:
		return &loginServer{check: check}, nil
	}
	return nil, smtp.ErrAuthUnknownMechanism
}

func (ss *session) Mail(from string, opts *smtp.MailOptions) error {
	cfg := ss.s.config()
	if cfg.RequireAuth && ss.user == "" {
		return errAuthRequired
	}
	if len(cfg.AllowedSenderDomains) > 0 {
		_, domain, _ := strings.Cut(from, "@")
		domain = strings.ToLower(domain)
		ok := false
		for _, d := range cfg.AllowedSenderDomains {
			if domain == d {
				ok = true
			}
		}
		if !ok {
			return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 7, 1}, Message: "Sender domain not allowed on this server"}
		}
	}
	if opts != nil && opts.Size > int64(cfg.MaxMessageMB)<<20 {
		return &smtp.SMTPError{Code: 552, EnhancedCode: smtp.EnhancedCode{5, 3, 4}, Message: "Message too big"}
	}
	ss.from = from
	return nil
}

func (ss *session) Rcpt(to string, opts *smtp.RcptOptions) error {
	if len(ss.rcpts) >= ss.s.config().MaxRecipients {
		return &smtp.SMTPError{Code: 452, EnhancedCode: smtp.EnhancedCode{4, 5, 3}, Message: "Too many recipients"}
	}
	if !validAddress(to) {
		return &smtp.SMTPError{Code: 501, EnhancedCode: smtp.EnhancedCode{5, 1, 3}, Message: "Bad recipient address syntax"}
	}
	ss.rcpts = append(ss.rcpts, to)
	return nil
}

func (ss *session) Data(r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	_, tls := ss.conn.TLSConnectionState()
	m, err := ss.s.submit(submission{
		from: ss.from, to: ss.rcpts, data: data, source: "smtp",
		clientIP: ss.ip, helo: ss.conn.Hostname(), user: ss.user, tls: tls,
	})
	if err != nil {
		ss.s.logf("could not queue a message from %s: %v", ss.ip, err)
		return &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "Could not queue the message, try again later"}
	}
	ss.s.logf("queued %s from %s <%s> for %d recipient(s), %d bytes", m.ID, ss.ip, ss.from, len(ss.rcpts), m.Size)
	return nil
}

// loginServer is the server side of AUTH LOGIN, which go-sasl does not
// provide but many clients (Outlook, System.Net.Mail) still use.
type loginServer struct {
	check func(user, pass string) error
	user  string
	step  int
}

func (l *loginServer) Next(response []byte) (challenge []byte, done bool, err error) {
	switch l.step {
	case 0:
		l.step = 1
		if response != nil { // AUTH LOGIN <user> sends the user name up front
			l.user = string(response)
			l.step = 2
			return []byte("Password:"), false, nil
		}
		return []byte("Username:"), false, nil
	case 1:
		l.user = string(response)
		l.step = 2
		return []byte("Password:"), false, nil
	case 2:
		l.step = 3
		return nil, true, l.check(l.user, string(response))
	}
	return nil, true, errors.New("unexpected response")
}

func isLoopback(ip string) bool {
	p := net.ParseIP(ip)
	return p != nil && p.IsLoopback()
}

func validAddress(a string) bool {
	local, domain, ok := strings.Cut(a, "@")
	return ok && local != "" && domain != "" && !strings.ContainsAny(a, " \t\r\n<>,;") && strings.Count(a, "@") == 1
}

// submission is a message on its way into the queue.
type submission struct {
	from     string
	to       []string
	data     []byte
	source   string
	clientIP string
	helo     string
	user     string
	tls      bool
}

// prepare adds the trace header and, like IIS, the Date and Message-ID
// headers a message must have if the client left them out. It returns
// the message and its decoded subject.
func (s *Server) prepare(sub submission, id string, now time.Time) ([]byte, string) {
	host := s.hostname()
	hdr, _ := textproto.NewReader(bufio.NewReader(bytes.NewReader(sub.data))).ReadMIMEHeader()
	var b bytes.Buffer
	if sub.source == "smtp" {
		with := "ESMTP"
		if sub.tls {
			with += "S"
		}
		if sub.user != "" {
			with += "A"
		}
		helo := sub.helo
		if helo == "" {
			helo = "unknown"
		}
		fmt.Fprintf(&b, "Received: from %s ([%s])\r\n\tby %s (NodeHoster) with %s id %s;\r\n\t%s\r\n",
			helo, sub.clientIP, host, with, id, now.Format(time.RFC1123Z))
	}
	if hdr.Get("Date") == "" {
		fmt.Fprintf(&b, "Date: %s\r\n", now.Format(time.RFC1123Z))
	}
	if hdr.Get("Message-Id") == "" {
		fmt.Fprintf(&b, "Message-ID: <%s@%s>\r\n", id, host)
	}
	b.Write(sub.data)
	subject := hdr.Get("Subject")
	if d, err := new(mime.WordDecoder).DecodeHeader(subject); err == nil {
		subject = d
	}
	return b.Bytes(), subject
}

// submit queues a message and wakes the delivery loop.
func (s *Server) submit(sub submission) (*model.MailMessage, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	data, subject := s.prepare(sub, id.String(), now)
	m := model.MailMessage{
		ID: id.String(), From: sub.from, Subject: subject, Size: int64(len(data)),
		Source: sub.source, ClientIP: sub.clientIP, User: sub.user,
		State: model.MailQueued, ReceivedAt: now,
	}
	for _, t := range sub.to {
		m.Recipients = append(m.Recipients, model.MailRecipient{Address: t, State: model.RcptPending})
	}
	if err := s.q.add(m, data); err != nil {
		return nil, err
	}
	s.accepted.Add(1)
	s.kick()
	return &m, nil
}

func (s *Server) checkUser(user, pass string) bool {
	for _, u := range s.config().Users {
		if strings.EqualFold(u.Username, user) && u.PasswordHash != "" {
			return bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(pass)) == nil
		}
	}
	bcrypt.CompareHashAndPassword(dummyHash(), []byte(pass)) // same cost as a real user
	return false
}

// dummyHash makes a login for an unknown user take as long as for a real
// one, so user names cannot be probed by timing.
var dummyHash = sync.OnceValue(func() []byte {
	h, _ := bcrypt.GenerateFromPassword([]byte("nodehoster"), bcrypt.DefaultCost)
	return h
})
