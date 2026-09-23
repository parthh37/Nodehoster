package api

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/parthh37/nodehoster/internal/ipban"
	"github.com/parthh37/nodehoster/internal/model"
)

// refuseBanned keeps banned addresses out of the web console too: they may
// have been banned for guessing its passwords. Loopback is never banned,
// and NodeHoster Manager talks over the local pipe, which this does not
// guard, so an administrator locked out can always unban from the server.
func (a *API) refuseBanned(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.c.Bans.Banned(net.ParseIP(clientIP(r))) {
			w.Header().Set("Connection", "close")
			writeErr(w, http.StatusForbidden, "your address is banned; an administrator can lift the ban from the server itself")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// listBans returns the bans in force. They name client addresses, so
// viewers do not see them.
func (a *API) listBans(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.c.Bans.List())
}

func (a *API) createBan(w http.ResponseWriter, r *http.Request) {
	var in model.BanRequest
	if err := decode(r, &in); err != nil {
		a.fail(w, err)
		return
	}
	by := "local administrator"
	if u := user(r); u != nil {
		by = u.Username
	}
	b, err := a.c.Bans.Ban(in.Address, in.Minutes, in.Reason, by)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "ban.add", b.Address, b.Reason)
	writeJSON(w, http.StatusCreated, b)
}

// deleteBan lifts the ban of an address or range, or every ban containing
// an address: "2001:db8::1" lifts the ban of its /64. A range's "/" is
// sent escaped (%2F).
func (a *API) deleteBan(w http.ResponseWriter, r *http.Request) {
	addr, err := url.PathUnescape(chi.URLParam(r, "ip"))
	if err != nil || strings.TrimSpace(addr) == "" {
		writeErr(w, http.StatusBadRequest, "not an address")
		return
	}
	removed, err := a.c.Bans.Unban(addr)
	if errors.Is(err, ipban.ErrNotBanned) {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "ban.remove", strings.Join(removed, ", "), "")
	w.WriteHeader(http.StatusNoContent)
}
