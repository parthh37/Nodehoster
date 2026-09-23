package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/parthh37/nodehoster/internal/mail"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/proxy"
	"github.com/parthh37/nodehoster/internal/rewrite"
	"github.com/parthh37/nodehoster/internal/store"
)

// ---- SMTP server

func (a *API) mailStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.c.Mail.Status())
}

func (a *API) mailQueue(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if state != "" && state != model.MailQueued && state != model.MailFailed {
		writeErr(w, http.StatusBadRequest, "state must be queued or failed")
		return
	}
	writeJSON(w, http.StatusOK, a.c.Mail.Queue(state))
}

func (a *API) mailRetry(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := a.c.Mail.Retry(id); err != nil {
		a.fail(w, mailErr(err))
		return
	}
	a.audit(r, "mail.retry", id, "")
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) mailRetryAll(w http.ResponseWriter, r *http.Request) {
	a.c.Mail.RetryAll()
	a.audit(r, "mail.retry", "all", "")
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) mailDelete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := a.c.Mail.Delete(id); err != nil {
		a.fail(w, mailErr(err))
		return
	}
	a.audit(r, "mail.delete", id, "")
	w.WriteHeader(http.StatusNoContent)
}

// mailContent downloads a message as it will be sent. It can contain
// anything an application mails (password resets, invoices), so it is
// admin-only and audited.
func (a *API) mailContent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	data, err := a.c.Mail.Content(id)
	if err != nil {
		a.fail(w, mailErr(err))
		return
	}
	a.audit(r, "mail.download", id, "")
	w.Header().Set("Content-Type", "message/rfc822")
	w.Header().Set("Content-Disposition", `attachment; filename="`+id+`.eml"`)
	w.Write(data)
}

func (a *API) mailTest(w http.ResponseWriter, r *http.Request) {
	var in model.MailTest
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	m, err := a.c.Mail.SendTest(in.From, in.To)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "mail.test", in.To, "")
	writeJSON(w, http.StatusAccepted, m)
}

func mailErr(err error) error {
	if errors.Is(err, mail.ErrNotFound) {
		return store.ErrNotFound
	}
	return err
}

// ---- URL rewrite and MIME types

func (a *API) rewriteImport(w http.ResponseWriter, r *http.Request) {
	var in model.RewriteImportRequest
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := rewrite.Import(in)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) mimeDefaults(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, proxy.DefaultMimeTypes())
}
