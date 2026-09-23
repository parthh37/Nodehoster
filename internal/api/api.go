// Package api is the admin console's REST API and the embedded web UI.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/parthh37/nodehoster/internal/auth"
	"github.com/parthh37/nodehoster/internal/core"
	"github.com/parthh37/nodehoster/internal/deploy"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/store"
	"github.com/parthh37/nodehoster/internal/webui"
)

type API struct {
	c   *core.Core
	log *slog.Logger
}

type ctxKey int

const (
	ctxUser ctxKey = iota
	ctxToken
)

// Handler builds the admin HTTP handler: API, webhooks, metrics and the UI.
func Handler(c *core.Core) http.Handler {
	a := &API{c: c, log: c.Log}
	r := chi.NewRouter()
	r.Use(a.recoverer)
	r.Use(securityHeaders)

	r.Post("/hooks/deploy/{id}", a.webhookDeploy)
	r.With(a.authenticate, a.requireFiltered).Get("/metrics", a.prometheus)

	r.Route("/api", func(r chi.Router) {
		r.Use(noCache)
		r.Post("/auth/login", a.login)
		r.Group(func(r chi.Router) {
			r.Use(a.authenticate)
			r.Use(csrf)
			r.Post("/auth/logout", a.logout)
			r.Get("/auth/me", a.me)
			// Account endpoints act on the user, not on a site or the
			// server, so a restricted token (say, CI's deploy-one-site
			// token) may not use them: it could otherwise mint itself
			// an unrestricted token or change the account's sign-in.
			r.Group(func(r chi.Router) {
				r.Use(unrestrictedToken)
				r.Post("/auth/password", a.changePassword)
				r.Post("/auth/totp/setup", a.totpSetup)
				r.Post("/auth/totp/enable", a.totpEnable)
				r.Post("/auth/totp/disable", a.totpDisable)
				r.Get("/tokens", a.listTokens)
				r.Post("/tokens", a.createToken)
				r.Delete("/tokens/{id}", a.deleteToken)
			})

			a.routes(r)
		})
		r.NotFound(func(w http.ResponseWriter, r *http.Request) {
			writeErr(w, http.StatusNotFound, "no such endpoint")
		})
	})
	r.Handle("/*", webui.Handler())
	return r
}

// routes registers every endpoint that is guarded by a role. Handler mounts
// them behind session and token authentication; LocalHandler behind the
// Windows identity of the local admin pipe.
//
// How to authorize a route (see access.go):
//   - a route under /sites/{id} goes in a requireSite group: the caller needs
//     that role on that site, which a site-scoped user (or a site-restricted
//     token) gets from a grant and a server-wide user from their role. Sites
//     they cannot see answer 404, as if they did not exist.
//   - a server-wide route goes in a require group: site-scoped callers are
//     always refused (403), whatever the role.
//   - requireFiltered is for the few routes site-scoped callers need that are
//     not under /sites/{id}: the handler must return only what canSeeSite
//     allows (the sites list, events), or nothing specific to the server (a
//     static catalog).
//
// Put a new route in the group matching the role it needs; the groups are
// the whole authorization, handlers do no role checks of their own.
func (a *API) routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(a.requireFiltered)
		r.Get("/server/info", a.serverInfo)
		r.Get("/events", a.listEvents)
		r.Get("/stream", a.stream)
		r.Get("/sites", a.listSites)
		r.Get("/node/versions", a.nodeVersions)
		r.Get("/settings/dns-catalog", a.dnsCatalog)
		r.Get("/mime/defaults", a.mimeDefaults)
	})
	r.Group(func(r chi.Router) {
		r.Use(a.requireSite(model.RoleViewer))
		r.Get("/sites/{id}", a.getSite)
		r.Get("/sites/{id}/status", a.siteStatus)
		r.Get("/sites/{id}/metrics", a.siteMetrics)
		r.Get("/sites/{id}/logs", a.siteLogs)
		r.Get("/sites/{id}/logs/stream", a.siteLogStream)
		r.Get("/sites/{id}/logs/download", a.siteLogDownload)
		r.Get("/sites/{id}/deployments", a.listDeployments)
		r.Get("/sites/{id}/deployments/{dep}/log", a.deploymentLog)
		r.Get("/sites/{id}/deployments/{dep}/log/stream", a.deploymentLogStream)
	})
	r.Group(func(r chi.Router) {
		r.Use(a.requireSite(model.RoleOperator))
		r.Post("/sites/{id}/start", a.siteAction("start"))
		r.Post("/sites/{id}/stop", a.siteAction("stop"))
		r.Post("/sites/{id}/restart", a.siteAction("restart"))
		r.Post("/sites/{id}/recycle", a.siteAction("recycle"))
		r.Post("/sites/{id}/logs/clear", a.siteLogClear)
		r.Post("/sites/{id}/deploy/zip", a.deployZip)
		r.Post("/sites/{id}/deploy/git", a.deployGit)
		r.Post("/sites/{id}/deployments/{dep}/activate", a.activateDeployment)
	})
	r.Group(func(r chi.Router) {
		// A site's configuration is a server administrator's: no grant
		// reaches admin (see model.SiteGrant).
		r.Use(a.requireSite(model.RoleAdmin))
		r.Put("/sites/{id}", a.updateSite)
		r.Delete("/sites/{id}", a.deleteSite)
	})
	r.Group(func(r chi.Router) {
		r.Use(a.require(model.RoleViewer))
		r.Get("/server/metrics", a.serverMetrics)
		r.Get("/certificates", a.listCerts)
		r.Get("/certificates/{id}", a.getCert)
		r.Get("/node/available", a.nodeAvailable)
		r.Get("/mail/status", a.mailStatus)
		r.Get("/mail/queue", a.mailQueue)
	})
	r.Group(func(r chi.Router) {
		r.Use(a.require(model.RoleOperator))
		r.Post("/certificates/{id}/renew", a.renewCert)
		r.Get("/mail/health", a.mailHealth)
		r.Post("/mail/queue/retry", a.mailRetryAll)
		r.Post("/mail/queue/{id}/retry", a.mailRetry)
	})
	r.Group(func(r chi.Router) {
		r.Use(a.require(model.RoleAdmin))
		r.Post("/sites", a.createSite)
		r.Post("/certificates/acme", a.requestCert)
		r.Post("/certificates/import", a.importCert)
		r.Post("/certificates/selfsigned", a.selfSignedCert)
		r.Put("/certificates/{id}", a.updateCert)
		r.Delete("/certificates/{id}", a.deleteCert)
		r.Post("/certificates/{id}/export", a.exportCert)
		r.Post("/node/versions", a.installNode)
		r.Delete("/node/versions/{version}", a.removeNode)
		r.Get("/settings", a.getSettings)
		r.Put("/settings", a.putSettings)
		r.Post("/settings/webhooks/test", a.testWebhook)
		r.Post("/rewrite/import", a.rewriteImport)
		r.Get("/mail/queue/{id}/eml", a.mailContent)
		r.Delete("/mail/queue/{id}", a.mailDelete)
		r.Post("/mail/test", a.mailTest)
		r.Get("/settings/admin", a.getAdminSettings)
		r.Put("/settings/admin", a.putAdminSettings)
		r.Get("/users", a.listUsers)
		r.Post("/users", a.createUser)
		r.Put("/users/{id}", a.updateUser)
		r.Delete("/users/{id}", a.deleteUser)
		r.Get("/audit", a.listAudit)
		r.Get("/backup", a.backup)
		r.Post("/restore", a.restore)
	})
}

