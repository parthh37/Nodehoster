package proxy

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// staticHandler serves files like an IIS static site: default documents,
// optional directory browsing, optional SPA fallback, and dotfiles (.env,
// .git) never served, the way IIS hides web.config.
type staticHandler struct {
	root     string
	index    []string
	spa      bool
	browse   bool
	cache    string
	errPages map[string]string
}

func (h *staticHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		errorPage(w, h.errPages, http.StatusMethodNotAllowed, "This resource only supports GET and HEAD.")
		return
	}
	p := path.Clean("/" + r.URL.Path)
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, ".") && seg != "." && seg != ".well-known" {
			errorPage(w, h.errPages, http.StatusNotFound, "The requested file was not found.")
			return
		}
	}
	full := filepath.Join(h.root, filepath.FromSlash(p))
	st, err := os.Stat(full)
	if err == nil && st.IsDir() {
		if !strings.HasSuffix(r.URL.Path, "/") {
			http.Redirect(w, r, r.URL.Path+"/"+queryOf(r), http.StatusMovedPermanently)
			return
		}
		for _, idx := range h.index {
			f := filepath.Join(full, idx)
			if fi, err := os.Stat(f); err == nil && !fi.IsDir() {
				h.serveFile(w, r, f)
				return
			}
		}
		if h.browse {
			h.setCache(w)
			http.FileServer(http.Dir(h.root)).ServeHTTP(w, r)
			return
		}
		err = os.ErrNotExist
	}
	if err != nil {
		if h.spa && path.Ext(p) == "" {
			for _, idx := range h.index {
				f := filepath.Join(h.root, idx)
				if fi, err := os.Stat(f); err == nil && !fi.IsDir() {
					w.Header().Set("Cache-Control", "no-cache")
					http.ServeFile(w, r, f)
					return
				}
			}
		}
		errorPage(w, h.errPages, http.StatusNotFound, "The requested file was not found.")
		return
	}
	h.serveFile(w, r, full)
}

func (h *staticHandler) setCache(w http.ResponseWriter) {
	if h.cache != "" {
		w.Header().Set("Cache-Control", h.cache)
	}
}

func (h *staticHandler) serveFile(w http.ResponseWriter, r *http.Request, f string) {
	h.setCache(w)
	if strings.HasSuffix(f, ".html") || strings.HasSuffix(f, ".htm") {
		if h.cache == "" {
			w.Header().Set("Cache-Control", "no-cache")
		}
	}
	// ServeContent via ServeFile handles ranges, If-Modified-Since and
	// content types. It would redirect index.html to "./", so open directly.
	file, err := os.Open(f)
	if err != nil {
		errorPage(w, h.errPages, http.StatusNotFound, "The requested file was not found.")
		return
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil {
		errorPage(w, h.errPages, http.StatusInternalServerError, "The file could not be read.")
		return
	}
	http.ServeContent(w, r, st.Name(), st.ModTime(), file)
}

func queryOf(r *http.Request) string {
	if r.URL.RawQuery == "" {
		return ""
	}
	return "?" + r.URL.RawQuery
}
