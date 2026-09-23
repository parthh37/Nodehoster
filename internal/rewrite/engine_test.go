package rewrite

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
)

func engine(t *testing.T, r model.RoutingConfig, root string) *Engine {
	t.Helper()
	if err := r.ValidateRewrites(); err != nil {
		t.Fatalf("model validation: %v", err)
	}
	if err := Validate(r); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	e, err := Compile(r, root)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func rules(rs ...model.RewriteRule) model.RoutingConfig {
	for i := range rs {
		rs[i].Enabled = true
	}
	return model.RoutingConfig{Rewrites: rs}
}

func run(e *Engine, target string) (*http.Request, Result) {
	r := httptest.NewRequest("GET", target, nil)
	res := e.Inbound(r, Env{ClientIP: "203.0.113.9", Port: "80"})
	return r, res
}

// Rules written before conditions and IIS syntax existed must behave
// exactly as they did.
func TestLegacyRules(t *testing.T) {
	e := engine(t, rules(
		model.RewriteRule{Match: "^/old/(.*)$", Action: "redirect", Target: "/new/$1", StatusCode: 301, Stop: true},
		model.RewriteRule{Match: "^/app/(.*)$", Action: "rewrite", Target: "/index.php?p=${1}"},
		model.RewriteRule{Match: "^/keep/(.*)$", Action: "rewrite", Target: "/k/$1"},
	), "")
	_, res := run(e, "/old/a/b?x=1")
	if res.Action != "redirect" || res.Location != "/new/a/b?x=1" || res.Status != 301 {
		t.Fatalf("redirect: %+v", res)
	}
	r, res := run(e, "/app/home?utm=1")
	if res.Action != "" || r.URL.Path != "/index.php" || r.URL.RawQuery != "p=home" {
		t.Fatalf("rewrite with query replaces it: %+v %s?%s", res, r.URL.Path, r.URL.RawQuery)
	}
	r, _ = run(e, "/keep/x?y=2")
	if r.URL.Path != "/k/x" || r.URL.RawQuery != "y=2" {
		t.Fatalf("rewrite without query keeps it: %s?%s", r.URL.Path, r.URL.RawQuery)
	}
	// Case-sensitive unless asked otherwise.
	if _, res := run(e, "/OLD/a"); res.Action != "" {
		t.Fatalf("legacy rules are case-sensitive: %+v", res)
	}
}

func TestIISSyntaxConditionsAndMaps(t *testing.T) {
	cfg := rules(
		model.RewriteRule{
			Name: "canonical host", Match: "(.*)", IgnoreCase: true, Action: "redirect",
			Target: "https://example.com{R:1}", StatusCode: 308, QueryString: "append",
			Conditions: []model.RewriteCondition{{Input: "{HTTP_HOST}", Pattern: `^www\.example\.com$`, IgnoreCase: true}},
		},
		model.RewriteRule{
			Name: "lang", Match: "^/(?P<page>[a-z]+)$", Action: "rewrite",
			Target:     "/{ToLower:{Langs:{C:1}}}/${page}",
			Conditions: []model.RewriteCondition{{Input: "{HTTP_ACCEPT_LANGUAGE}", Pattern: `^([a-z]{2})`}},
		},
	)
	cfg.RewriteMaps = []model.RewriteMap{{Name: "Langs", DefaultValue: "EN", Entries: map[string]string{"De": "DE"}}}
	e := engine(t, cfg, "")

	r := httptest.NewRequest("GET", "/shop?a=1", nil)
	r.Host = "WWW.example.com"
	res := e.Inbound(r, Env{})
	if res.Action != "redirect" || res.Status != 308 || res.Location != "https://example.com/shop?a=1" {
		t.Fatalf("host redirect: %+v", res)
	}

	r = httptest.NewRequest("GET", "/about", nil)
	r.Header.Set("Accept-Language", "de-CH,de;q=0.9")
	e.Inbound(r, Env{})
	if r.URL.Path != "/de/about" {
		t.Fatalf("map + back-references: %s", r.URL.Path)
	}
	r = httptest.NewRequest("GET", "/about", nil)
	r.Header.Set("Accept-Language", "fr")
	e.Inbound(r, Env{})
	if r.URL.Path != "/en/about" {
		t.Fatalf("map default: %s", r.URL.Path)
	}
}

func TestNegateMatchAnyAndStop(t *testing.T) {
	e := engine(t, rules(
		model.RewriteRule{Match: "^/api/", Negate: true, Action: "none", Stop: true,
			MatchAny: true, Conditions: []model.RewriteCondition{
				{Input: "{REQUEST_METHOD}", Pattern: "^POST$"},
				{Input: "{QUERY_STRING}", Pattern: "debug"},
			}},
		model.RewriteRule{Match: ".*", Action: "block", StatusCode: 405},
	), "")
	if _, res := run(e, "/api/x"); res.Status != 405 {
		t.Fatalf("api path is not negated: %+v", res)
	}
	if _, res := run(e, "/page?debug=1"); res.Action != "" {
		t.Fatalf("matchAny + stop: %+v", res)
	}
	if _, res := run(e, "/page"); res.Status != 405 {
		t.Fatalf("no condition matched: %+v", res)
	}
}

// The classic front controller: send everything that is not a file or a
// directory to index.php.
func TestFileConditions(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "style.css"), []byte("x"), 0o644)
	os.Mkdir(filepath.Join(root, "img"), 0o755)
	e := engine(t, rules(model.RewriteRule{
		Match: "^/(.*)$", Action: "rewrite", Target: "/index.php?route={R:1}", QueryString: "append",
		Conditions: []model.RewriteCondition{
			{Input: "{REQUEST_FILENAME}", MatchType: "isFile", Negate: true},
			{Input: "{REQUEST_FILENAME}", MatchType: "isDirectory", Negate: true},
		},
	}), root)
	for path, want := range map[string]string{
		"/style.css":   "/style.css",
		"/img":         "/img",
		"/blog/post?x": "/index.php?route=blog/post&x",
		"/../../etc":   "/index.php?route=../../etc",
	} {
		r, _ := run(e, path)
		got := r.URL.Path
		if r.URL.RawQuery != "" {
			got += "?" + r.URL.RawQuery
		}
		if got != want {
			t.Errorf("%s: got %s, want %s", path, got, want)
		}
	}
}

