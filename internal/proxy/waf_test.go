package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/ipban"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/waf"
)

// wafEnv is a proxy site in front of an upstream that reports what it
// received, with the firewall's recorder and IP banning attached.
type wafEnv struct {
	rt       *siteRuntime
	hits     atomic.Int64
	mu       sync.Mutex
	bodySum  [32]byte
	bodyLen  int
	events   []model.WAFEvent
	bans     *ipban.Manager
	recorder *waf.Recorder
}

func newWAFEnv(t *testing.T, cfg model.WAFConfig, mutate ...func(*model.Site)) *wafEnv {
	t.Helper()
	e := &wafEnv{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		e.mu.Lock()
		e.bodySum, e.bodyLen = sha256.Sum256(b), len(b)
		e.mu.Unlock()
		io.WriteString(w, "app")
	}))
	t.Cleanup(up.Close)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	e.recorder = waf.NewRecorder(waf.RecorderOptions{Save: func(_ context.Context, evs []model.WAFEvent) error {
		e.mu.Lock()
		e.events = append(e.events, evs...)
		e.mu.Unlock()
		return nil
	}})
	go func() { e.recorder.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	e.bans = ipban.New(ipban.Options{})
	b := model.DefaultIPBan()
	b.Enabled = true
	b.WAFBlocks = model.BanRule{Threshold: 2, WindowSec: 60}
	e.bans.Apply(b, nil)

	s := affServer(t)
	s.deps.WAF, s.deps.Bans = e.recorder, e.bans
	site := &model.Site{ID: "w1", Name: "shop", Type: model.SiteProxy, Proxy: &model.ProxyConfig{Upstreams: []model.Upstream{{URL: up.URL}}}}
	site.Routing.WAF = cfg
	for _, m := range mutate {
		m(site)
	}
	e.rt = compileTest(t, s, site)
	return e
}

func (e *wafEnv) do(method, target, body, ct string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	req.RemoteAddr = "203.0.113.50:40000"
	req.Header.Set("User-Agent", "Mozilla/5.0 test")
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	rec := httptest.NewRecorder()
	e.rt.ServeHTTP(rec, req)
	return rec
}

// waitEvents waits for the recorder's writer to save n events.
func (e *wafEnv) waitEvents(t *testing.T, n int) []model.WAFEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		evs := append([]model.WAFEvent(nil), e.events...)
		e.mu.Unlock()
		if len(evs) >= n {
			return evs
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("fewer than %d events saved", n)
	return nil
}

func TestWAFBlocks(t *testing.T) {
	e := newWAFEnv(t, model.WAFConfig{Mode: model.WAFBlock})
	attack := "/search?q=" + url.QueryEscape("1' UNION SELECT password FROM users--")

	rec := e.do("GET", attack, "", "")
	id := rec.Header().Get("X-Request-Id")
	if rec.Code != http.StatusForbidden || len(id) != 16 || !strings.Contains(rec.Body.String(), id) || e.hits.Load() != 0 {
		t.Fatalf("attack: %d, id %q, upstream hits %d, body %s", rec.Code, id, e.hits.Load(), rec.Body)
	}
	if rec := e.do("GET", "/search?q=running+shoes", "", ""); rec.Code != 200 || e.hits.Load() != 1 {
		t.Fatalf("ordinary request: %d", rec.Code)
	}
	evs := e.waitEvents(t, 1)
	ev := evs[0]
	if ev.ID != id || ev.Action != model.WAFActionBlocked || ev.SiteID != "w1" || ev.ClientIP != "203.0.113.50" || ev.Path != "/search" || ev.Score < 5 || len(ev.Matches) == 0 {
		t.Errorf("event %+v", ev)
	}
	st := e.recorder.Snapshot()["w1"]
	if st.Inspected != 2 || st.Blocked != 1 || st.Matches[model.WAFSQLi] == 0 {
		t.Errorf("counters %+v", st)
	}

	// The second block within the window bans the address (rule: 2 in 60 s).
	ip := net.ParseIP("203.0.113.50")
	if e.bans.Banned(ip) {
		t.Fatal("banned after one block")
	}
	e.do("GET", attack, "", "")
	if !e.bans.Banned(ip) {
		t.Fatal("not banned after two blocks")
	}
}

