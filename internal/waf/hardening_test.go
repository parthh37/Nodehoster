package waf

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// allocated returns the bytes f allocates.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func gzipped(s string) string {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	io.WriteString(w, s)
	w.Close()
	return b.String()
}

// TestDeepJSONBounded: 128 KB of {"a": used to keep every level's dotted
// name in its own frame, 700 MB of garbage for one request. Inspection now
// stops at maxJSONDepth and allocates about what the body weighs.
func TestDeepJSONBounded(t *testing.T) {
	body := strings.Repeat(`{"a":`, (128<<10)/5)
	for _, tc := range []struct {
		name string
		e    *Engine
	}{{"block", pl1()}, {"every rule, never stopping", all()}} {
		var res Result
		n := allocated(func() { res = tc.e.Inspect(browser("POST", "/api", body, "application/json")) })
		if n > 8<<20 {
			t.Errorf("%s: %d MB allocated for a 128 KB body", tc.name, n>>20)
		}
		if !has(res, 920205) {
			t.Errorf("%s: depth not reported: %v", tc.name, ruleIDs(res))
		}
	}

	// Past the depth the body is inspected as text: excluding 920205 does
	// not hide an attack below it.
	e := Compile(model.WAFConfig{Mode: model.WAFBlock, Exclusions: []model.WAFExclusion{{RuleIDs: []int{920205}}}})
	deep := strings.Repeat(`{"a":`, 100) + `"x' or '1'='1"` + strings.Repeat("}", 100)
	if res := e.Inspect(browser("POST", "/api", deep, "application/json")); !res.Exceeded() || has(res, 920205) {
		t.Errorf("attack below the depth limit: %v", ruleIDs(res))
	}

	// Within the limit, names are dotted as before, and long ones cut.
	long := strings.Repeat("k", 300)
	res := all().Inspect(browser("POST", "/api", `{"a":{"b":[{"c":"<script>"}]},"`+long+`":{"d":"x' or 'a'='a"}}`, "application/json"))
	var names []string
	for _, m := range res.Matches {
		if m.In == model.WAFInArg && !slices.Contains(names, m.Name) {
			names = append(names, m.Name)
		}
	}
	if len(names) != 2 || names[0] != "a.b.c" || names[1] != snippetName(long[:maxNameLen]) {
		t.Errorf("names %q", names)
	}
}

// TestValueLimitFailsClosed: past the value limits inspection used to stop
// silently, and 920210 (3) did not reach the threshold, so padding a
// request let an attack after the padding through.
func TestValueLimitFailsClosed(t *testing.T) {
	sqli := "user=" + q("x' or '1'='1")
	headers := func(r *http.Request, n int) *http.Request {
		for i := 0; i < n; i++ {
			r.Header.Set(fmt.Sprintf("X-Pad-%d", i), "1")
		}
		return r
	}

	// Headers do not use up the arguments' allowance.
	r := headers(browser("POST", "/login", sqli, "application/x-www-form-urlencoded"), 1000)
	if res := pl1().Inspect(r); !res.Exceeded() || res.Incomplete {
		t.Errorf("1000 headers: attack in the body not seen: %v", ruleIDs(res))
	}
	// Past the header limit, the request fails closed whatever the
	// threshold, even with nothing else wrong with it.
	high := Compile(model.WAFConfig{Mode: model.WAFBlock, AnomalyThreshold: 1000})
	r = headers(browser("POST", "/login", "user=bob", "application/x-www-form-urlencoded"), 1030)
	if res := high.Inspect(r); !res.Exceeded() || !res.Incomplete || !has(res, 920210) {
		t.Errorf("1030 headers: %+v", res)
	}

	// Arguments: 520 pads and an attack are inspected whole...
	pad := strings.Repeat("a=1&", 520)
	if res := pl1().Inspect(browser("GET", "/x?"+pad+sqli, "", "")); !res.Exceeded() || res.Incomplete {
		t.Errorf("520 pads: %v", ruleIDs(res))
	}
	// ...past the limit the request fails closed...
	pad = strings.Repeat("a=1&", maxArgs)
	if res := high.Inspect(browser("GET", "/x?"+pad+sqli, "", "")); !res.Exceeded() || !res.Incomplete {
		t.Errorf("%d pads: %+v", maxArgs, res)
	}
	// ...in a body too...
	if res := high.Inspect(browser("POST", "/x", pad+sqli, "application/x-www-form-urlencoded")); !res.Incomplete {
		t.Errorf("padded body: %+v", res)
	}
	// ...and with 920210 excluded, everything is inspected.
	e := Compile(model.WAFConfig{Mode: model.WAFBlock, Exclusions: []model.WAFExclusion{{Path: "/bulk", RuleIDs: []int{920210}}}})
	if res := e.Inspect(browser("GET", "/bulk?"+pad+sqli, "", "")); !res.Exceeded() || res.Incomplete || has(res, 920210) {
		t.Errorf("920210 excluded: %+v", res)
	}
	if res := e.Inspect(browser("GET", "/bulk?"+pad, "", "")); res.Exceeded() {
		t.Errorf("920210 excluded, no attack: %+v", res)
	}

	// The work budget fails closed too.
	chunk := `select union from where or and ' " < on= ${ {{ ../ ; | && $( javascript `
	e = all()
	e.budget = 1 << 20
	if res := e.Inspect(browser("POST", "/x", strings.Repeat(chunk, (128<<10)/len(chunk)), "text/plain")); !res.Incomplete || !res.Exceeded() {
		t.Errorf("budget: score %d/%d, incomplete %v", res.Score, res.Threshold, res.Incomplete)
	}
}

