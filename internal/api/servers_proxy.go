package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/parthh37/nodehoster/internal/auth"
	"github.com/parthh37/nodehoster/internal/config"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/remote"
)

// The proxy of server connections: /api/servers/{id}/proxy/<path> is
// <remote>/api/<path>, sent with the connection's token, so that the web
// console operates another server as if it were this one, live streams
// and uploads included.
//
// It only ever reaches a configured connection, and only under its /api.
// Nothing of the caller's credentials travels (cookies, Authorization, the
// CSRF header): the request carries a few allowed headers, the token and
// the caller's role as a limit (model.RoleLimitHeader). The remote
// server's account endpoints are refused, so that nobody mints tokens or
// changes the account behind a connection. Every request that changes
// something is in this server's audit log (who, which server, what).

const (
	// maxProxyRequest is the largest request body forwarded: the largest
	// upload the API takes (a configuration restore).
	maxProxyRequest = 8 << 30
	// maxProxyResponse is the largest response forwarded (a backup
	// archive with the sites' shared folders). Bodies are streamed, never
	// held in memory.
	maxProxyResponse = 16 << 30
	// proxyIdleTimeout ends a response (other than an event stream) that
	// has not moved for that long.
	proxyIdleTimeout = 2 * time.Minute
	// proxyRecheck is how often a proxied event stream checks that the
	// caller may still use the connection (as /api/stream does).
	proxyRecheck = 2 * time.Second
)

// Request headers forwarded to the remote server; everything else (the
// caller's cookies, Authorization, CSRF header, forwarding headers) stays
// here.
var proxyRequestHeaders = []string{
	"Accept", "Accept-Language", "Content-Type", "Last-Event-ID", "Range",
	"If-None-Match", "If-Modified-Since", "X-Backup-Passphrase",
}

// Response headers returned to the caller; this server sets its own
// security and caching headers, and cookies never come back.
var proxyResponseHeaders = []string{
	"Content-Type", "Content-Length", "Content-Disposition", "Content-Range",
	"Accept-Ranges", "Last-Modified", "ETag", "X-Accel-Buffering",
}

// proxyServer forwards a request to a connection's server.
func (a *API) proxyServer(w http.ResponseWriter, r *http.Request) {
	s, ok := a.usableServer(w, r)
	if !ok {
		return
	}
	acc := access(r)
	rest, err := proxiedPath(r.URL.EscapedPath(), "/api/servers/"+s.ID+"/proxy/")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if dec, _ := url.PathUnescape(rest); accountPath(dec) {
		writeErr(w, http.StatusForbidden, "the account endpoints of a connected server (sign-in, password, API tokens) cannot be used through a connection")
		return
	}
	if r.Header.Get("Upgrade") != "" {
		// The API has no WebSocket endpoints; an upgraded connection
		// would escape the proxy's checks and limits.
		writeErr(w, http.StatusBadRequest, "connection upgrades are not proxied")
		return
	}
	readOnly := r.Method == http.MethodGet || r.Method == http.MethodHead
	if !readOnly && !acc.Server(model.RoleOperator) {
		// Viewers only read, whatever the connection's token may do (a
		// remote server older than the role limit would not stop them).
		writeErr(w, http.StatusForbidden, "your role only allows viewing a connected server")
		return
	}
	s, client, token, err := a.c.ServerClient(s.ID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	target, err := url.Parse(s.URL + "/api/" + rest)
	if err != nil || !strings.HasPrefix(path.Clean(target.Path)+"/", mustPath(s.URL)+"/api/") {
		writeErr(w, http.StatusBadRequest, "not a path of the API")
		return
	}
	target.RawQuery = r.URL.RawQuery

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	sw := &statusWriter{ResponseWriter: w}
	rp := &httputil.ReverseProxy{
		Transport: client.Transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL = target
			pr.Out.Host = ""
			in := pr.Out.Header
			pr.Out.Header = http.Header{}
			for _, k := range proxyRequestHeaders {
				if v := in.Values(k); len(v) > 0 {
					pr.Out.Header[k] = v
				}
			}
			pr.Out.Header.Set("Authorization", "Bearer "+token)
			pr.Out.Header.Set(model.RoleLimitHeader, string(acc.Role))
			pr.Out.Header.Set("User-Agent", "NodeHoster/"+config.Version+" (server connection)")
		},
		ModifyResponse: func(resp *http.Response) error {
			return a.proxyResponse(resp, r, s, cancel)
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			var mbe *http.MaxBytesError
			switch {
			case errors.As(err, &mbe):
				writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the request is larger than %d bytes", mbe.Limit))
			case r.Context().Err() != nil:
				// The caller left.
			default:
				writeErr(w, http.StatusBadGateway, fmt.Sprintf("cannot reach %s: %s", s.Name, remote.Describe(err)))
			}
		},
		ErrorLog: slog.NewLogLogger(a.log.Handler(), slog.LevelDebug),
	}
	if r.Body != nil && r.Body != http.NoBody {
		r.Body = http.MaxBytesReader(sw, r.Body, maxProxyRequest)
	}
	if !readOnly {
		// Deferred: a response broken off midway aborts the handler.
		defer func() {
			what, _ := url.PathUnescape(rest)
			a.audit(r, "server.proxy", s.Name, fmt.Sprintf("%s /api/%s (%d)", r.Method, what, sw.status()))
		}()
	}
	rp.ServeHTTP(sw, r.WithContext(ctx))
}

