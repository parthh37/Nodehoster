package api

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/parthh37/nodehoster/internal/model"
)

// Importing sites from IIS (applicationHost.config, iisnode web.config) or
// PM2. The preview only reads what it is given; apply creates the reviewed
// drafts. Both are a server administrator's: they create sites.

func (a *API) importPreview(w http.ResponseWriter, r *http.Request) {
	var source, filename, name, appRoot string
	var data []byte
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			a.fail(w, errors.New("upload the file in the 'file' field (at most 8 MB)"))
			return
		}
		source, name, appRoot = r.FormValue("source"), r.FormValue("name"), r.FormValue("appRoot")
		if source != model.ImportLocalIIS {
			f, hdr, err := r.FormFile("file")
			if err != nil {
				a.fail(w, &model.ValidationError{Field: "file", Message: "choose a file to import"})
				return
			}
			defer f.Close()
			filename = hdr.Filename
			if data, err = io.ReadAll(io.LimitReader(f, 8<<20)); err != nil {
				a.fail(w, err)
				return
			}
		}
	} else {
		var in struct {
			Source   string `json:"source"`
			Text     string `json:"text"`
			Filename string `json:"filename"`
			Name     string `json:"name"`
			AppRoot  string `json:"appRoot"`
		}
		if err := decode(r, &in); err != nil {
			a.fail(w, err)
			return
		}
		source, data, filename, name, appRoot = in.Source, []byte(in.Text), in.Filename, in.Name, in.AppRoot
	}
	pv, err := a.c.ImportPreview(source, data, filename, name, appRoot)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pv)
}

func (a *API) importApply(w http.ResponseWriter, r *http.Request) {
	var req model.ImportApplyRequest
	if err := decode(r, &req); err != nil {
		a.fail(w, err)
		return
	}
	if len(req.Items) == 0 {
		a.fail(w, &model.ValidationError{Field: "items", Message: "select something to import"})
		return
	}
	source := req.Source
	if source == "" {
		source = "import"
	}
	res := a.c.ImportApply(r.Context(), req)
	for _, c := range res.Created {
		if c.Kind == model.ImportKindTask {
			a.audit(r, "site.update", siteNameOf(a, c.SiteID), "imported task "+c.Name+" from "+source)
		} else {
			a.audit(r, "site.import", c.Name, source)
		}
	}
	writeJSON(w, http.StatusOK, res)
}

func siteNameOf(a *API, id string) string {
	if s, err := a.c.Site(id); err == nil {
		return s.Name
	}
	return id
}