// TestBodyParseMismatches: bodies the firewall used to skip that Node.js
// applications read.
func TestBodyParseMismatches(t *testing.T) {
	attack := `{"q":"x' or '1'='1"}`

	// A Content-Type Go cannot parse: inspected as text.
	res := pl1().Inspect(browser("POST", "/api", attack, "application/json;;"))
	if !res.Exceeded() || !has(res, 920190) {
		t.Errorf("unparsable Content-Type: %v", ruleIDs(res))
	}

	// A multipart field with a Content-Type but no file name is a field.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="bio"`)
	h.Set("Content-Type", "application/octet-stream")
	w, _ := mw.CreatePart(h)
	io.WriteString(w, "<script>alert(1)</script>")
	mw.Close()
	if res := pl1().Inspect(browser("POST", "/u", buf.String(), mw.FormDataContentType())); !res.Exceeded() {
		t.Error("multipart field with a binary Content-Type not inspected")
	}

	// gzip and deflate (zlib or raw) are inflated; the application gets
	// the body as sent.
	var z, raw bytes.Buffer
	zw := zlib.NewWriter(&z)
	io.WriteString(zw, attack)
	zw.Close()
	fw, _ := flate.NewWriter(&raw, flate.DefaultCompression)
	io.WriteString(fw, attack)
	fw.Close()
	for enc, body := range map[string]string{"gzip": gzipped(attack), "x-gzip": gzipped(attack), "deflate": z.String(), "Deflate ": raw.String()} {
		r := browser("POST", "/api", body, "application/json")
		r.Header.Set("Content-Encoding", enc)
		if res := pl1().Inspect(r); !res.Exceeded() || res.Incomplete {
			t.Errorf("%s: %+v", enc, res)
		}
		if got, _ := io.ReadAll(r.Body); string(got) != body {
			t.Errorf("%s: the application got a different body", enc)
		}
	}

	// A zip bomb inflates no further than the limit.
	bomb := gzipped(strings.Repeat("\x00", 64<<20))
	var r *http.Request
	n := allocated(func() {
		r = browser("POST", "/api", bomb, "text/plain")
		r.Header.Set("Content-Encoding", "gzip")
		pl1().Inspect(r)
	})
	if n > 4<<20 {
		t.Errorf("64 MB gzip bomb: %d MB allocated", n>>20)
	}
	if got, _ := io.ReadAll(r.Body); len(got) != len(bomb) {
		t.Error("bomb not replayed as sent")
	}

	// Not gzip after all: reported, and what there is inspected.
	r = browser("POST", "/api", "not gzip", "text/plain")
	r.Header.Set("Content-Encoding", "gzip")
	if res := all().Inspect(r); !has(res, 920200) {
		t.Errorf("invalid gzip: %v", ruleIDs(res))
	}

	// An encoding the rules cannot read fails closed, unless excluded.
	for _, enc := range []string{"br", "zstd", "gzip, gzip"} {
		r = browser("POST", "/api", "whatever", "application/json")
		r.Header.Set("Content-Encoding", enc)
		if res := Compile(model.WAFConfig{Mode: model.WAFBlock, AnomalyThreshold: 1000}).Inspect(r); !res.Incomplete || !has(res, 920240) {
			t.Errorf("%s: %+v", enc, res)
		}
	}
	e := Compile(model.WAFConfig{Mode: model.WAFBlock, Exclusions: []model.WAFExclusion{{RuleIDs: []int{920240}}}})
	r = browser("POST", "/api", "whatever", "application/json")
	r.Header.Set("Content-Encoding", "br")
	if res := e.Inspect(r); res.Exceeded() {
		t.Errorf("920240 excluded: %+v", res)
	}
	// A binary body is not inspected, encoded or not.
	r = browser("POST", "/api", "whatever", "image/png")
	r.Header.Set("Content-Encoding", "br")
	if res := pl1().Inspect(r); res.Exceeded() {
		t.Errorf("brotli image: %+v", res)
	}
}

