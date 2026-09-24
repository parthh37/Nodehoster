package logship

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/parthh37/nodehoster/internal/model"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func at() time.Time { return time.Date(2026, 3, 1, 2, 30, 5, 123456000, time.UTC) }

func TestFormatSyslog(t *testing.T) {
	t.Parallel()
	inst := 2
	r := Record{Time: at(), Source: "app", Level: "error", Message: "boom\nat x", SiteID: "id1", SiteName: `shop "eu"]`, Instance: &inst, Stream: "stderr"}
	got := string(FormatSyslog(r, 16, "web 01", "nodehoster"))
	want := `<131>1 2026-03-01T02:30:05.123456Z web_01 nodehoster - app [nodehoster@32473 site="shop \"eu\"\]" siteId="id1" instance="2" stream="stderr"] boom` + "\nat x"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	a := Record{Time: at(), Source: "access", Level: "info", Message: "GET / 200", Access: &AccessFields{Method: "GET", Path: "/", Status: 200, Bytes: 5, DurationMs: 1.25, ClientIP: "10.0.0.1", Host: "x"}}
	if s := string(FormatSyslog(a, 1, "h", "a")); !strings.HasPrefix(s, "<14>1 ") || !strings.Contains(s, ` status="200" bytes="5" durationMs="1.2" clientIp="10.0.0.1" host="x"]`) {
		t.Errorf("access = %q", s)
	}
	if s := string(FormatSyslog(Record{Time: at(), Source: "server", Level: "debug", Message: "m"}, 3, "", "")); s != "<31>1 2026-03-01T02:30:05.123456Z - - - server - m" {
		t.Errorf("bare = %q", s)
	}
}

func TestCLEF(t *testing.T) {
	t.Parallel()
	inst := 0
	e := CLEF(Record{Time: at(), Source: "app", Level: "warning", Message: "careful", SiteID: "s1", SiteName: "Shop", Instance: &inst, Stream: "stderr",
		Attrs: map[string]string{"@x": "y", "Source": "overwritten?"}})
	b, _ := json.Marshal(e)
	var m map[string]any
	json.Unmarshal(b, &m)
	for k, v := range map[string]any{"@t": "2026-03-01T02:30:05.123456Z", "@m": "careful", "@l": "Warning", "Site": "Shop", "SiteId": "s1", "Instance": float64(0), "Stream": "stderr", "Source": "app", "x": "y"} {
		if m[k] != v {
			t.Errorf("%s = %v, want %v (%s)", k, m[k], v, b)
		}
	}
	if CLEF(Record{Level: "info"})["@l"] != "Information" || CLEF(Record{Level: ""})["@l"] != "Information" {
		t.Error("default level")
	}
}

func syslogTarget(transport, addr string) model.LogTarget {
	return model.LogTarget{ID: "t", Name: "sys", Type: "syslog", Enabled: true, Sources: []string{"app"},
		Syslog: &model.SyslogTarget{Address: addr, Transport: transport, Facility: "local0", AppName: "nh"}}
}

func TestSyslogUDP(t *testing.T) {
	t.Parallel()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	s, err := NewSender(syslogTarget("udp", pc.LocalAddr().String()), "web01")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	big := strings.Repeat("x", 20000)
	if err := s.Send(context.Background(), []Record{{Time: at(), Source: "app", Level: "info", Message: "one"}, {Time: at(), Source: "app", Level: "info", Message: big}}); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 65536)
	pc.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, _, err := pc.ReadFrom(buf)
	if err != nil || !strings.HasPrefix(string(buf[:n]), "<134>1 2026-03-01T02:30:05.123456Z web01 nh - app - one") {
		t.Fatalf("datagram 1 = %q, %v", buf[:n], err)
	}
	n, _, _ = pc.ReadFrom(buf)
	if n != maxUDP {
		t.Errorf("long datagram = %d bytes, want %d", n, maxUDP)
	}
}

