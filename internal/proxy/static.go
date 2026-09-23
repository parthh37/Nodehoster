package proxy

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// staticHandler serves files like an IIS static site: default documents,
// optional directory browsing, optional SPA fallback, MIME types from
// NodeHoster's own table, and dotfiles (.env, .git) never served, the way
// IIS hides web.config.
type staticHandler struct {
	root     string
	index    []string
	spa      bool
	browse   bool
	cache    string
	errPages map[string]string
	types    *mimeTypes
}

func (h *staticHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		errorPage(w, h.errPages, http.StatusMethodNotAllowed, "This resource only supports GET and HEAD.")
		return
	}
	full, p, ok := h.resolve(r.URL.Path)
	if !ok {
		errorPage(w, h.errPages, http.StatusNotFound, "The requested file was not found.")
		return
	}
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
					h.serveFile(w, r, f)
					return
				}
			}
		}
		errorPage(w, h.errPages, http.StatusNotFound, "The requested file was not found.")
		return
	}
	h.serveFile(w, r, full)
}

// resolve maps a URL path to a file under the root. It refuses anything
// that could step outside the root or reveal hidden files: a decoded "%5C"
// is a path separator on Windows and ":" can name another drive or an NTFS
// alternate data stream, so neither may appear in a request path.
func (h *staticHandler) resolve(urlPath string) (full, clean string, ok bool) {
	if strings.ContainsAny(urlPath, "\\:\x00") {
		return "", "", false
	}
	clean = path.Clean("/" + urlPath)
	for _, seg := range strings.Split(clean, "/") {
		if strings.HasPrefix(seg, ".") && seg != ".well-known" {
			return "", "", false
		}
	}
	root, err := filepath.Abs(h.root)
	if err != nil {
		return "", "", false
	}
	full = filepath.Join(root, filepath.FromSlash(clean))
	if full != root && !strings.HasPrefix(full, root+string(filepath.Separator)) {
		return "", "", false
	}
	return full, clean, true
}

// setCache applies the site's Cache-Control unless the response already
// has one (the SPA fallback page is always revalidated).
func (h *staticHandler) setCache(w http.ResponseWriter) {
	if h.cache != "" && w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", h.cache)
	}
}

func (h *staticHandler) serveFile(w http.ResponseWriter, r *http.Request, f string) {
	ct, ok := h.types.lookup(f)
	if !ok {
		// IIS answers 404.3: the file exists but no MIME map allows it.
		errorPage(w, h.errPages, http.StatusNotFound, "The requested file was not found.")
		return
	}
	w.Header().Set("Content-Type", ct)
	h.setCache(w)
	if h.cache == "" && strings.HasPrefix(ct, "text/html") {
		w.Header().Set("Cache-Control", "no-cache")
	}
	// ServeContent handles ranges and If-Modified-Since, and keeps the
	// Content-Type set above. ServeFile would redirect index.html to "./",
	// so open directly.
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