func TestRewriteToURLProxies(t *testing.T) {
	e := engine(t, rules(model.RewriteRule{
		Match: "^/blog/(.*)", Action: "rewrite", Target: "https://blog.internal:8443/{R:1}", PreserveHost: true, QueryString: "append",
	}), "")
	_, res := run(e, "/blog/post-1?ref=x")
	if res.Action != "proxy" || res.ProxyURL.String() != "https://blog.internal:8443/post-1?ref=x" || !res.PreserveHost {
		t.Fatalf("proxy: %+v %v", res, res.ProxyURL)
	}
}

func TestRespondAndBraces(t *testing.T) {
	e := engine(t, rules(model.RewriteRule{
		Match: "^/health$", Action: "respond", Body: `{"ok": true}`, ContentType: "application/json",
	}), "")
	_, res := run(e, "/health")
	if res.Action != "respond" || res.Status != 200 || res.Body != `{"ok": true}` || res.ContentType != "application/json" {
		t.Fatalf("respond: %+v", res)
	}
}

func TestValidateReportsField(t *testing.T) {
	cfg := rules(model.RewriteRule{Match: "^/x", Action: "rewrite", Target: "/{Nope:{R:1}}"})
	err := Validate(cfg)
	ve, ok := err.(*model.ValidationError)
	if !ok || ve.Field != "routing.rewrites[0].target" {
		t.Fatalf("got %v", err)
	}
	cfg.Rewrites[0].Enabled = false
	if Validate(cfg) == nil {
		t.Fatal("disabled rules must be checked too")
	}
	if cfg.Rewrites[0].Enabled {
		t.Fatal("Validate changed the caller's rules")
	}
	for _, bad := range []string{"{R:}", "{C:x}", "{ToLower:abc"} {
		if _, err := parseExpr(bad, nil); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

func TestOutbound(t *testing.T) {
	cfg := model.RoutingConfig{OutboundRules: []model.OutboundRule{
		{Name: "loc", Enabled: true, Scope: "header", Header: "Location", Match: "^http://backend:3000/(.*)", Action: "rewrite", Value: "https://{HTTP_HOST}/{R:1}"},
		{Name: "links", Enabled: true, Scope: "tags", Tags: []string{"a", "img"}, Match: "^http://backend:3000/(.*)", IgnoreCase: true, Action: "rewrite", Value: "/{R:1}"},
		{Name: "text", Enabled: true, Scope: "body", Match: "Powered by (\\w+)", Action: "rewrite", Value: "Served by NodeHoster ({R:1})"},
	}}
	e := engine(t, cfg, "")

	handler := func(ct, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "http://backend:3000/next")
			w.Header().Set("Content-Type", ct)
			w.Header().Set("Content-Length", "999")
			io.WriteString(w, body)
		}
	}
	serve := func(h http.Handler) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/", nil)
		r.Host = "example.com"
		w, finish := e.ResponseWriter(rec, r, Env{TLS: true})
		h.ServeHTTP(w, r)
		finish()
		return rec
	}

	html := `<p>Powered by Express</p><a class=x HREF='HTTP://backend:3000/about'>a</a><img src=http://backend:3000/i.png><link href="http://backend:3000/s.css">`
	rec := serve(handler("text/html; charset=utf-8", html))
	if got := rec.Header().Get("Location"); got != "https://example.com/next" {
		t.Errorf("Location: %s", got)
	}
	want := `<p>Served by NodeHoster (Express)</p><a class=x HREF='/about'>a</a><img src="/i.png"><link href="http://backend:3000/s.css">`
	if rec.Body.String() != want {
		t.Errorf("body:\n got %s\nwant %s", rec.Body.String(), want)
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(want)) {
		t.Errorf("Content-Length %s for %d bytes", got, len(want))
	}

	// Binary and compressed responses stream through untouched.
	rec = serve(handler("image/png", "Powered by PNG"))
	if rec.Body.String() != "Powered by PNG" {
		t.Errorf("binary body was rewritten: %s", rec.Body.String())
	}
	rec = serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Encoding", "gzip")
		io.WriteString(w, "Powered by gzip")
	}))
	if rec.Body.String() != "Powered by gzip" {
		t.Errorf("encoded body was rewritten: %s", rec.Body.String())
	}
	// Tag rules only apply to HTML; body rules to any text.
	rec = serve(handler("application/json", `{"u":"<a href='http://backend:3000/x'>","p":"Powered by Go"}`))
	if !strings.Contains(rec.Body.String(), "http://backend:3000/x") || !strings.Contains(rec.Body.String(), "Served by NodeHoster (Go)") {
		t.Errorf("json: %s", rec.Body.String())
	}
}

func TestOutboundLargeBodyPassesThrough(t *testing.T) {
	e := engine(t, model.RoutingConfig{OutboundRules: []model.OutboundRule{
		{Enabled: true, Scope: "body", Match: "a", Action: "rewrite", Value: "b"},
	}}, "")
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	w, finish := e.ResponseWriter(rec, r, Env{})
	w.Header().Set("Content-Type", "text/plain")
	chunk := strings.Repeat("a", 1<<20)
	for range 10 {
		w.Write([]byte(chunk))
	}
	finish()
	if rec.Body.Len() != 10<<20 || strings.Contains(rec.Body.String(), "b") {
		t.Fatalf("large body: %d bytes", rec.Body.Len())
	}
}