func TestWAFDetectPasses(t *testing.T) {
	e := newWAFEnv(t, model.WAFConfig{Mode: model.WAFDetect})
	rec := e.do("POST", "/api/comments", `{"text":"<script>alert(1)</script>"}`, "application/json")
	if rec.Code != 200 || e.hits.Load() != 1 {
		t.Fatalf("detect mode: %d, upstream hits %d", rec.Code, e.hits.Load())
	}
	if ev := e.waitEvents(t, 1)[0]; ev.Action != model.WAFActionDetected {
		t.Errorf("event %+v", ev)
	}
	if st := e.recorder.Snapshot()["w1"]; st.Detected != 1 || st.Blocked != 0 {
		t.Errorf("counters %+v", st)
	}
	if e.bans.Banned(net.ParseIP("203.0.113.50")) {
		t.Error("a detection counted towards a ban")
	}
}

// TestWAFBodyReachesApplication: the upstream gets every byte of a body,
// inspected or not, past the inspection limit or not.
func TestWAFBodyReachesApplication(t *testing.T) {
	e := newWAFEnv(t, model.WAFConfig{Mode: model.WAFBlock, InspectBodyKB: 16})
	for _, tc := range []struct {
		name, ct string
		size     int
	}{
		{"small json", "application/json", 1 << 10},
		{"json past the limit", "application/json", 3 << 20},
		{"binary", "application/octet-stream", 2 << 20},
		{"form at the limit", "application/x-www-form-urlencoded", 16 << 10},
	} {
		var body string
		switch tc.ct {
		case "application/json":
			body = `{"data":"` + strings.Repeat("abcdefgh", tc.size/8) + `"}`
		case "application/x-www-form-urlencoded":
			body = "a=" + strings.Repeat("x", tc.size-2)
		default:
			b := make([]byte, tc.size)
			for i := range b {
				b[i] = byte(i * 7)
			}
			body = string(b)
		}
		rec := e.do("POST", "/upload", body, tc.ct)
		e.mu.Lock()
		sum, n := e.bodySum, e.bodyLen
		e.mu.Unlock()
		if rec.Code != 200 || n != len(body) || sum != sha256.Sum256([]byte(body)) {
			t.Errorf("%s: %d, upstream got %d of %d bytes", tc.name, rec.Code, n, len(body))
		}
	}
}

// TestWAFOffAndExempt: a site without the firewall inspects nothing; an
// exempt site's blocks do not count towards bans.
func TestWAFOffAndExempt(t *testing.T) {
	e := newWAFEnv(t, model.WAFConfig{})
	if e.rt.waf != nil || e.rt.wafStats != nil {
		t.Fatal("firewall compiled while off")
	}
	if rec := e.do("GET", "/?q=%3Cscript%3E", "", ""); rec.Code != 200 {
		t.Fatalf("off: %d", rec.Code)
	}

	e = newWAFEnv(t, model.WAFConfig{Mode: model.WAFBlock}, func(s *model.Site) { s.Routing.Banning.Exempt = true })
	for i := 0; i < 5; i++ {
		if rec := e.do("GET", "/?q=%3Cscript%3E", "", ""); rec.Code != http.StatusForbidden {
			t.Fatalf("exempt site did not block: %d", rec.Code)
		}
	}
	if e.bans.Banned(net.ParseIP("203.0.113.50")) {
		t.Error("an exempt site's blocks banned the client")
	}
}

// TestWAFCustomErrorPage: the site's own 403 page is used; the request ID
// is still in the header.
func TestWAFCustomErrorPage(t *testing.T) {
	e := newWAFEnv(t, model.WAFConfig{Mode: model.WAFBlock}, func(s *model.Site) {
		s.Routing.ErrorPages = map[string]string{"403": "<h1>Nope</h1>"}
	})
	rec := e.do("GET", "/?q=%3Cscript%3E", "", "")
	if rec.Code != 403 || rec.Body.String() != "<h1>Nope</h1>" || rec.Header().Get("X-Request-Id") == "" {
		t.Errorf("%d %q %q", rec.Code, rec.Body, rec.Header().Get("X-Request-Id"))
	}
}

// TestSmugglingNeverReachesTheSite: Go's server removes Content-Length
// from a chunked request (RFC 9112), so the conflict the firewall's
// protocol rules would look for is gone before any site sees it.
func TestSmugglingNeverReachesTheSite(t *testing.T) {
	var got http.Header
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		body, _ = io.ReadAll(r.Body)
	}))
	defer srv.Close()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 4\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n0\r\n\r\n")
	buf := make([]byte, 512)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, _ := conn.Read(buf)
	if !bytes.HasPrefix(buf[:n], []byte("HTTP/1.1 200")) || got.Get("Content-Length") != "" || string(body) != "hello" {
		t.Errorf("response %q, Content-Length %q, body %q", buf[:n], got.Get("Content-Length"), body)
	}
}