// readFrames reads octet-counted syslog frames from c.
func readFrames(t *testing.T, c net.Conn, n int) []string {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(c)
	var out []string
	for len(out) < n {
		lenStr, err := br.ReadString(' ')
		if err != nil {
			t.Fatalf("frame length: %v", err)
		}
		l, err := strconv.Atoi(strings.TrimSpace(lenStr))
		if err != nil {
			t.Fatalf("frame length %q", lenStr)
		}
		msg := make([]byte, l)
		if _, err := io.ReadFull(br, msg); err != nil {
			t.Fatal(err)
		}
		out = append(out, string(msg))
	}
	return out
}

func TestSyslogTCPOctetCounting(t *testing.T) {
	t.Parallel()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	got := make(chan []string, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		got <- readFrames(t, c, 2)
	}()
	s, _ := NewSender(syslogTarget("tcp", l.Addr().String()), "web01")
	defer s.Close()
	if err := s.Send(context.Background(), []Record{{Time: at(), Source: "app", Level: "error", Message: "line1\nline2"}, {Time: at(), Source: "app", Level: "info", Message: "ok"}}); err != nil {
		t.Fatal(err)
	}
	frames := <-got
	if !strings.HasSuffix(frames[0], "- line1\nline2") || !strings.HasPrefix(frames[0], "<131>1 ") || !strings.HasSuffix(frames[1], " ok") {
		t.Errorf("frames = %q", frames)
	}
}

func selfSignedTLS(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestSyslogTLS(t *testing.T) {
	t.Parallel()
	cert, caPEM := selfSignedTLS(t)
	l, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	got := make(chan []string, 3)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				if err := c.(*tls.Conn).Handshake(); err != nil {
					return
				}
				got <- readFrames(t, c, 1)
			}()
		}
	}()
	rec := []Record{{Time: at(), Source: "server", Level: "warning", Message: "secure"}}

	// The server's certificate is not trusted without the CA.
	cfg := syslogTarget("tls", l.Addr().String())
	s, _ := NewSender(cfg, "h")
	if err := s.Send(context.Background(), rec); err == nil {
		t.Error("an untrusted certificate was accepted")
	}
	s.Close()

	cfg.Syslog.CACert = caPEM
	s, err = NewSender(cfg, "h")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if f := <-got; !strings.HasPrefix(f[0], "<132>1 ") || !strings.HasSuffix(f[0], " secure") {
		t.Errorf("frame = %q", f)
	}

	cfg.Syslog.CACert, cfg.Syslog.InsecureSkipVerify = "", true
	s, _ = NewSender(cfg, "h")
	defer s.Close()
	if err := s.Send(context.Background(), rec); err != nil {
		t.Errorf("skip-verify: %v", err)
	}
	<-got
}

