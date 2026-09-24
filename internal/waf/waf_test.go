package waf

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
)

// all runs every rule (paranoia 3) and never stops early.
func all() *Engine {
	return Compile(model.WAFConfig{Mode: model.WAFDetect, ParanoiaLevel: 3, AnomalyThreshold: 1000})
}

func ruleIDs(res Result) []int {
	var out []int
	for _, m := range res.Matches {
		out = append(out, m.RuleID)
	}
	return out
}

func has(res Result, id int) bool {
	for _, m := range res.Matches {
		if m.RuleID == id {
			return true
		}
	}
	return false
}

// TestEveryRuleFires: each rule of the catalog has a request it catches.
func TestEveryRuleFires(t *testing.T) {
	get := func(target string) func() *http.Request {
		return func() *http.Request { return browser("GET", target, "", "") }
	}
	arg := func(v string) func() *http.Request { return get("/x?a=" + q(v)) }
	ua := func(v string) func() *http.Request {
		return func() *http.Request { r := browser("GET", "/", "", ""); r.Header.Set("User-Agent", v); return r }
	}
	cases := map[int]func() *http.Request{
		913100: ua("Mozilla/5.0 (compatible; Nmap Scripting Engine; https://nmap.org/book/nse.html)"),
		913110: func() *http.Request {
			r := browser("GET", "/", "", "")
			r.Header.Set("X-Scan-Memo", "Category=\"Audit\"")
			return r
		},
		913120: ua("python-requests/2.31.0"),
		920100: get("/x?a=abc%00def"),
		920110: get("/x?a%01b=1"),
		920115: arg("line\x07bell"),
		920120: get("/x?a=%zz"),
		920130: get("/x?a=%u0041"),
		920140: get("/x?a=%2541"),
		920160: func() *http.Request {
			r := httptest.NewRequest("GET", "/", strings.NewReader("body"))
			r.Header.Set("User-Agent", browserUA)
			return r
		},
		920170: func() *http.Request { r := browser("GET", "/", "", ""); r.Method = "TRACK"; return r },
		920180: func() *http.Request { return httptest.NewRequest("GET", "/", nil) },
		920190: func() *http.Request { return browser("POST", "/", "x", "text/plain; ===") },
		920200: func() *http.Request { return browser("POST", "/", `{"a":`+"\x01}", "application/json") },
		920210: func() *http.Request { return get("/x?" + strings.Repeat("a=1&", maxValues+10))() },
		920220: nil, // TestWorkBudget
		930100: arg("../../x"),
		930110: get("/x?a=..%c0%afetc"),
		930120: arg("/etc/shadow"),
		930130: get("/.git/HEAD"),
		930140: get("/app.js::$DATA"),
		930150: get("/PROGRA~1/x"),
		930160: get("/files/con.txt"),
		931100: arg("expect://id"),
		931110: arg("http://evil.example/shell.txt?"),
		931120: arg("http://10.1.2.3/x"),
		932100: arg("x; ls -la"),
		932105: arg("x | bash -i"),
		932110: arg("cmd.exe /c whoami"),
		932120: arg("$(id)"),
		932125: arg("`uname -a`"),
		932130: arg("() { :; }; id"),
		932140: arg("/usr/bin/python3 -c x"),
		932160: func() *http.Request { return multipartReq("/u", nil, "f", "x.aspx", "a") },
		933100: arg("<?= $x ?>"),
		933110: func() *http.Request { return multipartReq("/u", nil, "f", "a.phtml", "a") },
		933120: arg("allow_url_include=On"),
		933130: arg("$_SERVER['x']"),
		933140: arg("base64_decode('eA==')"),
		933145: arg("system('id')"),
		933150: arg(`a:1:{i:0;O:4:"Test":0:{}}`),
		934100: arg("require('child_process')"),
		934105: arg("process.env.SECRET"),
		934110: arg("x.constructor.prototype.y"),
		934120: arg("http://metadata.google.internal/computeMetadata/v1/"),
		934130: arg("http://127.0.0.1:8080/admin"),
		934140: arg("<%= process.mainModule %>"),
		934150: arg("{{7*7}}"),
		934160: arg("[]['constructor']"),
		941100: arg("<script src=//x>"),
		941110: arg(`<body onload=x()>`),
		941120: arg(`'onmouseover='x'`),
		941125: arg(` onclick=x`),
		941130: arg("vbscript:msgbox(1)"),
		941135: arg("java\nscript:x"),
		941140: arg(`<object data=x>`),
		941150: arg("x=document.cookie"),
		941160: arg("<svg>"),
		941170: arg("width:expression(alert(1))"),
		941200: arg("<b>"),
		942100: arg("x' or 'a'='a"),
		942110: arg("x' or true--"),
		942120: arg("1 and 1=1"),
		942130: arg("1 union all select 1"),
		942140: arg("1; truncate table x"),
		942150: arg("select count(*) from users"),
		942155: arg("insert into users (a) values (1)"),
		942160: arg("pg_sleep(10)"),
		942170: arg("select @@version"),
		942180: arg("load_file('/etc/x')"),
		942190: arg("admin' #"),
		942195: arg("/*!12345select*/"),
		942200: arg("'a'||'b'"),
		942210: get("/x?a[$where]=1"),
		942220: arg("delete x where y"),
		942230: arg("select a, b from t"),
		942240: arg("drop table t"),
		942300: arg("x -- y"),
		944100: arg("${jndi:dns://x}"),
		944105: arg("${${env:X:-j}}"),
		944110: arg("java.lang.ProcessBuilder"),
		944120: arg("rO0ABXNy"),
		944130: get("/x?class.classLoader.x=1"),
		944140: arg("%{#_memberaccess}"),
	}
	e := all()
	for _, r := range Rules() {
		mk, ok := cases[r.ID]
		if !ok {
			t.Errorf("rule %d has no test case", r.ID)
			continue
		}
		if mk == nil {
			continue
		}
		if res := e.Inspect(mk()); !has(res, r.ID) {
			t.Errorf("rule %d (%s) did not fire; matched %v", r.ID, r.Message, ruleIDs(res))
		}
	}
	for id := range cases {
		if _, ok := Rule(id); !ok {
			t.Errorf("case for rule %d, which does not exist", id)
		}
	}
}