// TestExclusionPaths: an exclusion's path covers whole segments of the
// path as sent and as resolved, without regard to case.
func TestExclusionPaths(t *testing.T) {
	for _, tc := range []struct {
		excl, path string
		excluded   bool
	}{
		{"/hooks/", "/hooks/github", true},
		{"/hooks/", "/hooks", true},
		{"/hooks", "/hooks/github", true},
		{"/hooks/", "/HOOKS/github", true},
		{"/hooks/", "//hooks//github", true},
		{"/hooks/", "/hooks/./github", true},
		{"/Hooks", "/hooks/x", true},
		{"/hooks/", "/hooks/../login", false},
		{"/hooks/", "/hooks/%2e%2e/login", false},
		{"/hooks/", "/login/../hooks/x", false},
		{"/hooks/", "/hooksx", false},
		{"/api", "/api-admin", false},
		{"/api", "/api/x", true},
		{"/", "/anything", true},
		{"/a/b/../c", "/a/c/x", true},
	} {
		e := Compile(model.WAFConfig{Mode: model.WAFBlock, Exclusions: []model.WAFExclusion{{Path: tc.excl}}})
		res := e.Inspect(browser("GET", tc.path+"?content="+q("x' or 'a'='a"), "", ""))
		if excluded := res.Score == 0; excluded != tc.excluded {
			t.Errorf("exclusion %q, path %q: excluded %v, matches %v", tc.excl, tc.path, excluded, ruleIDs(res))
		}
	}
}

func TestSensitiveNames(t *testing.T) {
	for name, want := range map[string]bool{
		"connect.sid": true, "sid": true, "my_sid": true, "session": true, "PHPSESSID": true, "JSESSIONID": true,
		"ASP.NET_SessionId": true, "__Host-next-auth.session-token": true, "password": true,
		"q": false, "side": false, "consider": false, "theme": false,
	} {
		if got := sensitiveName(name); got != want {
			t.Errorf("sensitiveName(%q) = %v", name, got)
		}
	}
	r := browser("GET", "/", "", "")
	r.Header.Set("Cookie", "connect.sid="+q("<script>x</script>"))
	res := pl1().Inspect(r)
	if len(res.Matches) == 0 || res.Matches[0].Snippet != redacted {
		t.Errorf("session cookie: %+v", res.Matches)
	}
}