func TestSeqSender(t *testing.T) {
	t.Parallel()
	var body, key, ct, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body, key, ct, path = string(b), r.Header.Get("X-Seq-ApiKey"), r.Header.Get("Content-Type"), r.URL.RequestURI()
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	s, _ := NewSender(model.LogTarget{Type: "seq", Seq: &model.SeqTarget{URL: srv.URL, APIKey: "k3y"}}, "h")
	if err := s.Send(context.Background(), []Record{{Time: at(), Source: "app", Level: "info", Message: "a"}, {Time: at(), Source: "app", Level: "error", Message: "b"}}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if path != "/api/events/raw?clef" || key != "k3y" || ct != "application/vnd.serilog.clef" || len(lines) != 2 || !strings.Contains(lines[1], `"@l":"Error"`) {
		t.Errorf("path %q key %q ct %q body %q", path, key, ct, body)
	}
}

// collector is an HTTP endpoint that records the batches it receives and
// answers with the status codes it is told to.
type collector struct {
	mu      sync.Mutex
	batches [][]Record
	codes   []int // answered in turn; then 200
	times   []time.Time
	stall   chan struct{}
	headers http.Header
}

func (c *collector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if c.stall != nil {
		select {
		case <-c.stall:
		case <-r.Context().Done():
		}
		return
	}
	data, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.times = append(c.times, time.Now())
	c.headers = r.Header.Clone()
	if len(c.codes) > 0 {
		code := c.codes[0]
		c.codes = c.codes[1:]
		if code >= 300 {
			w.WriteHeader(code)
			return
		}
	}
	var batch []Record
	if r.Header.Get("Content-Type") == "application/x-ndjson" {
		dec := json.NewDecoder(bytes.NewReader(data))
		for {
			var rec Record
			if dec.Decode(&rec) != nil {
				break
			}
			batch = append(batch, rec)
		}
	} else {
		json.Unmarshal(data, &batch)
	}
	c.batches = append(c.batches, batch)
}

func (c *collector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, b := range c.batches {
		n += len(b)
	}
	return n
}

func httpTarget(url, format string) model.LogTarget {
	return model.LogTarget{ID: "h", Name: "http", Type: "http", Enabled: true, Sources: []string{"app", "server"}, MinLevel: "info",
		HTTP: &model.HTTPTarget{URL: url, Format: format, Headers: []model.HTTPHeader{{Name: "Authorization", Value: "Bearer abc", Secret: true}}}}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHTTPBatching(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"json", "ndjson"} {
		c := &collector{}
		srv := httptest.NewServer(c)
		sh := New(quiet, "h", func(id string) string { return "name-" + id }, Options{BatchSize: 100, FlushEvery: 20 * time.Millisecond})
		sh.Configure([]model.LogTarget{httpTarget(srv.URL, format)})
		for i := range 250 {
			sh.Ship(Record{Source: "app", Level: "info", Message: "m" + strconv.Itoa(i), SiteID: "s1"})
		}
		waitFor(t, "250 records", func() bool { return c.count() == 250 })
		c.mu.Lock()
		if len(c.batches) < 3 || len(c.batches[0]) > 100 || c.batches[0][0].Message != "m0" || c.batches[0][0].SiteName != "name-s1" || c.headers.Get("Authorization") != "Bearer abc" {
			t.Errorf("%s: %d batches, first %+v, headers %v", format, len(c.batches), c.batches[0][0], c.headers)
		}
		c.mu.Unlock()
		if st := sh.Status(); st[0].Sent != 250 || st[0].LastSuccess == nil || st[0].Dropped != 0 {
			t.Errorf("%s: status %+v", format, st[0])
		}
		sh.Close()
		srv.Close()
	}
}

func TestHTTPRetryBackoffAndPermanentFailure(t *testing.T) {
	t.Parallel()
	c := &collector{codes: []int{503, 503, 200}}
	srv := httptest.NewServer(c)
	defer srv.Close()
	sh := New(quiet, "h", nil, Options{FlushEvery: 10 * time.Millisecond, RetryBase: 40 * time.Millisecond, RetryMax: time.Second})
	defer sh.Close()
	sh.Configure([]model.LogTarget{httpTarget(srv.URL, "json")})
	sh.Ship(Record{Source: "app", Level: "info", Message: "retried"})
	waitFor(t, "the retried record", func() bool { return c.count() == 1 })
	c.mu.Lock()
	gap1, gap2 := c.times[1].Sub(c.times[0]), c.times[2].Sub(c.times[1])
	c.mu.Unlock()
	if gap1 < 35*time.Millisecond || gap2 < 75*time.Millisecond {
		t.Errorf("retry delays %v, %v: want about 40 ms then 80 ms", gap1, gap2)
	}
	st := sh.Status()[0]
	if st.Sent != 1 || st.LastError == "" || st.Failed != 0 {
		t.Errorf("status %+v", st)
	}

	// A 400 is not retried: the batch is counted as failed at once.
	c.mu.Lock()
	c.codes = []int{400}
	before := len(c.times)
	c.mu.Unlock()
	sh.Ship(Record{Source: "app", Level: "info", Message: "refused"})
	waitFor(t, "the refused batch", func() bool { return sh.Status()[0].Failed == 1 })
	time.Sleep(100 * time.Millisecond)
	c.mu.Lock()
	if len(c.times) != before+1 {
		t.Errorf("a 400 was retried: %d requests", len(c.times)-before)
	}
	c.mu.Unlock()
}

// A collector that never answers must not slow down logging: Ship returns
// at once, and the oldest records are dropped and counted.
func TestShipNeverBlocksOnAStalledCollector(t *testing.T) {
	t.Parallel()
	c := &collector{stall: make(chan struct{})}
	srv := httptest.NewServer(c)
	defer srv.Close()
	defer close(c.stall)
	sh := New(quiet, "h", nil, Options{QueueSize: 100, BatchSize: 10, FlushEvery: 5 * time.Millisecond, Timeout: time.Minute})
	sh.Configure([]model.LogTarget{httpTarget(srv.URL, "json")})
	start := time.Now()
	for i := range 50000 {
		sh.Ship(Record{Source: "app", Level: "info", Message: "x" + strconv.Itoa(i)})
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("shipping 50,000 records took %v", d)
	}
	st := sh.Status()[0]
	if st.Dropped < 49000 || st.Queued > 100 {
		t.Errorf("status %+v", st)
	}
	done := make(chan struct{})
	go func() { sh.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Error("Close hung on a stalled collector")
	}
}

type fakeSender struct {
	mu   sync.Mutex
	recs []Record
	err  error
}

func (f *fakeSender) Send(ctx context.Context, b []Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.recs = append(f.recs, b...)
	return nil
}
func (f *fakeSender) Close() error { return nil }
func (f *fakeSender) messages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.recs {
		out = append(out, r.Message)
	}
	return out
}

