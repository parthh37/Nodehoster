package proxy

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// staticHandler serves files like an IIS static site: default documents,
// optional directory browsing, optional SPA fallback, MIME types from
// NodeHoster's own table, and dotfiles (.env, .git) and web.config never
// served, the way IIS's request filtering hides web.config.
type staticHandler struct {
	root     string
	index    []string
	spa      bool
	browse   bool
	cache    string
	errPages map[string]string
	types    *mimeTypes
	// precompressed serves file.br / file.gz next to a file to clients
	// that accept them, like IIS static compression's cache (when the
	// site compresses responses).
	precompressed bool
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
// alternate data stream, so neither may appear in a request path. A folder
// moved from IIS keeps its web.config (connection strings, machineKey):
// it is refused in any case and with the trailing dots or spaces Windows
// ignores in file names.
func (h *staticHandler) resolve(urlPath string) (full, clean string, ok bool) {
	if strings.ContainsAny(urlPath, "\\:\x00") {
		return "", "", false
	}
	clean = path.Clean("/" + urlPath)
	for _, seg := range strings.Split(clean, "/") {
		if strings.HasPrefix(seg, ".") && seg != ".well-known" {
			return "", "", false
		}
		if strings.EqualFold(strings.TrimRight(seg, ". "), "web.config") {
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
	if h.precompressed {
		if pf, pst := h.compressedVariant(w, r, f, st); pf != nil {
			defer pf.Close()
			// ServeContent leaves Content-Length out when Content-Encoding
			// is set, expecting on-the-fly compression; these bytes are final.
			w.Header().Set("Content-Length", strconv.FormatInt(pst.Size(), 10))
			http.ServeContent(w, r, st.Name(), st.ModTime(), pf)
			return
		}
	}
	http.ServeContent(w, r, st.Name(), st.ModTime(), file)
}

// compressedVariant opens file.br or file.gz when the client accepts that
// encoding and the variant is not older than the file (a stale variant
// would serve an old version). Whenever a variant exists the response
// varies by Accept-Encoding, whichever representation this client gets.
func (h *staticHandler) compressedVariant(w http.ResponseWriter, r *http.Request, f string, orig os.FileInfo) (*os.File, os.FileInfo) {
	exts := map[string]string{"br": ".br", "gzip": ".gz"}
	found := false
	for _, ext := range exts {
		if st, err := os.Stat(f + ext); err == nil && !st.IsDir() {
			found = true
		}
	}
	if !found {
		return nil, nil
	}
	addVary(w.Header(), "Accept-Encoding")
	for _, enc := range acceptedEncodings(r.Header.Get("Accept-Encoding")) {
		pf, err := os.Open(f + exts[enc])
		if err != nil {
			continue
		}
		st, err := pf.Stat()
		if err != nil || st.IsDir() || st.ModTime().Before(orig.ModTime()) {
			pf.Close()
			continue
		}
		w.Header().Set("Content-Encoding", enc)
		return pf, st
	}
	return nil, nil
}

func queryOf(r *http.Request) string {
	if r.URL.RawQuery == "" {
		return ""
	}
	return "?" + r.URL.RawQuery
}
