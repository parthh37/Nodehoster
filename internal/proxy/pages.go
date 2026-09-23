package proxy

import (
	"fmt"
	"html"
	"net/http"
	"strconv"
)

const pageTemplate = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>%d %s</title>
<style>
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;background:#f8fafc;color:#0f172a;
font:15px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif}
main{max-width:34rem;padding:2.5rem}
h1{font-size:4rem;margin:0;color:#0f766e;font-weight:700;letter-spacing:-.04em}
h2{font-size:1.25rem;margin:.25rem 0 1rem}
p{color:#475569;margin:0}
footer{margin-top:2rem;font-size:.8rem;color:#94a3b8}
@media (prefers-color-scheme:dark){body{background:#0b1120;color:#e2e8f0}p{color:#94a3b8}h1{color:#2dd4bf}}
</style></head>
<body><main><h1>%d</h1><h2>%s</h2><p>%s</p><footer>NodeHoster</footer></main></body></html>`

// errorPage writes a site's custom page for status if it has one, and the
// built-in page otherwise.
func errorPage(w http.ResponseWriter, custom map[string]string, status int, detail string) {
	h := w.Header()
	h.Del("Content-Length")
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if body, ok := custom[strconv.Itoa(status)]; ok && body != "" {
		fmt.Fprint(w, body)
		return
	}
	fmt.Fprintf(w, pageTemplate, status, http.StatusText(status), status, html.EscapeString(http.StatusText(status)), html.EscapeString(detail))
}

const defaultPage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>NodeHoster</title>
<style>
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;background:#f8fafc;color:#0f172a;
font:15px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif}
main{max-width:36rem;padding:2.5rem}
h1{font-size:1.75rem;margin:0 0 .5rem;color:#0f766e}
p{color:#475569}
code{background:#e2e8f0;padding:.1rem .35rem;border-radius:.25rem}
@media (prefers-color-scheme:dark){body{background:#0b1120;color:#e2e8f0}p{color:#94a3b8}h1{color:#2dd4bf}code{background:#1e293b}}
</style></head>
<body><main><h1>NodeHoster is running</h1>
<p>No site is bound to <code>%s</code>. Add a binding for this host name in the NodeHoster console to serve it.</p>
</main></body></html>`