func TestFilters(t *testing.T) {
	t.Parallel()
	fake := &fakeSender{}
	sh := New(quiet, "h", nil, Options{FlushEvery: 5 * time.Millisecond})
	sh.NewSender = func(model.LogTarget, string) (Sender, error) { return fake, nil }
	defer sh.Close()
	sh.Configure([]model.LogTarget{{ID: "a", Name: "a", Type: "http", Enabled: true, Sources: []string{"server", "app", "event"}, SiteIDs: []string{"s1"}, MinLevel: "warning"},
		{ID: "b", Name: "off", Type: "http", Enabled: false, Sources: []string{"audit"}}})
	if !sh.Wants("app") || sh.Wants("access") || sh.Wants("audit") || sh.WantsServerLevel("info") || !sh.WantsServerLevel("error") {
		t.Error("Wants is wrong")
	}
	for _, r := range []Record{
		{Source: "app", SiteID: "s1", Level: "info", Message: "app s1"},
		{Source: "app", SiteID: "s2", Level: "info", Message: "app s2"},
		{Source: "server", Level: "info", Message: "server info"},
		{Source: "server", Level: "error", Message: "server error"},
		{Source: "event", Level: "info", Message: "server event"},
		{Source: "event", SiteID: "s2", Level: "error", Message: "s2 event"},
		{Source: "access", SiteID: "s1", Level: "info", Message: "access"},
		{Source: "audit", Level: "info", Message: "audit"},
	} {
		sh.Ship(r)
	}
	want := "app s1|server error|server event"
	waitFor(t, want, func() bool { return strings.Join(fake.messages(), "|") == want })
}

