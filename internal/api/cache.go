package api

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/parthh37/nodehoster/internal/model"
)

// siteCachePurge empties a site's response cache, or the entries under a
// path prefix, like removing items from IIS's output cache.
func (a *API) siteCachePurge(w http.ResponseWriter, r *http.Request) {
	s := a.site(w, r)
	if s == nil {
		return
	}
	var body struct {
		Path string `json:"path"`
	}
	if err := decode(r, &body); err != nil && !errors.Is(err, io.EOF) {
		a.fail(w, err)
		return
	}
	if body.Path != "" && !strings.HasPrefix(body.Path, "/") {
		a.fail(w, &model.ValidationError{Field: "path", Message: "must start with /"})
		return
	}
	n := a.c.Proxy.PurgeCache(s.ID, body.Path)
	a.audit(r, "site.cache.purge", s.Name, body.Path)
	writeJSON(w, http.StatusOK, map[string]int{"purged": n})
}