// ---- middleware

func (a *API) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler {
					panic(p)
				}
				a.log.Error("panic in admin handler", "path", r.URL.Path, "panic", p)
				writeErr(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		if r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// csrf requires a custom header on state-changing requests authenticated by
// cookie. Browsers cannot send it cross-origin without a CORS preflight,
// which this server never grants.
func csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Context().Value(ctxToken) == nil {
			if r.Header.Get("X-Requested-With") != "NodeHoster" {
				writeErr(w, http.StatusForbidden, "missing X-Requested-With header")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		var u *store.UserRecord
		var tok *model.APIToken
		var err error
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			u, tok, err = a.c.Auth.Token(ctx, strings.TrimPrefix(h, "Bearer "))
			if err == nil {
				ctx = context.WithValue(ctx, ctxToken, tok)
			}
		} else if ck, cerr := r.Cookie(auth.SessionCookie); cerr == nil {
			u, err = a.c.Auth.Session(ctx, ck.Value)
		} else {
			err = errors.New("not signed in")
		}
		if err != nil || u == nil {
			writeErr(w, http.StatusUnauthorized, "not signed in")
			return
		}
		// The access is worked out once per request, from the user as
		// stored now, so a changed role or grant applies immediately to
		// sessions and tokens alike.
		ctx = withAccess(ctx, auth.UserAccess(&u.User).Restrict(tok))
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, ctxUser, u)))
	})
}

func user(r *http.Request) *store.UserRecord {
	u, _ := r.Context().Value(ctxUser).(*store.UserRecord)
	return u
}

// audit records a change made through the API.
func (a *API) audit(r *http.Request, action, target, detail string) {
	name := "anonymous"
	if u := user(r); u != nil {
		name = u.Username
	}
	a.c.Store.AddAudit(context.Background(), model.AuditEntry{
		Time: time.Now(), User: name, IP: clientIP(r), Action: action, Target: target, Detail: detail,
	})
}

func clientIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

// ---- JSON helpers

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// fail maps domain errors to HTTP responses.
func (a *API) fail(w http.ResponseWriter, err error) {
	var ve *model.ValidationError
	switch {
	case errors.As(err, &ve):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": ve.Message, "field": ve.Field})
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrDuplicateName):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error(), "field": "name"})
	case errors.Is(err, deploy.ErrBusy):
		writeErr(w, http.StatusConflict, err.Error())
	default:
		writeErr(w, http.StatusBadRequest, err.Error())
	}
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<20))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

func intParam(r *http.Request, name string, def, maxV int) int {
	v, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil || v <= 0 {
		return def
	}
	return min(v, maxV)
}

// ---- server-sent events

type sse struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func newSSE(w http.ResponseWriter) *sse {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	s := &sse{w: w, rc: http.NewResponseController(w)}
	s.rc.Flush()
	return s
}

func (s *sse) send(event string, v any) error {
	var data []byte
	if str, ok := v.(string); ok {
		data, _ = json.Marshal(str)
	} else {
		data, _ = json.Marshal(v)
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return err
	}
	return s.rc.Flush()
}

func (s *sse) ping() error {
	if _, err := io.WriteString(s.w, ": ping\n\n"); err != nil {
		return err
	}
	return s.rc.Flush()
}