func TestTeeShipsServerLogWithoutRecursion(t *testing.T) {
	t.Parallel()
	var file bytes.Buffer
	var fileMu sync.Mutex
	tee := NewTee(slog.NewTextHandler(lockedWriter{&file, &fileMu}, &slog.HandlerOptions{Level: slog.LevelInfo}))
	log := slog.New(tee)
	failing := &fakeSender{err: errors.New("collector down")}
	var calls atomic.Int32
	// What core does: the shipper logs through the Tee's base handler.
	sh := New(slog.New(tee.Base()), "h", nil, Options{FlushEvery: 5 * time.Millisecond, RetryBase: time.Millisecond, MaxAttempts: 2})
	sh.NewSender = func(model.LogTarget, string) (Sender, error) {
		calls.Add(1)
		return failing, nil
	}
	defer sh.Close()
	sh.Configure([]model.LogTarget{{ID: "a", Name: "a", Type: "http", Enabled: true, Sources: []string{"server"}, MinLevel: "debug"}})
	tee.Attach(sh)

	log.With("component", "proxy").WithGroup("req").Debug("debug line", "path", "/x")
	log.Warn("site crashed", "site", "s1")
	waitFor(t, "two failed records", func() bool { return sh.Status()[0].Failed == 2 })
	time.Sleep(50 * time.Millisecond)
	st := sh.Status()[0]
	if st.Queued != 0 || st.Failed != 2 {
		t.Errorf("the shipper's own warning was shipped: %+v", st)
	}
	fileMu.Lock()
	text := file.String()
	fileMu.Unlock()
	if strings.Contains(text, "debug line") {
		t.Error("the file got a debug line below its level")
	}
	if strings.Count(text, "log shipping failed") != 1 {
		t.Errorf("shipping errors in the log file are not rate-limited:\n%s", text)
	}

	ok := &fakeSender{}
	sh.NewSender = func(model.LogTarget, string) (Sender, error) { return ok, nil }
	sh.Configure([]model.LogTarget{{ID: "a", Name: "a2", Type: "http", Enabled: true, Sources: []string{"server"}, MinLevel: "debug"}})
	log.With("component", "proxy").WithGroup("req").Debug("debug line", "path", "/x")
	waitFor(t, "the debug line", func() bool { return len(ok.messages()) == 1 })
	r := ok.recs[0]
	if r.Level != "debug" || r.Attrs["component"] != "proxy" || r.Attrs["req.path"] != "/x" || r.Source != "server" {
		t.Errorf("record = %+v", r)
	}
}

type lockedWriter struct {
	b  *bytes.Buffer
	mu *sync.Mutex
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func TestAppLevel(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ stream, text, want string }{
		{"stdout", "listening", "info"},
		{"stderr", "(node:1) DeprecationWarning: x", "warning"},
		{"stderr", "TypeError: x is not a function", "error"},
		{"stderr", "    at Object.<anonymous> (/app/server.js:3:1)", "error"},
		{"system", "instance 0 started", "info"},
	} {
		if got := AppLevel(tc.stream, tc.text); got != tc.want {
			t.Errorf("%s %q = %s, want %s", tc.stream, tc.text, got, tc.want)
		}
	}
	if AccessLevel(503) != "error" || AccessLevel(404) != "warning" || AccessLevel(301) != "info" {
		t.Error("AccessLevel")
	}
}

