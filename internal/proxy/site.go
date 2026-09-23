package proxy

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klauspost/compress/gzhttp"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/procmgr"
	"github.com/parthh37/nodehoster/internal/rewrite"
	"golang.org/x/crypto/bcrypt"
	"gopkg.in/natefinch/lumberjack.v2"
)

type ctxKey int

const (
	ctxBackend ctxKey = iota
	ctxClientIP
	ctxRewriteProxy // *rewrite.Result of a rewrite to an absolute URL
)

type location struct {
	model.Location
	handler http.Handler // for url and static kinds
}

// siteRuntime is a site compiled for serving: regular expressions compiled,
// CIDRs parsed, transports built. A new one is built on every configuration
// change and swapped in atomically.
type siteRuntime struct {
	srv   *Server
	site  *model.Site
	stats *siteStats

	allow, deny []*net.IPNet
	maintAllow  []*net.IPNet
	rewrites    *rewrite.Engine
	types       *mimeTypes
	locations   []location
	limiter     *ipLimiter
	basicUsers  map[string]string // username -> bcrypt hash
	authCache   sync.Map          // sha256(user:pass) -> true
	httpsPort   int               // for HTTPS redirects, 0 = no https binding

	core      http.Handler // type-specific handler (node/proxy/static/redirect)
	dispatch  http.Handler // after the request pipeline: locations or core, compressed and rewritten
	toURL     http.Handler // rewrite rules that proxy to an absolute URL
	transport *http.Transport
	pool      *upstreamPool
	rr        atomic.Uint64
	access    *lumberjack.Logger
}

func (s *Server) compileSite(site *model.Site, prev *siteRuntime) *siteRuntime {
	rt := &siteRuntime{srv: s, site: site, stats: s.statsFor(site.ID)}
	r := site.Routing
	for _, c := range r.IP.Allow {
		if n, err := model.ParseCIDROrIP(c); err == nil {
			rt.allow = append(rt.allow, n)
		}
	}
	for _, c := range r.IP.Deny {
		if n, err := model.ParseCIDROrIP(c); err == nil {
			rt.deny = append(rt.deny, n)
		}
	}
	for _, c := range r.Maintenance.AllowIPs {
		if n, err := model.ParseCIDROrIP(c); err == nil {
			rt.maintAllow = append(rt.maintAllow, n)
		}
	}
	if eng, err := rewrite.Compile(r, s.physicalRoot(site)); err != nil {
		// Validated when saved, so only a configuration from an older
		// version could get here; serve it without its rewrite rules.
		s.deps.Log.Warn("rewrite rules not applied", "site", site.Name, "err", err)
	} else if !eng.Empty() {
		rt.rewrites = eng
	}
	rt.types = newMimeTypes(s.deps.Settings().Mime, r)
	if r.RateLimit.Enabled {
		rt.limiter = newIPLimiter(r.RateLimit.RequestsPerSecond, r.RateLimit.Burst)
	}
	if r.BasicAuth.Enabled {
		rt.basicUsers = map[string]string{}
		for _, u := range r.BasicAuth.Users {
			rt.basicUsers[u.Username] = u.PasswordHash
		}
	}
	for _, b := range site.Bindings {
		if b.Protocol == "https" {
			rt.httpsPort = b.Port
			break
		}
	}

	timeout := time.Duration(r.TimeoutSec) * time.Second
	insecure := (site.Type == model.SiteProxy && site.Proxy.InsecureSkipVerify) ||
		(site.Type == model.SiteNode && site.Node.LoadBalancer.Enabled && site.Node.LoadBalancer.InsecureSkipVerify)
	rt.transport = newTransport(insecure, timeout)
	if rt.rewrites != nil {
		rt.toURL = rt.rewriteProxy()
	}

	for _, l := range r.Locations {
		loc := location{Location: l}
		switch l.Kind {
		case "url":
			if u, err := url.Parse(l.URL); err == nil {
				loc.handler = rt.urlProxy(u)
			}
		case "static":
			loc.handler = &staticHandler{root: l.Root, index: []string{"index.html", "index.htm", "default.htm"}, errPages: r.ErrorPages, types: rt.types}
		}
		rt.locations = append(rt.locations, loc)
	}
	// Longest prefix wins.
	sort.SliceStable(rt.locations, func(i, j int) bool { return len(rt.locations[i].Path) > len(rt.locations[j].Path) })

	var prevPool *upstreamPool
	if prev != nil {
		prevPool = prev.pool
	}
	switch site.Type {
	case model.SiteNode:
		rt.core = rt.nodeHandler()
		if lb := site.Node.LoadBalancer; lb.Enabled {
			id := site.ID
			cfg := poolConfig{
				upstreams: lb.Servers, strategy: lb.Strategy, hc: lb.HealthCheck, insecure: lb.InsecureSkipVerify,
				localWeight: lb.LocalWeight,
				localReady:  func() bool { return len(s.deps.Procs.Backends(id)) > 0 },
				localCount:  func() int { return len(s.deps.Procs.Backends(id)) },
			}
			rt.pool = newUpstreamPool(site, cfg, s.deps.Bus, prevPool)
			rt.core = rt.upstreamHandler(rt.core)
		}
	case model.SiteProxy:
		rt.pool = newUpstreamPool(site, proxyPoolConfig(site), s.deps.Bus, prevPool)
		rt.core = rt.upstreamHandler(nil)
	case model.SiteStatic:
		root := site.ResolveRoot(s.deps.SitesDir, site.Static.Root)
		rt.core = &staticHandler{
			root: root, index: site.Static.IndexFiles, spa: site.Static.SPAFallback,
			browse: site.Static.DirectoryBrowsing, cache: site.Static.CacheControl, errPages: r.ErrorPages,
			types: rt.types,
		}
	case model.SiteRedirect:
		rt.core = rt.redirectHandler()
	}
	// The dispatch chain: outbound rules see the uncompressed response,
	// and compression applies to whatever they produce.
	rt.dispatch = http.HandlerFunc(rt.route)
	if rt.rewrites.HasOutbound() {
		inner := rt.dispatch
		rt.dispatch = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ow, finish := rt.rewrites.ResponseWriter(w, req, rt.env(req))
			inner.ServeHTTP(ow, req)
			finish()
		})
	}
	if r.Compression {
		gz, err := gzhttp.NewWrapper(gzhttp.ExceptContentTypes([]string{"text/event-stream"}))
		if err == nil {
			inner := rt.dispatch
			wrapped := gz(inner)
			rt.dispatch = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Header.Get("Upgrade") != "" {
					inner.ServeHTTP(w, req) // never wrap WebSocket upgrades
					return
				}
				wrapped.ServeHTTP(w, req)
			})
		}
	}
	if r.AccessLog {
		if prev != nil && prev.access != nil {
			rt.access = prev.access
		} else {
			st := s.deps.Settings()
			rt.access = &lumberjack.Logger{
				Filename: s.accessLogPath(site.ID), MaxSize: st.LogMaxSizeMB, MaxBackups: st.LogMaxFiles,
				MaxAge: st.LogRetentionDays, LocalTime: true,
			}
		}
	}
	return rt
}