func TestDecode(t *testing.T) {
	for in, want := range map[string]string{
		"a+b%20c":             "a b c",
		"%2527":               "'",
		"%u0027%U003C":        "'<",
		"&lt;&#60;&#x3c;&#60": "<<<<",
		"%26lt%3B":            "<",
		`\x3c<\u{3c}`:         "<<<",
		"100% sure %zz %u12":  "100% sure %zz %u12",
		"%":                   "%",
		"&unknown;":           "&unknown;",
	} {
		if got := decode(in, true); got != want {
			t.Errorf("decode(%q) = %q, want %q", in, got, want)
		}
	}
	if got := decode("a+b", false); got != "a+b" {
		t.Errorf("plus decoded outside forms: %q", got)
	}
}

func TestFold(t *testing.T) {
	for in, want := range map[string]string{
		"SeLeCt":                   "select",
		"＜ＳＣＲＩＰＴ＞":                 "<script>",
		"a\x00b":                   "ab",
		"\xc0\xae\xc0\xae\xc0\xaf": "../",
		"\xe0\x80\xae":             ".",
		"ÄÖ":                       "äö",
		"x\xffy":                   "x\xffy", // invalid UTF-8 kept, not U+FFFD
		"∕":                        "/",
	} {
		if got, _ := fold(in); got != want {
			t.Errorf("fold(%q) = %q, want %q", in, got, want)
		}
	}
	if _, nul := fold("a\x00"); !nul {
		t.Error("NUL not reported")
	}
	s := "plain lowercase ascii"
	if got, _ := fold(s); got != s {
		t.Error("clean string changed")
	}
}