func TestRecordLimits(t *testing.T) {
	t.Parallel()
	fake := &fakeSender{}
	sh := New(quiet, "h", nil, Options{FlushEvery: 5 * time.Millisecond})
	sh.NewSender = func(model.LogTarget, string) (Sender, error) { return fake, nil }
	defer sh.Close()
	sh.Configure([]model.LogTarget{{ID: "a", Name: "a", Type: "http", Enabled: true, Sources: []string{"access", "server"}, MinLevel: "debug"}})

	uri := "/search?q=" + strings.Repeat("é", 1<<20) // 2 MiB, multi-byte runes
	access := &AccessFields{Method: "GET", Path: uri, Status: 200, UserAgent: strings.Repeat("u", 1<<20), Host: "h"}
	sh.Ship(Record{Source: "access", Level: "info", Message: "GET " + uri, Access: access})
	attrs := map[string]string{"err": strings.Repeat("e", 1<<20)}
	for i := range 100 {
		attrs["k"+strconv.Itoa(i)] = "v"
	}
	sh.Ship(Record{Source: "server", Level: "info", Message: "small", Attrs: attrs})
	waitFor(t, "two records", func() bool { return len(fake.messages()) == 2 })

	fake.mu.Lock()
	a, s := fake.recs[0], fake.recs[1]
	fake.mu.Unlock()
	if len(a.Message) > maxMessage || !strings.HasSuffix(a.Message, truncMark) || !utf8.ValidString(a.Message) {
		t.Errorf("message: %d bytes, valid UTF-8 %v", len(a.Message), utf8.ValidString(a.Message))
	}
	if len(a.Access.Path) > maxField || !strings.HasSuffix(a.Access.Path, truncMark) || !utf8.ValidString(a.Access.Path) ||
		len(a.Access.UserAgent) > maxField || a.Access.Host != "h" || a.Access.Status != 200 {
		t.Errorf("access: path %d bytes, user agent %d bytes, %+v", len(a.Access.Path), len(a.Access.UserAgent), a.Access.Host)
	}
	if access.Path != uri {
		t.Error("the caller's access fields were changed")
	}
	if s.Message != "small" || len(s.Attrs) != maxAttrs+1 || s.Attrs["truncated"] != "true" || len(s.Attrs["err"]) > maxField {
		t.Errorf("attrs: %d, err %d bytes", len(s.Attrs), len(s.Attrs["err"]))
	}
}

// gatedSender blocks every Send until the gate is closed.
type gatedSender struct {
	fakeSender
	gate    chan struct{}
	batches [][]Record
}

func (g *gatedSender) Send(ctx context.Context, b []Record) error {
	select {
	case <-g.gate:
	case <-ctx.Done():
		return ctx.Err()
	}
	g.mu.Lock()
	g.batches = append(g.batches, b)
	g.mu.Unlock()
	return g.fakeSender.Send(ctx, b)
}

// Large records while a collector is down: the queue is bounded in bytes,
// not only in records, and a batch is bounded in bytes too.
func TestQueueAndBatchByteBudgets(t *testing.T) {
	t.Parallel()
	g := &gatedSender{gate: make(chan struct{})}
	sh := New(quiet, "h", nil, Options{QueueBytes: 256 << 10, BatchBytes: 40 << 10, FlushEvery: 5 * time.Millisecond, Timeout: time.Minute})
	sh.NewSender = func(model.LogTarget, string) (Sender, error) { return g, nil }
	defer sh.Close()
	sh.Configure([]model.LogTarget{{ID: "a", Name: "a", Type: "http", Enabled: true, Sources: []string{"app"}}})

	big := strings.Repeat("x", 1<<20)
	for i := range 200 {
		sh.Ship(Record{Source: "app", Level: "info", Message: strconv.Itoa(i) + " " + big})
	}
	w := sh.workers[0]
	w.mu.Lock()
	queued, bytes := w.n, w.bytes
	w.mu.Unlock()
	if bytes > 256<<10 || queued > 16 {
		t.Errorf("queued %d records, %d bytes: want at most 256 KiB", queued, bytes)
	}
	if st := sh.Status()[0]; st.Dropped < 150 {
		t.Errorf("status %+v", st)
	}

	close(g.gate)
	waitFor(t, "the queue to drain", func() bool { return sh.Status()[0].Queued == 0 })
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.batches) < 2 {
		t.Errorf("%d batches", len(g.batches))
	}
	for _, b := range g.batches {
		size := 0
		for i := range b {
			size += b[i].size()
		}
		if len(b) > 1 && size > 40<<10 {
			t.Errorf("a batch of %d records is %d bytes", len(b), size)
		}
	}
	if last := g.recs[len(g.recs)-1].Message; !strings.HasPrefix(last, "199 ") {
		t.Errorf("the newest record was not kept: %.10q", last)
	}
}