// release frees resources of a runtime that has been replaced.
func (rt *siteRuntime) release(keepAccessLog bool) {
	if rt.pool != nil {
		rt.pool.close()
	}
	// Let in-flight requests finish on the old transport.
	time.AfterFunc(2*time.Minute, rt.transport.CloseIdleConnections)
	if rt.access != nil && !keepAccessLog {
		rt.access.Close()
	}
}

func newTransport(insecure bool, responseTimeout time.Duration) *http.Transport {
	t := &http.Transport{
		Proxy:                 nil, // never route through the machine's proxy settings
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          1024,
		MaxIdleConnsPerHost:   256,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		ResponseHeaderTimeout: responseTimeout,
	}
	if insecure {
		t.TLSClientConfig = insecureTLS()
	}
	return t
}

// ---- request pipeline

func (rt *siteRuntime) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	site := rt.site
	ro := site.Routing
	clientIP := rt.srv.clientIP(r)
	ctx := context.WithValue(r.Context(), ctxClientIP, clientIP)
	r = r.WithContext(ctx)

	// HTTPS redirect.
	if ro.HTTPSRedirect && r.TLS == nil && rt.httpsPort != 0 {
		host := hostOnly(r.Host)
		if rt.httpsPort != 443 {
			host = net.JoinHostPort(host, strconv.Itoa(rt.httpsPort))
		}
		http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusMovedPermanently)
		return
	}
	if ro.HSTS.Enabled && r.TLS != nil {
		v := fmt.Sprintf("max-age=%d", ro.HSTS.MaxAgeSec)
		if ro.HSTS.IncludeSubdomains {
			v += "; includeSubDomains"
		}
		if ro.HSTS.Preload {
			v += "; preload"
		}
		w.Header().Set("Strict-Transport-Security", v)
	}

	ip := net.ParseIP(clientIP)
	if (len(rt.allow) > 0 && !inNets(ip, rt.allow)) || inNets(ip, rt.deny) {
		errorPage(w, ro.ErrorPages, http.StatusForbidden, "Access from your address is not allowed.")
		return
	}
	if ro.Maintenance.Enabled && !inNets(ip, rt.maintAllow) {
		if ro.Maintenance.RetryAfterSec > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(ro.Maintenance.RetryAfterSec))
		}
		if ro.Maintenance.HTML != "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, ro.Maintenance.HTML)
			return
		}
		errorPage(w, ro.ErrorPages, http.StatusServiceUnavailable, "This site is down for maintenance. Please try again shortly.")
		return
	}
	if rt.limiter != nil && !rt.limiter.allow(clientIP) {
		w.Header().Set("Retry-After", "1")
		errorPage(w, ro.ErrorPages, http.StatusTooManyRequests, "Too many requests. Slow down and try again.")
		return
	}
	if rt.basicUsers != nil && !excluded(r.URL.Path, ro.BasicAuth.ExcludePaths) && !rt.checkBasic(r) {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Basic realm=%q, charset="UTF-8"`, ro.BasicAuth.Realm))
		errorPage(w, ro.ErrorPages, http.StatusUnauthorized, "Authentication is required.")
		return
	}
	if ro.MaxBodyMB > 0 {
		limit := int64(ro.MaxBodyMB) << 20
		if r.ContentLength > limit {
			errorPage(w, ro.ErrorPages, http.StatusRequestEntityTooLarge, "The request body is too large.")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
	}

	// URL rewrite rules, in order.
	if rt.rewrites != nil {
		switch res := rt.rewrites.Inbound(r, rt.env(r)); res.Action {
		case "redirect":
			http.Redirect(w, r, res.Location, res.Status)
			return
		case "block":
			errorPage(w, ro.ErrorPages, res.Status, "This request was blocked.")
			return
		case "respond":
			w.Header().Set("Content-Type", res.ContentType)
			w.WriteHeader(res.Status)
			io.WriteString(w, res.Body)
			return
		case "proxy":
			r = r.WithContext(context.WithValue(r.Context(), ctxRewriteProxy, &res))
		}
	}

	applyHeaderRules(r.Header, ro.RequestHeaders)
	rt.dispatch.ServeHTTP(w, r)
}

// env describes a request for the rewrite engine.
func (rt *siteRuntime) env(r *http.Request) rewrite.Env {
	e := rewrite.Env{TLS: r.TLS != nil}
	e.ClientIP, _ = r.Context().Value(ctxClientIP).(string)
	if a, ok := r.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr); ok {
		e.Port = strconv.Itoa(a.Port)
	}
	return e
}

// route sends a request that passed the pipeline to its handler: the URL
// a rewrite rule proxies to, a location, or the site itself.
func (rt *siteRuntime) route(w http.ResponseWriter, r *http.Request) {
	ro := rt.site.Routing
	if rt.rewrites.RewritesBodies() {
		// A compressed response could not be rewritten.
		r.Header.Del("Accept-Encoding")
	}
	if _, ok := r.Context().Value(ctxRewriteProxy).(*rewrite.Result); ok {
		rt.toURL.ServeHTTP(w, r)
		return
	}
	for _, loc := range rt.locations {
		if r.URL.Path != loc.Path && !strings.HasPrefix(r.URL.Path, strings.TrimSuffix(loc.Path, "/")+"/") {
			continue
		}
		if loc.StripPrefix {
			r.URL.Path = "/" + strings.TrimLeft(strings.TrimPrefix(r.URL.Path, strings.TrimSuffix(loc.Path, "/")), "/")
			r.URL.RawPath = ""
			r.Header.Set("X-Forwarded-Prefix", loc.Path)
		}
		switch loc.Kind {
		case "site":
			target := rt.srv.runtime(loc.SiteID)
			if target == nil {
				errorPage(w, ro.ErrorPages, http.StatusBadGateway, "The application mounted at this path does not exist.")
				return
			}
			target.core.ServeHTTP(w, r)
		default:
			if loc.handler == nil {
				errorPage(w, ro.ErrorPages, http.StatusBadGateway, "This location is misconfigured.")
				return
			}
			loc.handler.ServeHTTP(w, r)
		}
		return
	}
	rt.core.ServeHTTP(w, r)
}

func applyHeaderRules(h http.Header, rules []model.HeaderRule) {
	for _, hr := range rules {
		switch hr.Action {
		case "set":
			h.Set(hr.Name, hr.Value)
		case "add":
			h.Add(hr.Name, hr.Value)
		case "remove":
			h.Del(hr.Name)
		}
	}
}

func excluded(p string, prefixes []string) bool {
	for _, x := range prefixes {
		if x != "" && strings.HasPrefix(p, x) {
			return true
		}
	}
	return false
}

func inNets(ip net.IP, nets []*net.IPNet) bool {
	if ip == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

var dummyHash = sync.OnceValue(func() []byte {
	h, _ := bcrypt.GenerateFromPassword([]byte("nodehoster"), bcrypt.DefaultCost)
	return h
})

// checkBasic validates HTTP basic credentials. bcrypt is deliberately slow,
// so successful credentials are remembered by their SHA-256 for the life of
// this configuration.
func (rt *siteRuntime) checkBasic(r *http.Request) bool {
	user, pass, ok := r.BasicAuth()
	if !ok {
		return false
	}
	hash, ok := rt.basicUsers[user]
	if !ok {
		bcrypt.CompareHashAndPassword(dummyHash(), []byte(pass)) // same cost as a real user
		return false
	}
	sum := sha256.Sum256([]byte(user + "\x00" + pass + "\x00" + hash))
	key := string(sum[:])
	if _, ok := rt.authCache.Load(key); ok {
		return true
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(pass)) != nil {
		return false
	}
	rt.authCache.Store(key, true)
	return true
}

// ---- type handlers

func (rt *siteRuntime) forwardHeaders(pr *httputil.ProxyRequest) {
	in := pr.In
	clientIP, _ := in.Context().Value(ctxClientIP).(string)
	prior := ""
	if rt.srv.trustsForwarded(in) {
		prior = in.Header.Get("X-Forwarded-For")
	}
	if prior != "" {
		pr.Out.Header.Set("X-Forwarded-For", prior+", "+remoteIP(in))
	} else {
		pr.Out.Header.Set("X-Forwarded-For", clientIP)
	}
	proto := "http"
	if in.TLS != nil {
		proto = "https"
	}
	pr.Out.Header.Set("X-Forwarded-Proto", proto)
	pr.Out.Header.Set("X-Forwarded-Host", in.Host)
	pr.Out.Header.Set("X-Real-IP", clientIP)
}

func (rt *siteRuntime) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusBadGateway
	msg := "The application did not respond correctly."
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		status, msg = http.StatusGatewayTimeout, "The application took too long to respond."
	}
	if errors.Is(err, context.Canceled) {
		status = 499 // client went away; nothing is written
		if rw, ok := w.(*responseWriter); ok {
			rw.status = status
		}
		return
	}
	rt.srv.deps.Log.Debug("proxy error", "site", rt.site.Name, "err", err)
	errorPage(w, rt.site.Routing.ErrorPages, status, msg)
}

// nodeHandler proxies to the site's Node.js instances.
func (rt *siteRuntime) nodeHandler() http.Handler {
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			b := pr.In.Context().Value(ctxBackend).(*procmgr.Backend)
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = b.Addr
			pr.Out.Host = pr.In.Host // applications see the public host name
			rt.forwardHeaders(pr)
		},
		Transport:     rt.transport,
		FlushInterval: -1, // stream responses (SSE, long polling) immediately
		ErrorHandler:  rt.errorHandler,
		ErrorLog:      rt.srv.stdLog,
	}
	id := rt.site.ID
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := pickBackend(rt.srv.deps.Procs.Backends(id), &rt.rr)
		if b == nil {
			msg := "The application is not running."
			if st, ok := rt.srv.deps.Procs.Status(id); ok && (st.State == model.StateStarting || st.State == model.StateDegraded) {
				msg = "The application is starting. Please try again in a moment."
				w.Header().Set("Retry-After", "5")
			}
			errorPage(w, rt.site.Routing.ErrorPages, http.StatusServiceUnavailable, msg)
			return
		}
		b.Active.Add(1)
		b.Requests.Add(1)
		defer b.Active.Add(-1)
		rp.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxBackend, b)))
	})
}

// hopHeader marks a request forwarded by a load-balanced node site. The
// server receiving it answers from its own instances, so two servers that
// list each other can never pass a request back and forth.
const hopHeader = "X-NodeHoster-Hop"

// upstreamHandler proxies to the pool's upstream URLs. For a load-balanced
// node site, local answers requests the pool assigns to this server.
func (rt *siteRuntime) upstreamHandler(local http.Handler) http.Handler {
	pool := rt.pool
	// Other NodeHoster servers find the site by its host binding, so a
	// load-balanced node site always forwards the client's Host.
	preserve := local != nil || rt.site.Proxy.PreserveHost
	type upKey struct{}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			u := pr.In.Context().Value(upKey{}).(*upstream)
			pr.SetURL(u.url)
			if preserve {
				pr.Out.Host = pr.In.Host
			}
			if local != nil {
				pr.Out.Header.Set(hopHeader, "1")
			}
			rt.forwardHeaders(pr)
		},
		Transport:     rt.transport,
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if u, ok := r.Context().Value(upKey{}).(*upstream); ok && !errors.Is(err, context.Canceled) {
				pool.passiveFailure(u, err)
				if local != nil && canRetryLocally(r, err) {
					local.ServeHTTP(w, r)
					return
				}
			}
			rt.errorHandler(w, r, err)
		},
		ErrorLog: rt.srv.stdLog,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if local != nil && r.Header.Get(hopHeader) != "" {
			local.ServeHTTP(w, r)
			return
		}
		clientIP, _ := r.Context().Value(ctxClientIP).(string)
		u := pool.pick(clientIP)
		if u == nil {
			errorPage(w, rt.site.Routing.ErrorPages, http.StatusBadGateway, "No upstream server is configured.")
			return
		}
		u.active.Add(1)
		defer u.active.Add(-1)
		if u.local {
			local.ServeHTTP(w, r)
			return
		}
		rp.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), upKey{}, u)))
	})
}

// rewriteProxy forwards requests a rewrite rule sent to an absolute URL,
// like IIS URL Rewrite with Application Request Routing.
func (rt *siteRuntime) rewriteProxy() http.Handler {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			res := pr.In.Context().Value(ctxRewriteProxy).(*rewrite.Result)
			u := res.ProxyURL
			pr.Out.URL.Scheme, pr.Out.URL.Host = u.Scheme, u.Host
			pr.Out.URL.Path, pr.Out.URL.RawPath, pr.Out.URL.RawQuery = u.Path, u.RawPath, u.RawQuery
			if res.PreserveHost {
				pr.Out.Host = pr.In.Host
			} else {
				pr.Out.Host = "" // the target's own host name
			}
			rt.forwardHeaders(pr)
		},
		Transport:     rt.transport,
		FlushInterval: -1,
		ErrorHandler:  rt.errorHandler,
		ErrorLog:      rt.srv.stdLog,
	}
}

// urlProxy is a location that forwards to a fixed URL.
func (rt *siteRuntime) urlProxy(target *url.URL) http.Handler {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			rt.forwardHeaders(pr)
		},
		Transport:     rt.transport,
		FlushInterval: -1,
		ErrorHandler:  rt.errorHandler,
		ErrorLog:      rt.srv.stdLog,
	}
}

func (rt *siteRuntime) redirectHandler() http.Handler {
	cfg := rt.site.Redirect
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := strings.TrimRight(cfg.TargetURL, "/")
		if cfg.PreservePath {
			target += r.URL.RequestURI()
		} else if target == "" {
			target = "/"
		}
		http.Redirect(w, r, target, cfg.StatusCode)
	})
}

// ---- response writer

// responseWriter records status and size for statistics and the access log,
// and applies response header rules for handlers other than the proxy. It
// passes Flush and Hijack through so streaming and WebSockets work.
type responseWriter struct {
	http.ResponseWriter
	status  int
	written int64
	hdrs    []model.HeaderRule
	server  string
}

func (w *responseWriter) WriteHeader(code int) {
	if code < 200 && code != http.StatusSwitchingProtocols {
		// Informational responses (100 Continue, 103 Early Hints) precede
		// the real one and must not be mistaken for it.
		w.ResponseWriter.WriteHeader(code)
		return
	}
	if w.status == 0 {
		w.status = code
		if w.server != "" && w.Header().Get("Server") == "" {
			w.Header().Set("Server", w.server)
		}
		applyHeaderRules(w.Header(), w.hdrs)
	} else if code >= 200 {
		return // superfluous WriteHeader
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(b)
	w.written += int64(n)
	return n, err
}

func (w *responseWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.status == 0 {
		w.status = http.StatusSwitchingProtocols
	}
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func hostOnly(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}

func remoteIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}
