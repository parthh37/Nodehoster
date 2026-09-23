package api

import (
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/parthh37/nodehoster/internal/certs"
	"github.com/parthh37/nodehoster/internal/core"
	"github.com/parthh37/nodehoster/internal/model"
)

type certView struct {
	*model.Certificate
	UsedBy []core.CertUse `json:"usedBy"`
}

func (a *API) listCerts(w http.ResponseWriter, r *http.Request) {
	list, err := a.c.Store.ListCertificates(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	out := make([]certView, 0, len(list))
	for _, c := range list {
		out = append(out, certView{c, a.c.CertificateUsage(c)})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) getCert(w http.ResponseWriter, r *http.Request) {
	c, err := a.c.Store.GetCertificate(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, certView{c, a.c.CertificateUsage(c)})
}

func (a *API) requestCert(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name      string            `json:"name"`
		Domains   []string          `json:"domains"`
		ACME      model.ACMEOptions `json:"acme"`
		AutoRenew *bool             `json:"autoRenew"`
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	if a.c.Settings().ACME.Email == "" || !a.c.Settings().ACME.AgreeTOS {
		a.fail(w, errors.New("set an ACME email address and accept the terms of service in Settings first"))
		return
	}
	renew := in.AutoRenew == nil || *in.AutoRenew
	c, err := a.c.Certs.RequestACME(r.Context(), in.Name, in.Domains, in.ACME, renew)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "cert.request", c.Name, "")
	writeJSON(w, http.StatusAccepted, certView{c, []core.CertUse{}})
}

func (a *API) importCert(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		a.fail(w, errors.New("upload the certificate as multipart form data"))
		return
	}
	read := func(field string) []byte {
		f, _, err := r.FormFile(field)
		if err != nil {
			return nil
		}
		defer f.Close()
		b, _ := io.ReadAll(f)
		return b
	}
	data := read("file")
	if len(data) == 0 {
		a.fail(w, errors.New("choose a certificate file"))
		return
	}
	c, err := a.c.Certs.Import(r.Context(), r.FormValue("name"), data, read("keyFile"), r.FormValue("password"))
	if err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "cert.import", c.Name, c.Fingerprint)
	writeJSON(w, http.StatusCreated, certView{c, []core.CertUse{}})
}

func (a *API) selfSignedCert(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name      string   `json:"name"`
		Domains   []string `json:"domains"`
		ValidDays int      `json:"validDays"`
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	c, err := a.c.Certs.SelfSigned(r.Context(), in.Name, in.Domains, in.ValidDays)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "cert.selfsigned", c.Name, "")
	writeJSON(w, http.StatusCreated, certView{c, []core.CertUse{}})
}

func (a *API) renewCert(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := a.c.Certs.Renew(r.Context(), id); err != nil {
		a.fail(w, err)
		return
	}
	c, _ := a.c.Store.GetCertificate(r.Context(), id)
	a.audit(r, "cert.renew", c.Name, "")
	writeJSON(w, http.StatusAccepted, certView{c, a.c.CertificateUsage(c)})
}

func (a *API) updateCert(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name      string `json:"name"`
		AutoRenew bool   `json:"autoRenew"`
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	c, err := a.c.Certs.Update(r.Context(), chi.URLParam(r, "id"), in.Name, in.AutoRenew)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "cert.update", c.Name, "")
	writeJSON(w, http.StatusOK, certView{c, a.c.CertificateUsage(c)})
}

func (a *API) deleteCert(w http.ResponseWriter, r *http.Request) {
	c, err := a.c.Store.GetCertificate(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	if used := a.c.CertificateUsage(c); len(used) > 0 {
		writeErr(w, http.StatusConflict, certs.ErrInUse.Error()+" ("+used[0].SiteName+")")
		return
	}
	if err := a.c.Certs.Delete(r.Context(), c.ID); err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "cert.delete", c.Name, "")
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) exportCert(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Format   string `json:"format"`
		Password string `json:"password"`
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	data, name, ctype, err := a.c.Certs.Export(chi.URLParam(r, "id"), in.Format, in.Password)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "cert.export", name, in.Format)
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Write(data)
}