func TestBodySnippetsRedacted(t *testing.T) {
	for _, tc := range []struct {
		body, ct string
		redact   bool
	}{
		{"user=bob&password=x' or '1'='1", "text/plain", true},
		{`{user:"bob","password":"x' or '1'='1"}`, "application/json", true}, // invalid: inspected as text
		{"user=bob&q=x' or '1'='1", "text/plain", false},
		{`{q:1,"password":"s3cret","q":"x' or '1'='1"}`, "application/json", false},
	} {
		res := pl1().Inspect(browser("POST", "/login", tc.body, tc.ct))
		var m *model.WAFMatch
		for i := range res.Matches {
			if res.Matches[i].In == model.WAFInBody && res.Matches[i].RuleID != 920200 {
				m = &res.Matches[i]
				break
			}
		}
		switch {
		case m == nil:
			t.Errorf("%s: no body match: %+v", tc.body, res.Matches)
		case (m.Snippet == redacted) != tc.redact:
			t.Errorf("%s: snippet %q", tc.body, m.Snippet)
		}
	}
	if got := redactPairs(`&password=hunter2&q=' or 1=1&api_key: "abc"`); got != `&password=[redacted]&q=' or 1=1&api_key: "[redacted]"` {
		t.Errorf("redactPairs = %q", got)
	}
}

func TestRedactPath(t *testing.T) {
	for in, want := range map[string]string{
		"/reset/9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08":                              "/reset/[redacted]",
		"/auth/eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U/x": "/auth/[redacted]/x",
		"/blog/how-to-deploy-node-apps-on-windows-in-2024":                                                     "/blog/how-to-deploy-node-apps-on-windows-in-2024",
		"/orders/550e8400-e29b-41d4-a716-446655440000":                                                         "/orders/550e8400-e29b-41d4-a716-446655440000",
		"/products/shoes": "/products/shoes",
	} {
		if got := redactPath(in); got != want {
			t.Errorf("redactPath(%q) = %q", in, got)
		}
	}
	r := browser("GET", "/reset/9f86d081884c7d659a2feaa0c55ad015a3bf4f1b?x=1", "", "")
	ev := NewEvent(r, "id", "site@staging", "203.0.113.1", model.WAFActionBlocked, Result{}, time.Now())
	if ev.Path != "/reset/[redacted]" || ev.SiteID != "site" || ev.Slot != "staging" {
		t.Errorf("event %+v", ev)
	}
	if x := ev.SuggestExclusion(); x.Path != "/reset/" {
		t.Errorf("suggested path %q", x.Path)
	}
}

// TestBufferBudget: bodies held for inspection share a server-wide
// budget; spent, block mode refuses (Busy) and detect mode passes the body
// on uninspected. Reading the body, closing it or the request ending gives
// the share back.
func TestBufferBudget(t *testing.T) {
	// Other tests leave bodies unread, their requests never ending.
	base := buffered.used.Load()
	prev := SetMaxBufferedBytes(base + 4<<10)
	defer SetMaxBufferedBytes(prev)
	body := `{"a":"` + strings.Repeat("x", 3<<10) + `"}`

	ctx, cancel := context.WithCancel(context.Background())
	held := browser("POST", "/", body, "application/json").WithContext(ctx)
	if res := pl1().Inspect(held); res.Busy || res.Exceeded() {
		t.Fatalf("first body: %+v", res)
	}
	r := browser("POST", "/", body, "application/json")
	if res := pl1().Inspect(r); !res.Busy {
		t.Errorf("over budget, block mode: %+v", res)
	}
	r = browser("POST", "/", body, "application/json")
	orig := r.Body
	if res := Compile(model.WAFConfig{Mode: model.WAFDetect}).Inspect(r); res.Busy || res.Exceeded() || r.Body != orig {
		t.Errorf("over budget, detect mode: %+v, buffered %v", res, r.Body != orig)
	}
	cancel() // the request ended
	deadline := time.Now().Add(2 * time.Second)
	for buffered.used.Load() != base && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if u := buffered.used.Load(); u != base {
		t.Fatalf("budget not given back at the request's end: %d", u-base)
	}

	// Read to the end, or closed.
	r = browser("POST", "/", body, "application/json")
	pl1().Inspect(r)
	io.ReadAll(r.Body)
	r2 := browser("POST", "/", body, "application/json")
	if res := pl1().Inspect(r2); res.Busy {
		t.Error("budget not given back after reading")
	}
	r2.Body.Close()
	if u := buffered.used.Load(); u != base {
		t.Errorf("budget not given back after closing: %d", u-base)
	}
}
