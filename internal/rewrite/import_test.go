package rewrite

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
)

const webConfig = `<?xml version="1.0" encoding="UTF-8"?>
<configuration>
  <system.webServer>
    <rewrite>
      <rewriteMaps>
        <rewriteMap name="Redirects" defaultValue="">
          <add key="/old-page" value="/new-page" />
        </rewriteMap>
      </rewriteMaps>
      <rules>
        <clear />
        <rule name="Canonical host" stopProcessing="true">
          <match url="(.*)" />
          <conditions>
            <add input="{HTTP_HOST}" pattern="^www\.example\.com$" />
          </conditions>
          <action type="Redirect" url="https://example.com/{R:1}" redirectType="Permanent" />
        </rule>
        <rule name="Map redirects" stopProcessing="true">
          <match url=".*" />
          <conditions>
            <add input="{Redirects:{REQUEST_URI}}" pattern="(.+)" />
          </conditions>
          <action type="Redirect" url="{C:1}" appendQueryString="false" redirectType="Found" />
        </rule>
        <rule name="Look-around" stopProcessing="true">
          <match url="^(?!api).*$" />
          <action type="Rewrite" url="x" />
        </rule>
        <rule name="Front controller" stopProcessing="true">
          <match url="^(.*)$" />
          <conditions logicalGrouping="MatchAll">
            <add input="{REQUEST_FILENAME}" matchType="IsFile" negate="true" />
            <add input="{REQUEST_FILENAME}" matchType="IsDirectory" negate="true" />
          </conditions>
          <serverVariables><set name="HTTP_X_ORIGINAL" value="{REQUEST_URI}" /></serverVariables>
          <action type="Rewrite" url="index.js" />
        </rule>
      </rules>
      <outboundRules>
        <rule name="Fix Location" preCondition="IsRedirect">
          <match serverVariable="RESPONSE_Location" pattern="^http://localhost:3000/(.*)" />
          <action type="Rewrite" value="https://example.com/{R:1}" />
        </rule>
        <rule name="Fix links" preCondition="ResponseIsHtml1">
          <match filterByTags="A, Img, CustomTags" customTags="x" pattern="^http://localhost:3000/(.*)" />
          <action type="Rewrite" value="/{R:1}" />
        </rule>
        <preConditions>
          <preCondition name="ResponseIsHtml1"><add input="{RESPONSE_CONTENT_TYPE}" pattern="^text/html" /></preCondition>
        </preConditions>
      </outboundRules>
    </rewrite>
  </system.webServer>
</configuration>`

func TestImportWebConfig(t *testing.T) {
	out, err := Import(model.RewriteImportRequest{Format: "webconfig", Text: webConfig})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Rules) != 3 || len(out.OutboundRules) != 2 || len(out.RewriteMaps) != 1 {
		t.Fatalf("got %d rules, %d outbound, %d maps; warnings %v", len(out.Rules), len(out.OutboundRules), len(out.RewriteMaps), out.Warnings)
	}
	joined := strings.Join(out.Warnings, "\n")
	for _, w := range []string{"Look-around", "server variables", "custom tags"} {
		if !strings.Contains(joined, w) {
			t.Errorf("missing warning about %s in:\n%s", w, joined)
		}
	}
	host := out.Rules[0]
	if host.Match != "^/?.*?(?:(.*))" || !host.IgnoreCase || host.StatusCode != 301 || host.QueryString != "append" || !host.Stop {
		t.Errorf("canonical host: %+v", host)
	}
	if out.Rules[1].StatusCode != 302 || out.Rules[1].QueryString != "discard" {
		t.Errorf("map redirect: %+v", out.Rules[1])
	}
	fc := out.Rules[2]
	if fc.Match != "^/(.*)$" || fc.Target != "index.js" || len(fc.Conditions) != 2 || fc.Conditions[0].MatchType != "isFile" || !fc.Conditions[0].Negate {
		t.Errorf("front controller: %+v", fc)
	}
	if o := out.OutboundRules[0]; o.Scope != "header" || o.Header != "Location" {
		t.Errorf("outbound header: %+v", o)
	}
	if o := out.OutboundRules[1]; o.Scope != "tags" || strings.Join(o.Tags, ",") != "a,img" {
		t.Errorf("outbound tags: %+v", o)
	}

	// The imported rules behave as they did in IIS.
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "app.css"), nil, 0o644)
	cfg := model.RoutingConfig{Rewrites: out.Rules, RewriteMaps: out.RewriteMaps}
	e := engine(t, cfg, root)
	r := httptest.NewRequest("GET", "/shop/item?id=4", nil)
	r.Host = "www.example.com"
	if res := e.Inbound(r, Env{}); res.Location != "https://example.com/shop/item?id=4" {
		t.Errorf("host redirect: %+v", res)
	}
	r = httptest.NewRequest("GET", "/old-page", nil)
	if res := e.Inbound(r, Env{}); res.Location != "/new-page" || res.Status != 302 {
		t.Errorf("map redirect: %+v", res)
	}
	r = httptest.NewRequest("GET", "/users/7", nil)
	e.Inbound(r, Env{})
	if r.URL.Path != "/index.js" {
		t.Errorf("front controller: %s", r.URL.Path)
	}
	r = httptest.NewRequest("GET", "/app.css", nil)
	e.Inbound(r, Env{})
	if r.URL.Path != "/app.css" {
		t.Errorf("existing file rewritten: %s", r.URL.Path)
	}
}

