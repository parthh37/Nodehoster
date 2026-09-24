package logship

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// NewSender builds the sender for a target (secrets in plain text).
func NewSender(t model.LogTarget, hostname string) (Sender, error) {
	switch {
	case t.Type == model.LogTargetSyslog && t.Syslog != nil:
		return newSyslog(*t.Syslog, hostname)
	case t.Type == model.LogTargetSeq && t.Seq != nil:
		return &seqSender{cfg: *t.Seq, client: httpClient}, nil
	case t.Type == model.LogTargetHTTP && t.HTTP != nil:
		return &httpSender{cfg: *t.HTTP, client: httpClient}, nil
	}
	return nil, fmt.Errorf("target %q is not configured", t.Name)
}

// httpClient: each request is bounded by its context.
var httpClient = &http.Client{Transport: &http.Transport{
	Proxy:               http.ProxyFromEnvironment,
	TLSHandshakeTimeout: 10 * time.Second,
	MaxIdleConnsPerHost: 2,
	IdleConnTimeout:     90 * time.Second,
}}

// ---- syslog (RFC 5424)

// maxUDP bounds a datagram; longer messages are cut. rsyslog and
// syslog-ng accept 8 KiB by default.
const maxUDP = 8192

type syslogSender struct {
	cfg      model.SyslogTarget
	facility int
	host     string
	app      string
	tls      *tls.Config

	mu   sync.Mutex
	conn net.Conn
}

func newSyslog(cfg model.SyslogTarget, hostname string) (*syslogSender, error) {
	s := &syslogSender{cfg: cfg, facility: model.SyslogFacilities[cfg.Facility], host: cfg.Hostname, app: cfg.AppName}
	if s.host == "" {
		s.host = hostname
	}
	if s.host == "" {
		s.host = "-"
	}
	if s.app == "" {
		s.app = "nodehoster"
	}
	if cfg.Transport == "tls" {
		host, _, _ := net.SplitHostPort(cfg.Address)
		s.tls = &tls.Config{ServerName: host, InsecureSkipVerify: cfg.InsecureSkipVerify, MinVersion: tls.VersionTLS12}
		if strings.TrimSpace(cfg.CACert) != "" {
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM([]byte(cfg.CACert)) {
				return nil, errors.New("the CA certificate is not PEM")
			}
			s.tls.RootCAs = pool
		}
	}
	return s, nil
}

func (s *syslogSender) dial(ctx context.Context) (net.Conn, error) {
	d := net.Dialer{Timeout: 10 * time.Second}
	switch s.cfg.Transport {
	case "udp":
		return d.DialContext(ctx, "udp", s.cfg.Address)
	case "tls":
		td := tls.Dialer{NetDialer: &d, Config: s.tls}
		return td.DialContext(ctx, "tcp", s.cfg.Address)
	}
	return d.DialContext(ctx, "tcp", s.cfg.Address)
}

func (s *syslogSender) Send(ctx context.Context, batch []Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		c, err := s.dial(ctx)
		if err != nil {
			return err
		}
		s.conn = c
	}
	if dl, ok := ctx.Deadline(); ok {
		s.conn.SetWriteDeadline(dl)
	}
	var err error
	if s.cfg.Transport == "udp" {
		for _, r := range batch {
			msg := FormatSyslog(r, s.facility, s.host, s.app)
			if len(msg) > maxUDP {
				msg = msg[:maxUDP]
			}
			if _, err = s.conn.Write(msg); err != nil {
				break
			}
		}
	} else {
		// Octet counting (RFC 6587 / RFC 5425): "<length> <message>", so
		// messages may contain newlines (stack traces).
		var buf bytes.Buffer
		for _, r := range batch {
			msg := FormatSyslog(r, s.facility, s.host, s.app)
			buf.WriteString(strconv.Itoa(len(msg)))
			buf.WriteByte(' ')
			buf.Write(msg)
		}
		_, err = s.conn.Write(buf.Bytes())
	}
	if err != nil {
		s.conn.Close()
		s.conn = nil
	}
	return err
}

func (s *syslogSender) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		err := s.conn.Close()
		s.conn = nil
		return err
	}
	return nil
}

// Syslog severities.
func severity(level string) int {
	switch level {
	case "error":
		return 3
	case "warning":
		return 4
	case "debug":
		return 7
	}
	return 6 // informational
}

// sdID is NodeHoster's structured-data element. 32473 is the private
// enterprise number RFC 5612 reserves for documentation and examples.
const sdID = "nodehoster@32473"

