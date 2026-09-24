package api

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/parthh37/nodehoster/internal/model"
)

// getTLS returns the server-wide TLS settings and the HTTP/3 listeners.
func (a *API) getTLS(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.c.TLSView())
}

// putTLS changes only the TLS settings (minimum version, HTTP/2, HTTP/3).
func (a *API) putTLS(w http.ResponseWriter, r *http.Request) {
	var in model.TLSSettings
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	v, err := a.c.UpdateTLS(r.Context(), in)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "settings.tls", "server", fmt.Sprintf("min TLS %s, HTTP/2 %s, HTTP/3 %s", in.MinVersion, onOff(in.HTTP2), onOff(in.HTTP3)))
	writeJSON(w, http.StatusOK, v)
}

// checkCertOCSP asks a certificate's OCSP responder now.
func (a *API) checkCertOCSP(w http.ResponseWriter, r *http.Request) {
	c, err := a.c.Store.GetCertificate(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	if _, err := a.c.Certs.CheckOCSP(r.Context(), c.ID); err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "cert.ocsp", c.Name, "")
	writeJSON(w, http.StatusOK, a.certView(c))
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