// proxyResponse checks and trims the remote server's answer before it is
// returned: its credentials problems are this server's (502, not a 401 the
// console would take for its own session ending), redirects are not
// followed, and only some headers pass. An event stream ends when the
// caller may no longer use the connection; any other body when it stalls
// or grows past maxProxyResponse.
func (a *API) proxyResponse(resp *http.Response, r *http.Request, s model.ServerConnection, cancel context.CancelFunc) error {
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		replaceBody(resp, http.StatusBadGateway, s.Name+" refused the connection's API token: it was revoked or has expired. An administrator can enter a new one under Servers.")
		return nil
	case resp.StatusCode >= 300 && resp.StatusCode < 400 && resp.StatusCode != http.StatusNotModified:
		replaceBody(resp, http.StatusBadGateway, s.Name+" answered with a redirect: check the connection's URL")
		return nil
	}
	h := http.Header{}
	for _, k := range proxyResponseHeaders {
		if v := resp.Header.Values(k); len(v) > 0 {
			h[k] = v
		}
	}
	resp.Header = h
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if ct == "text/event-stream" {
		go a.watchProxyStream(resp.Request.Context(), r, s.ID, cancel)
		return nil
	}
	resp.Body = newIdleBody(resp.Body, proxyIdleTimeout, maxProxyResponse, cancel)
	return nil
}

// watchProxyStream ends a proxied stream within proxyRecheck of the
// caller losing the right to use the connection (their role changed, the
// connection was removed or restricted, their session or token ended).
func (a *API) watchProxyStream(ctx context.Context, r *http.Request, id string, cancel context.CancelFunc) {
	t := time.NewTicker(proxyRecheck)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			acc, ok := a.currentAccess(r)
			s, err := a.c.ServerConnection(id)
			if !ok || err != nil || !mayUseServer(acc, s) {
				cancel()
				return
			}
		}
	}
}

// replaceBody turns resp into a JSON error of this server.
func replaceBody(resp *http.Response, status int, msg string) {
	resp.Body.Close()
	data, _ := json.Marshal(map[string]string{"error": msg})
	resp.StatusCode, resp.Status = status, fmt.Sprintf("%d %s", status, http.StatusText(status))
	resp.Header = http.Header{"Content-Type": {"application/json"}}
	resp.Body = io.NopCloser(bytes.NewReader(data))
	resp.ContentLength = int64(len(data))
}

// proxiedPath returns the remote API path of an escaped request path: what
// follows prefix, kept escaped (a ban's range travels as %2F). Segments
// that could leave the API (".", "..", a backslash, however encoded, also
// behind an encoded slash) and empty ones are refused.
func proxiedPath(escaped, prefix string) (string, error) {
	rest, ok := strings.CutPrefix(escaped, prefix)
	if !ok || rest == "" {
		return "", errors.New("name an endpoint of the server's API")
	}
	for _, seg := range strings.Split(rest, "/") {
		dec, err := url.PathUnescape(seg)
		if err != nil || dec == "" || strings.ContainsAny(dec, "\\\x00") {
			return "", errors.New("not a path of the API")
		}
		for _, part := range strings.Split(dec, "/") {
			if part == "." || part == ".." {
				return "", errors.New("not a path of the API")
			}
		}
	}
	return rest, nil
}

// accountPath reports whether an API path is an account endpoint, which
// acts on the user behind the connection's token rather than on the
// server: signing in and out, password, two-factor, API tokens. Reading
// who the token is (auth/me) is allowed: the console shows what it may do.
func accountPath(rest string) bool {
	first, tail, _ := strings.Cut(strings.ToLower(rest), "/")
	switch first {
	case "auth":
		return tail != "me"
	case "tokens":
		return true
	}
	return false
}

// mustPath is the path of a normalized base URL ("" for none).
func mustPath(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return "/invalid"
	}
	return strings.TrimRight(u.Path, "/")
}

// limitAccess narrows access to at most role lim (a RoleLimitHeader).
func limitAccess(acc auth.Access, lim model.Role) (auth.Access, bool) {
	switch lim {
	case model.RoleViewer, model.RoleOperator, model.RoleAdmin:
		return acc.Restrict(&model.APIToken{Role: lim}), true
	}
	return acc, false
}

// statusWriter records the status of a proxied response, for the audit
// log. The response controller reaches the connection through Unwrap.
type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) status() int {
	if w.code == 0 {
		return http.StatusBadGateway
	}
	return w.code
}

// idleBody is a proxied response body that ends (by cancelling the
// request) when the remote server sent nothing for a while, and fails past
// a size. Only the wait for the remote server counts, not a slow caller.
type idleBody struct {
	rc      io.ReadCloser
	timer   *time.Timer
	idle    time.Duration
	left    int64
	closeMu sync.Once
}

func newIdleBody(rc io.ReadCloser, idle time.Duration, limit int64, cancel context.CancelFunc) *idleBody {
	b := &idleBody{rc: rc, idle: idle, left: limit, timer: time.AfterFunc(idle, cancel)}
	b.timer.Stop()
	return b
}

func (b *idleBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		return 0, errors.New("the response is too large to forward")
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	b.timer.Reset(b.idle)
	n, err := b.rc.Read(p)
	b.timer.Stop()
	b.left -= int64(n)
	return n, err
}

func (b *idleBody) Close() error {
	b.closeMu.Do(func() { b.timer.Stop() })
	return b.rc.Close()
}