// FormatSyslog renders an RFC 5424 message:
//
//	<PRI>1 TIMESTAMP HOSTNAME APP-NAME PROCID MSGID [SD] MSG
//
// MSGID is the source (app, access, server, event, audit); the site,
// instance, stream and access fields are structured data.
func FormatSyslog(r Record, facility int, host, app string) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "<%d>1 %s %s %s - %s ", facility*8+severity(r.Level), r.Time.Format("2006-01-02T15:04:05.000000Z07:00"),
		header(host, 255), header(app, 48), header(r.Source, 32))
	params := [][2]string{}
	add := func(k, v string) {
		if v != "" {
			params = append(params, [2]string{k, v})
		}
	}
	add("site", r.SiteName)
	add("siteId", r.SiteID)
	if r.Instance != nil {
		add("instance", strconv.Itoa(*r.Instance))
	}
	add("stream", r.Stream)
	if a := r.Access; a != nil {
		add("method", a.Method)
		add("path", a.Path)
		add("status", strconv.Itoa(a.Status))
		add("bytes", strconv.FormatInt(a.Bytes, 10))
		add("durationMs", strconv.FormatFloat(a.DurationMs, 'f', 1, 64))
		add("clientIp", a.ClientIP)
		add("host", a.Host)
		add("userAgent", a.UserAgent)
	}
	keys := make([]string, 0, len(r.Attrs))
	for k := range r.Attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		add(sdName(k), r.Attrs[k])
	}
	if len(params) == 0 {
		b.WriteString("-")
	} else {
		b.WriteString("[" + sdID)
		for _, p := range params {
			b.WriteString(" " + p[0] + `="` + sdEscape(p[1]) + `"`)
		}
		b.WriteString("]")
	}
	b.WriteString(" ")
	b.WriteString(r.Message)
	return b.Bytes()
}

// header makes a header field printable US-ASCII without spaces, "-" if empty.
func header(s string, max int) string {
	out := strings.Map(func(r rune) rune {
		if r < 33 || r > 126 {
			return '_'
		}
		return r
	}, s)
	if len(out) > max {
		out = out[:max]
	}
	if out == "" {
		return "-"
	}
	return out
}

func sdName(s string) string {
	out := strings.Map(func(r rune) rune {
		if r < 33 || r > 126 || r == '=' || r == ']' || r == '"' {
			return '_'
		}
		return r
	}, s)
	if len(out) > 32 {
		out = out[:32]
	}
	return out
}

func sdEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, `]`, `\]`).Replace(s)
}

// ---- Seq (CLEF)

type seqSender struct {
	cfg    model.SeqTarget
	client *http.Client
}

var clefLevels = map[string]string{"debug": "Debug", "info": "Information", "warning": "Warning", "error": "Error"}

// CLEF renders a record as a compact log event format line: @t, @m and
// @l, with the site, instance, stream and source as properties.
func CLEF(r Record) map[string]any {
	e := map[string]any{"@t": r.Time.UTC().Format(time.RFC3339Nano), "@m": r.Message, "@l": clefLevels[r.Level], "Source": r.Source}
	if e["@l"] == "" || e["@l"] == nil {
		e["@l"] = "Information"
	}
	set := func(k string, v any) {
		if s, ok := v.(string); ok && s == "" {
			return
		}
		e[k] = v
	}
	set("Site", r.SiteName)
	set("SiteId", r.SiteID)
	if r.Instance != nil {
		e["Instance"] = *r.Instance
	}
	set("Stream", r.Stream)
	if a := r.Access; a != nil {
		set("RequestMethod", a.Method)
		set("RequestPath", a.Path)
		e["StatusCode"] = a.Status
		e["Bytes"] = a.Bytes
		e["Elapsed"] = a.DurationMs
		set("ClientIp", a.ClientIP)
		set("Host", a.Host)
		set("UserAgent", a.UserAgent)
	}
	for k, v := range r.Attrs {
		k = strings.TrimLeft(k, "@") // @-names are CLEF's own
		if k != "" {
			if _, taken := e[k]; !taken {
				e[k] = v
			}
		}
	}
	return e
}

func (s *seqSender) Send(ctx context.Context, batch []Record) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range batch {
		enc.Encode(CLEF(r)) // one object per line
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.URL+"/api/events/raw?clef", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/vnd.serilog.clef")
	if s.cfg.APIKey != "" {
		req.Header.Set("X-Seq-ApiKey", s.cfg.APIKey)
	}
	return do(s.client, req)
}

func (s *seqSender) Close() error { return nil }

// ---- generic HTTP JSON

type httpSender struct {
	cfg    model.HTTPTarget
	client *http.Client
}

func (s *httpSender) Send(ctx context.Context, batch []Record) error {
	var buf bytes.Buffer
	ct := "application/json"
	if s.cfg.Format == "ndjson" {
		ct = "application/x-ndjson"
		enc := json.NewEncoder(&buf)
		for _, r := range batch {
			enc.Encode(r)
		}
	} else if err := json.NewEncoder(&buf).Encode(batch); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.URL, &buf)
	if err != nil {
		return permanent{err}
	}
	req.Header.Set("Content-Type", ct)
	req.Header.Set("User-Agent", "NodeHoster-LogShipping")
	for _, h := range s.cfg.Headers {
		req.Header.Set(h.Name, h.Value)
	}
	return do(s.client, req)
}

func (s *httpSender) Close() error { return nil }

func do(client *http.Client, req *http.Request) error {
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 300 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	msg := strings.TrimSpace(string(body))
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return httpStatusError(resp.StatusCode, msg)
}
