// Package webui serves the React admin console compiled into the binary.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

const notBuilt = `<!doctype html><meta charset="utf-8"><title>NodeHoster</title>
<body style="font-family:system-ui;padding:3rem;max-width:40rem">
<h1>NodeHoster</h1><p>The web console was not included in this build.
Run <code>npm run build</code> in <code>web/</code> and rebuild the binary.
The REST API is available under <code>/api</code>.</p></body>`

// Handler serves static assets with long caching for hashed files and falls
// back to index.html for client-side routes.
func Handler() http.Handler {
	sub, _ := fs.Sub(dist, "dist")
	files := http.FileServer(http.FS(sub))
	_, err := fs.Stat(sub, "index.html")
	built := err == nil
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !built {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(notBuilt))
			return
		}
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p != "" && p != "index.html" {
			if st, err := fs.Stat(sub, p); err == nil && !st.IsDir() {
				if strings.HasPrefix(p, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
			if path.Ext(p) != "" {
				http.NotFound(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		index, _ := fs.ReadFile(sub, "index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(index)
	})
}