func TestSQLForm(t *testing.T) {
	for in, want := range map[string]string{
		"union/**/select":         "union select",
		"union/*x*/all/**/select": "union all select",
		"union--x\nselect":        "union select",
		"union#x\nselect":         "union select",
		"a -- b":                  "a -- b",
		"a/*unterminated":         "a ",
	} {
		if got := sqlForm(in); got != want {
			t.Errorf("sqlForm(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSnippet(t *testing.T) {
	if got := snippet("a\nb\x00c‮d"); got != `a\nb\u0000c\u202ed` {
		t.Errorf("snippet = %q", got)
	}
	long := strings.Repeat("é", 100)
	got := snippet(long)
	if !strings.HasSuffix(got, "…") || len(got) > maxSnippet+len("…") || !strings.HasPrefix(got, "éé") {
		t.Errorf("long snippet = %q", got)
	}
	if got := snippet("a\xffb"); got != "a�b" {
		t.Errorf("invalid UTF-8 = %q", got)
	}
}

// TestAutomaton compares the literal scanner with strings.Contains.
func TestAutomaton(t *testing.T) {
	lits := []string{"he", "she", "his", "hers", "a", "aa", "abc", "bca", "%u", "${", "::$"}
	a := buildAutomaton(lits)
	rng := rand.New(rand.NewSource(1))
	alphabet := "hesiracb%u${:$ x"
	for i := 0; i < 2000; i++ {
		b := make([]byte, rng.Intn(20))
		for j := range b {
			b[j] = alphabet[rng.Intn(len(alphabet))]
		}
		s := string(b)
		var found litSet
		a.scan(s, &found)
		for li, l := range lits {
			var one litSet
			one.add(li)
			if got := found.intersects(&one); got != strings.Contains(s, l) {
				t.Fatalf("scan(%q): literal %q found=%v", s, l, got)
			}
		}
	}
}

func TestOffCompilesToNil(t *testing.T) {
	for _, m := range []string{"", model.WAFOff} {
		if Compile(model.WAFConfig{Mode: m}) != nil {
			t.Errorf("mode %q compiled", m)
		}
	}
}

func TestExclusions(t *testing.T) {
	sqli := func(path string) *http.Request { return browser("GET", path+"?content="+q("x' or 'a'='a"), "", "") }
	for _, tc := range []struct {
		name  string
		ex    model.WAFExclusion
		path  string
		clean bool
	}{
		{"rule everywhere", model.WAFExclusion{RuleIDs: []int{942100}}, "/a", true},
		{"rule under path", model.WAFExclusion{Path: "/admin", RuleIDs: []int{942100}}, "/admin/posts", true},
		{"rule under other path", model.WAFExclusion{Path: "/admin", RuleIDs: []int{942100}}, "/shop", false},
		{"other rule", model.WAFExclusion{RuleIDs: []int{941100}}, "/a", false},
		{"category", model.WAFExclusion{Categories: []string{model.WAFSQLi}}, "/a", true},
		{"arg from all rules", model.WAFExclusion{Args: []string{"CONTENT"}}, "/a", true},
		{"arg prefix", model.WAFExclusion{Args: []string{"cont*"}}, "/a", true},
		{"other arg", model.WAFExclusion{Args: []string{"title"}}, "/a", false},
		{"arg from other rule", model.WAFExclusion{Args: []string{"content"}, RuleIDs: []int{941100}}, "/a", false},
		{"arg from its rule", model.WAFExclusion{Args: []string{"content"}, RuleIDs: []int{942100}}, "/a", true},
		{"off under path", model.WAFExclusion{Path: "/webhooks/"}, "/webhooks/github", true},
		{"cookie name does not cover args", model.WAFExclusion{Cookies: []string{"content"}}, "/a", false},
	} {
		e := Compile(model.WAFConfig{Mode: model.WAFBlock, Exclusions: []model.WAFExclusion{tc.ex}})
		res := e.Inspect(sqli(tc.path))
		if clean := res.Score == 0; clean != tc.clean {
			t.Errorf("%s: score %d, matches %v", tc.name, res.Score, ruleIDs(res))
		}
	}

	// Cookies and headers.
	e := Compile(model.WAFConfig{Mode: model.WAFBlock, Exclusions: []model.WAFExclusion{{Cookies: []string{"prefs"}, Headers: []string{"X-Template"}}}})
	r := browser("GET", "/", "", "")
	r.Header.Set("Cookie", "prefs="+q("<script>x</script>"))
	r.Header.Set("X-Template", "${jndi:ldap://x}")
	if res := e.Inspect(r); res.Score != 0 {
		t.Errorf("excluded cookie and header scored: %v", ruleIDs(res))
	}
	r.Header.Set("Cookie", "other="+q("<script>x</script>"))
	if res := e.Inspect(r); res.Score == 0 {
		t.Error("cookie not excluded scored nothing")
	}

	// A request-level rule excluded by ID.
	e = Compile(model.WAFConfig{Mode: model.WAFBlock, Exclusions: []model.WAFExclusion{{RuleIDs: []int{920170}}}})
	r = browser("GET", "/", "", "")
	r.Method = "TRACE"
	if res := e.Inspect(r); res.Score != 0 {
		t.Errorf("excluded request rule scored: %v", ruleIDs(res))
	}
}

func TestParanoiaAndThreshold(t *testing.T) {
	r := func() *http.Request { return browser("GET", "/x?a="+q("<b>bold</b>"), "", "") }
	if res := pl1().Inspect(r()); res.Score != 0 {
		t.Errorf("PL1 scored markup: %v", ruleIDs(res))
	}
	res := Compile(model.WAFConfig{Mode: model.WAFBlock, ParanoiaLevel: 3}).Inspect(r())
	if !has(res, 941200) || res.Exceeded() {
		t.Errorf("PL3: %d %v", res.Score, ruleIDs(res))
	}
	res = Compile(model.WAFConfig{Mode: model.WAFBlock, ParanoiaLevel: 3, AnomalyThreshold: 3}).Inspect(r())
	if !res.Exceeded() {
		t.Error("threshold 3 not exceeded by a warning")
	}
}

// TestEarlyExit: once the threshold is reached nothing more is inspected
// (the body is not even read).
func TestEarlyExit(t *testing.T) {
	r := browser("POST", "/x?a="+q("<script>"), `{"b":"' or 1=1--"}`, "application/json")
	orig := r.Body
	res := pl1().Inspect(r)
	if len(res.Matches) != 1 || res.Matches[0].RuleID != 941100 {
		t.Errorf("matches %v", ruleIDs(res))
	}
	if r.Body != orig {
		t.Error("body read after the verdict")
	}
}

func TestWorkBudget(t *testing.T) {
	chunk := `select union from where or and ' " < on= ${ {{ ../ ; | && $( javascript `
	body := strings.Repeat(chunk, (128<<10)/len(chunk))
	e := all()
	e.budget = 1 << 20 // the default, 4 MB, takes a larger body than a test should
	res := e.Inspect(browser("POST", "/x", body, "text/plain"))
	if !has(res, 920220) {
		t.Errorf("budget not enforced: %v", ruleIDs(res))
	}
}

func TestSensitiveSnippets(t *testing.T) {
	res := pl1().Inspect(browser("POST", "/login", "user=bob&password="+q("x' or '1'='1"), "application/x-www-form-urlencoded"))
	if len(res.Matches) == 0 || res.Matches[0].Snippet != "[redacted]" || res.Matches[0].Name != "password" {
		t.Errorf("matches %+v", res.Matches)
	}
	res = pl1().Inspect(browser("GET", "/x?q="+q("x' or '1'='1"), "", ""))
	if len(res.Matches) == 0 || !strings.Contains(res.Matches[0].Snippet, "or '1'=") {
		t.Errorf("matches %+v", res.Matches)
	}
}

func TestBodyReplay(t *testing.T) {
	e := Compile(model.WAFConfig{Mode: model.WAFBlock, InspectBodyKB: 1})

	// Small: read whole, replayed whole.
	small := `{"a":"hello"}`
	r := browser("POST", "/", small, "application/json")
	e.Inspect(r)
	if got, _ := io.ReadAll(r.Body); string(got) != small {
		t.Errorf("small body = %q", got)
	}

	// Larger than the limit, unknown length: the first KB is inspected,
	// the rest streams; an attack past the limit is not seen.
	big := `{"a":"` + strings.Repeat("x", 2000) + `","b":"<script>"}`
	r = browser("POST", "/", big, "application/json")
	r.ContentLength = -1
	res := e.Inspect(r)
	if res.Score != 0 {
		t.Errorf("past the limit scored: %v", ruleIDs(res))
	}
	if got, _ := io.ReadAll(r.Body); string(got) != big {
		t.Errorf("big body: %d bytes, want %d", len(got), len(big))
	}
	if err := r.Body.Close(); err != nil {
		t.Error(err)
	}

	// Binary: not read at all.
	r = browser("POST", "/", "\x89PNG...", "image/png")
	orig := r.Body
	e.Inspect(r)
	if r.Body != orig {
		t.Error("binary body was buffered")
	}

	// Compressed: not read.
	r = browser("POST", "/", "gzipped", "application/json")
	r.Header.Set("Content-Encoding", "gzip")
	orig = r.Body
	e.Inspect(r)
	if r.Body != orig {
		t.Error("compressed body was buffered")
	}

	// A read error reaches the application after what was read.
	boom := errors.New("client went away")
	r = browser("POST", "/", "x", "application/json")
	r.ContentLength = -1
	r.Body = io.NopCloser(io.MultiReader(strings.NewReader(`{"a":"b"`), errReaderT{boom}))
	e.Inspect(r)
	got, err := io.ReadAll(r.Body)
	if string(got) != `{"a":"b"` || !errors.Is(err, boom) {
		t.Errorf("after error: %q, %v", got, err)
	}
}

type errReaderT struct{ err error }

func (e errReaderT) Read([]byte) (int, error) { return 0, e.err }

func TestJSONNames(t *testing.T) {
	e := Compile(model.WAFConfig{Mode: model.WAFBlock, Exclusions: []model.WAFExclusion{{Args: []string{"post.body"}}}})
	body := `{"post":{"title":"t","body":"<script>ok here</script>","tags":["a"]},"list":[{"x":"<script>"}]}`
	res := e.Inspect(browser("POST", "/", body, "application/json"))
	if len(res.Matches) != 1 || res.Matches[0].Name != "list.x" {
		t.Errorf("matches %+v", res.Matches)
	}
}

func TestMultipartFileContentNotInspected(t *testing.T) {
	r := multipartReq("/upload", map[string]string{"note": "hello"}, "file", "notes.txt", "<script>alert(1)</script> ' or 1=1--")
	if res := pl1().Inspect(r); res.Score != 0 {
		t.Errorf("file content inspected: %+v", res.Matches)
	}
	// The body still reaches the application intact.
	var buf bytes.Buffer
	io.Copy(&buf, r.Body)
	if !strings.Contains(buf.String(), "<script>alert(1)</script>") {
		t.Error("multipart body not replayed")
	}
}

func TestValidate(t *testing.T) {
	for _, tc := range []struct {
		cfg  model.WAFConfig
		typ  model.SiteType
		want string // field, "" = valid
	}{
		{model.WAFConfig{}, model.SiteNode, ""},
		{model.WAFConfig{Mode: model.WAFBlock, ParanoiaLevel: 2, AnomalyThreshold: 10, InspectBodyKB: 256}, model.SiteNode, ""},
		{model.WAFConfig{Mode: "on"}, model.SiteNode, "routing.waf.mode"},
		{model.WAFConfig{Mode: model.WAFDetect}, model.SiteWorker, "routing.waf.mode"},
		{model.WAFConfig{ParanoiaLevel: 4}, model.SiteNode, "routing.waf.paranoiaLevel"},
		{model.WAFConfig{InspectBodyKB: 99999}, model.SiteNode, "routing.waf.inspectBodyKB"},
		{model.WAFConfig{Exclusions: []model.WAFExclusion{{RuleIDs: []int{123}}}}, model.SiteNode, "routing.waf.exclusions[0].ruleIds"},
		{model.WAFConfig{Exclusions: []model.WAFExclusion{{Categories: []string{"nope"}}}}, model.SiteNode, "routing.waf.exclusions[0].categories"},
		{model.WAFConfig{Exclusions: []model.WAFExclusion{{Path: "admin", RuleIDs: []int{942100}}}}, model.SiteNode, "routing.waf.exclusions[0].path"},
		{model.WAFConfig{Exclusions: []model.WAFExclusion{{}}}, model.SiteNode, "routing.waf.exclusions[0]"},
		{model.WAFConfig{Exclusions: []model.WAFExclusion{{Args: []string{" "}}}}, model.SiteNode, "routing.waf.exclusions[0].args[0]"},
		{model.WAFConfig{Exclusions: []model.WAFExclusion{{Path: "/hooks"}}}, model.SiteNode, ""},
	} {
		err := Validate(tc.cfg, tc.typ)
		var ve *model.ValidationError
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%+v: %v", tc.cfg, err)
		case tc.want != "" && (!errors.As(err, &ve) || ve.Field != tc.want):
			t.Errorf("%+v: %v, want field %s", tc.cfg, err, tc.want)
		}
	}
}

func TestInvalidEscape(t *testing.T) {
	for in, bad := range map[string]bool{"a=%41": false, "a=%u0041": false, "a=%": true, "a=%4": true, "a=%zz&b=1": true, "a=%41%2": true, "": false} {
		if got := invalidEscape(in) != ""; got != bad {
			t.Errorf("invalidEscape(%q) = %v", in, got)
		}
	}
}

func TestConcurrentInspect(t *testing.T) {
	e := pl1()
	done := make(chan bool)
	for i := 0; i < 8; i++ {
		go func() {
			for j := 0; j < 50; j++ {
				for _, a := range attacks[:10] {
					if !e.Inspect(a.req()).Exceeded() {
						t.Error(a.name)
					}
				}
			}
			done <- true
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}