func TestImportWebConfigFragment(t *testing.T) {
	out, err := Import(model.RewriteImportRequest{Format: "webconfig", Text: `<rules><rule name="w" patternSyntax="Wildcard"><match url="blog/*" /><action type="Redirect" url="news/{R:1}" /></rule></rules>`})
	if err != nil || len(out.Rules) != 1 {
		t.Fatalf("%v %+v", err, out)
	}
	if r := out.Rules[0]; r.Match != `^/blog/(.*)$` || r.Target != "/news/{R:1}" {
		t.Fatalf("wildcard: %+v", r)
	}
	if _, err := Import(model.RewriteImportRequest{Format: "webconfig", Text: "<configuration/>"}); err == nil {
		t.Fatal("expected an error without a rewrite section")
	}
}

const wordpress = `
# BEGIN WordPress
<IfModule mod_rewrite.c>
RewriteEngine On
RewriteBase /
RewriteRule ^index\.php$ - [L]
RewriteCond %{REQUEST_FILENAME} !-f
RewriteCond %{REQUEST_FILENAME} !-d
RewriteRule . /index.php [L]
</IfModule>
# END WordPress

RewriteCond %{HTTPS} off [OR]
RewriteCond %{HTTP_HOST} ^www\. [NC]
RewriteCond %{HTTP_HOST} ^(?:www\.)?(.+)$ [NC]
RewriteRule ^(.*)$ https://%1/$1 [R=301,L,NE]
RewriteRule ^feed/?$ /rss.xml [QSA,L]
RewriteRule ^api/(.*)$ http://127.0.0.1:4000/$1 [P]
RewriteRule ^secret - [F]
Redirect 301 /old /new
RedirectMatch gone ^/removed/
Options -Indexes
`

func TestImportHtaccess(t *testing.T) {
	out, err := Import(model.RewriteImportRequest{Format: "htaccess", Text: wordpress})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Rules) != 8 {
		for _, r := range out.Rules {
			t.Logf("%+v", r)
		}
		t.Fatalf("got %d rules; warnings %v", len(out.Rules), out.Warnings)
	}
	if !strings.Contains(strings.Join(out.Warnings, "\n"), "Options") {
		t.Errorf("warnings: %v", out.Warnings)
	}
	if r := out.Rules[0]; r.Action != "none" || !r.Stop || r.Match != `^/index\.php$` {
		t.Errorf("index.php passthrough: %+v", r)
	}
	if r := out.Rules[1]; r.Target != "/index.php" || len(r.Conditions) != 2 || r.Conditions[1].MatchType != "isDirectory" {
		t.Errorf("front controller: %+v", r)
	}
	https := out.Rules[2]
	if https.Action != "redirect" || https.StatusCode != 301 || https.Target != "https://{C:1}/{R:1}" || len(https.Conditions) != 3 {
		t.Errorf("https: %+v", https)
	}
	if r := out.Rules[4]; r.Action != "rewrite" || r.Target != "http://127.0.0.1:4000/{R:1}" {
		t.Errorf("proxy: %+v", r)
	}
	if r := out.Rules[5]; r.Action != "block" || r.StatusCode != 403 {
		t.Errorf("forbidden: %+v", r)
	}

	e := engine(t, model.RoutingConfig{Rewrites: out.Rules}, t.TempDir())
	r := httptest.NewRequest("GET", "/2024/hello", nil)
	e.Inbound(r, Env{TLS: true})
	if r.URL.Path != "/index.php" {
		t.Errorf("wordpress permalink: %s", r.URL.Path)
	}
	// Past the WordPress block, which catches everything that is not a file.
	e = engine(t, model.RoutingConfig{Rewrites: out.Rules[2:]}, t.TempDir())
	r = httptest.NewRequest("GET", "/old/page?x=1", nil)
	r.Host = "example.com"
	if res := e.Inbound(r, Env{TLS: true}); res.Location != "/new/page?x=1" || res.Status != 301 {
		t.Errorf("mod_alias Redirect: %+v", res)
	}
	r = httptest.NewRequest("GET", "/removed/x", nil)
	if res := e.Inbound(r, Env{TLS: true}); res.Status != 410 {
		t.Errorf("gone: %+v", res)
	}
	r = httptest.NewRequest("GET", "/api/users", nil)
	if res := e.Inbound(r, Env{TLS: true}); res.Action != "proxy" || res.ProxyURL.String() != "http://127.0.0.1:4000/users" {
		t.Errorf("proxy: %+v", res)
	}
}

func TestSlashPattern(t *testing.T) {
	for in, want := range map[string]string{
		"^blog/(.*)$":  "^/blog/(.*)$",
		"^/already":    "^/already",
		`\.php$`:       `^/?.*?(?:\.php$)`,
		"^a$|^b/(x|y)": "^/a$|^/b/(x|y)",
		".*":           ".*",
		"":             "",
		`^[|]x`:        `^/[|]x`,
	} {
		if got := slashPattern(in); got != want {
			t.Errorf("slashPattern(%q) = %q, want %q", in, got, want)
		}
	}
}
