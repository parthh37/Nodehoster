// Package proxy is the front door: it owns every public listener, matches
// requests to sites by binding (IP, port, host name, SNI), runs the site's
// request pipeline and forwards to Node.js instances, upstreams or files.
package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/parthh37/nodehoster/internal/certs"
	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/ipban"
	"github.com/parthh37/nodehoster/internal/logship"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/procmgr"
)

type Deps struct {
	Log      *slog.Logger
	Bus      *events.Bus
	Procs    *procmgr.Manager
	Certs    *certs.Manager
	Settings func() model.Settings
	SitesDir string
	LogsDir  string
	// AffinityKey signs session affinity cookies; persisted by the caller
	// so cookies survive restarts. A random key is used when empty.
	AffinityKey []byte
	// Bans is automatic IP banning; nil in tests that do not need it.
	Bans *ipban.Manager
	Ship *logship.Shipper // access log shipping; may be nil
}

type route struct {
	ip      net.IP // nil = all addresses
	host    string // "" = any, "*.example.com" = wildcard
	binding model.Binding
	site    *siteRuntime
	client  *clientPolicy // client certificates; nil = not asked for
}

// routeTable is immutable once built; Reload swaps in a new one.
type routeTable struct {
	byPort map[int][]*route // sorted by precedence
	sites  map[string]*siteRuntime
}

type listener struct {
	key     string // proto|addr
	proto   string
	addr    string
	port    int
	ln      net.Listener
	srv     *http.Server
	h3      *h3Listener // instead of ln and srv for a QUIC listener
	closing atomic.Bool
}

type Server struct {
	deps   Deps
	stdLog *log.Logger

	table atomic.Pointer[routeTable]

	// reloadMu serializes Reload: two concurrent reloads would otherwise
	// both release the same previous runtimes.
	reloadMu sync.Mutex

	mu        sync.Mutex
	listeners map[string]*listener
	failed    map[string]error // addr -> listen error

	statsMu sync.Mutex
	stats   map[string]*siteStats

	trustedMu sync.RWMutex
	trusted   []*net.IPNet

	affKeyOnce sync.Once
	affKey     []byte

	policies map[string]*clientPolicy // compiled client certificate policies by key (reloadMu)
	h3       atomic.Pointer[h3Ports]  // ports with a QUIC listener, for Alt-Svc

	// backends lists a node site's ready instances; the process manager's
	// in production, replaceable in tests.
	backends func(id string) []*procmgr.Backend
}

func New(deps Deps) *Server {
	s := &Server{
		deps:      deps,
		stdLog:    slog.NewLogLogger(deps.Log.Handler(), slog.LevelDebug),
		listeners: map[string]*listener{},
		failed:    map[string]error{},
		stats:     map[string]*siteStats{},
	}
	s.table.Store(&routeTable{byPort: map[int][]*route{}, sites: map[string]*siteRuntime{}})
	s.backends = deps.Procs.Backends
	deps.Certs.HTTP.HasPort80 = s.hasHTTPPort80
	go s.retryFailedListeners()
	return s
}

// retryFailedListeners keeps trying ports that could not be bound, for
// example while another program (or the temporary ACME challenge listener)
// held them, so a site comes up without needing another reload.
func (s *Server) retryFailedListeners() {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for range t.C {
		s.mu.Lock()
		failed := len(s.failed) > 0
		s.mu.Unlock()
		if failed {
			s.reloadMu.Lock()
			s.reconcileListeners(s.table.Load())
			s.reloadMu.Unlock()
		}
	}
}

func (s *Server) statsFor(id string) *siteStats {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	st, ok := s.stats[id]
	if !ok {
		st = &siteStats{}
		s.stats[id] = st
	}
	return st
}

func (s *Server) accessLogPath(id string) string {
	return filepath.Join(s.deps.LogsDir, id, "access.log")
}

// AccessLogPath is where a site's access log is written.
func (s *Server) AccessLogPath(id string) string { return s.accessLogPath(id) }

// physicalRoot is the directory a site's files are in, for the rewrite
// engine's {REQUEST_FILENAME}; "" for sites without one.
func (s *Server) physicalRoot(site *model.Site) string {
	switch {
	case site.Type == model.SiteStatic && site.Static != nil:
		return site.ResolveRoot(s.deps.SitesDir, site.Static.Root)
	case site.Type == model.SiteNode && site.Node != nil:
		return site.ResolveRoot(s.deps.SitesDir, site.Node.AppRoot)
	}
	return ""
}

func (s *Server) runtime(id string) *siteRuntime {
	return s.table.Load().sites[id]
}

// Reload rebuilds routing from the given sites and reconciles listeners:
// new ports are opened, unused ones closed, and existing connections keep
// working throughout.
func (s *Server) Reload(sites []*model.Site, running func(*model.Site) bool) {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	st := s.deps.Settings()
	var trusted []*net.IPNet
	for _, c := range st.Proxy.TrustedProxies {
		if n, err := model.ParseCIDROrIP(c); err == nil {
			trusted = append(trusted, n)
		}
	}
	s.trustedMu.Lock()
	s.trusted = trusted
	s.trustedMu.Unlock()

	old := s.table.Load()
	next := &routeTable{byPort: map[int][]*route{}, sites: map[string]*siteRuntime{}}
	for _, site := range sites {
		rt := s.compileSite(site, old.sites[site.ID])
		next.sites[site.ID] = rt
		if !running(site) {
			continue // stopped sites keep their runtime (for locations) but no bindings
		}
		for _, b := range site.Bindings {
			r := &route{host: b.Host, binding: b, site: rt, client: s.clientPolicyFor(b)}
			if b.IP != "" {
				r.ip = net.ParseIP(b.IP)
			}
			next.byPort[b.Port] = append(next.byPort[b.Port], r)
		}
	}
	for port := range next.byPort {
		sortRoutes(next.byPort[port])
	}
	s.prunePolicies(next)
	s.table.Store(next)
	for id, rt := range old.sites {
		keep := false
		if n, ok := next.sites[id]; ok && n.access == rt.access {
			keep = true
		}
		rt.release(keep)
	}
	s.reconcileListeners(next)
}

// sortRoutes orders routes by IIS precedence: a specific IP beats all
// addresses, and an exact host beats a wildcard host beats no host.
func sortRoutes(rs []*route) {
	rank := func(r *route) int {
		n := 0
		if r.ip != nil {
			n += 4
		}
		switch {
		case r.host == "":
		case strings.HasPrefix(r.host, "*."):
			n += 1
		default:
			n += 2
		}
		return n
	}
	sort.SliceStable(rs, func(i, j int) bool {
		if rank(rs[i]) != rank(rs[j]) {
			return rank(rs[i]) > rank(rs[j])
		}
		return len(rs[i].host) > len(rs[j].host) // longer wildcard suffix first
	})
}

func hostMatches(pattern, host string) bool {
	if pattern == "" {
		return true
	}
	if strings.HasPrefix(pattern, "*.") {
		suffix := pattern[1:] // ".example.com"
		return strings.HasSuffix(host, suffix) && !strings.Contains(strings.TrimSuffix(host, suffix), ".")
	}
	return pattern == host
}

func (t *routeTable) match(port int, local net.IP, host string) *route {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, r := range t.byPort[port] {
		if r.ip != nil && !r.ip.Equal(local) {
			continue
		}
		if hostMatches(r.host, host) {
			return r
		}
	}
	return nil
}

// ---- listeners

type wantListener struct {
	proto string
	addr  string
	port  int
}

func (s *Server) reconcileListeners(t *routeTable) {
	want := map[string]wantListener{}
	for port, routes := range t.byPort {
		proto := routes[0].binding.Protocol
		anyIP := false
		ips := map[string]bool{}
		for _, r := range routes {
			if r.ip == nil {
				anyIP = true
			} else {
				ips[r.ip.String()] = true
			}
		}
		// One socket per port when any binding uses all addresses: a specific
		// address and the wildcard cannot both be bound on Windows. Routes
		// for specific addresses are then matched on the local address.
		if anyIP {
			addr := net.JoinHostPort("", strconv.Itoa(port))
			want[proto+"|"+addr] = wantListener{proto, addr, port}
		} else {
			for ip := range ips {
				addr := net.JoinHostPort(ip, strconv.Itoa(port))
				want[proto+"|"+addr] = wantListener{proto, addr, port}
			}
		}
	}
	s.addHTTP3(want)

	s.mu.Lock()
	defer s.mu.Unlock()
	for key, l := range s.listeners {
		if _, ok := want[key]; !ok {
			delete(s.listeners, key)
			// Close the socket now so the port can be bound again right
			// away (for example by the same port switching protocol);
			// in-flight requests drain in the background.
			l.closing.Store(true)
			l.closeSocket()
			go func(l *listener) {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				l.shutdown(ctx)
			}(l)
			s.deps.Log.Info("listener closed", "proto", l.proto, "addr", l.addr)
		}
	}
	prevFailed := s.failed
	s.failed = map[string]error{}
	for key, w := range want {
		if _, ok := s.listeners[key]; ok {
			continue
		}
		l, err := s.listen(w)
		if err != nil {
			s.failed[w.failKey()] = err
			if prevFailed[w.failKey()] == nil { // report once, not on every retry
				s.deps.Bus.Error("server.listen", "", "cannot listen on %s (%s): %v", w.addr, w.proto, err)
			}
			continue
		}
		if prevFailed[w.failKey()] != nil {
			s.deps.Bus.Info("server.listen", "", "now listening on %s (%s)", w.addr, w.proto)
		}
		s.listeners[key] = l
	}
	s.updateH3Ports()
}

func (s *Server) listen(w wantListener) (*listener, error) {
	if w.proto == protoH3 {
		return s.listenH3(w)
	}
	st := s.deps.Settings()
	ln, err := net.Listen("tcp", w.addr)
	if err != nil {
		return nil, err
	}
	srv := &http.Server{
		Handler:           http.HandlerFunc(s.serve),
		ReadHeaderTimeout: time.Duration(max(st.Proxy.ReadHeaderTimeoutS, 5)) * time.Second,
		IdleTimeout:       time.Duration(max(st.Proxy.IdleTimeoutS, 10)) * time.Second,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          s.stdLog,
	}
	l := &listener{key: w.proto + "|" + w.addr, proto: w.proto, addr: w.addr, port: w.port, ln: ln, srv: srv}
	serve := func() error { return srv.Serve(ln) }
	if w.proto == "https" {
		// HTTP/2 stays wired up; whether it is offered is decided per
		// handshake by the ALPN list in tlsConfigFor, so the setting can be
		// changed without reopening listeners.
		srv.TLSConfig = &tls.Config{GetConfigForClient: s.tlsConfigFor}
		serve = func() error { return srv.ServeTLS(ln, "", "") }
	}
	go func() {
		if err := serve(); err != nil && !errors.Is(err, http.ErrServerClosed) && !l.closing.Load() {
			s.deps.Log.Error("listener stopped", "addr", w.addr, "err", err)
		}
	}()
	s.deps.Log.Info("listening", "proto", w.proto, "addr", w.addr)
	return l, nil
}

// tlsConfigFor picks settings per handshake so TLS settings apply without
// reopening listeners.
func (s *Server) tlsConfigFor(hello *tls.ClientHelloInfo) (*tls.Config, error) {
	st := s.deps.Settings()
	min := uint16(tls.VersionTLS12)
	if st.TLS.MinVersion == "1.3" {
		min = tls.VersionTLS13
	}
	protos := []string{"http/1.1"}
	if st.TLS.HTTP2 {
		protos = []string{"h2", "http/1.1"}
	}
	cfg := &tls.Config{
		MinVersion:     min,
		NextProtos:     protos,
		GetCertificate: s.getCertificate,
	}
	if r := s.handshakeRoute(hello); r != nil && r.client != nil {
		r.client.configure(cfg)
	}
	return cfg, nil
}

// getCertificate selects the certificate for a handshake from the binding
// that matches the local address and SNI host name.
func (s *Server) getCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	host := strings.ToLower(hello.ServerName)
	// No binding for this name: the port's default binding answers, so the
	// client gets a proper 404 page over TLS rather than an alert.
	r := s.handshakeRoute(hello)
	if r == nil {
		return nil, errors.New("no https binding on this port")
	}
	b := r.binding
	var c *tls.Certificate
	if b.CertMode == model.CertModeAuto {
		name := b.Host
		if name == "" {
			name = host
		}
		c = s.deps.Certs.ForHost(name)
	} else {
		c = s.deps.Certs.Get(b.CertificateID)
	}
	if c == nil {
		return nil, fmt.Errorf("binding %s has no usable certificate", b.String())
	}
	return c, nil
}

func (s *Server) hasHTTPPort80() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.listeners {
		if l.proto == "http" && l.port == 80 {
			return true
		}
	}
	return false
}

// Listeners describes open sockets and failures for the dashboard.
func (s *Server) Listeners() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, l := range s.listeners {
		out = append(out, fmt.Sprintf("%s %s", l.proto, l.addr))
	}
	for addr, err := range s.failed {
		out = append(out, fmt.Sprintf("FAILED %s: %v", addr, err))
	}
	slices.Sort(out)
	return out
}

// Shutdown closes every listener, waiting for in-flight requests.
func (s *Server) Shutdown(ctx context.Context) {
	s.mu.Lock()
	ls := s.listeners
	s.listeners = map[string]*listener{}
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, l := range ls {
		wg.Add(1)
		go func(l *listener) {
			defer wg.Done()
			l.shutdown(ctx)
		}(l)
	}
	wg.Wait()
}

// ---- request entry point

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if s.deps.Certs.HTTP.ServeHTTP(w, r) {
		return
	}
	port, local := 0, net.IP(nil)
	if a, ok := r.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr); ok {
		port, local = a.Port, a.IP
	}
	t := s.table.Load()
	route := t.match(port, local, hostOnly(r.Host))
	s.advertiseHTTP3(w, r, port, local, route)

	// Automatic IP banning comes first, before anything a site does; a
	// site that opts out neither refuses banned clients nor counts
	// against them.
	var banIP net.IP
	if bans := s.deps.Bans; bans != nil && (route == nil || !route.site.site.Routing.Banning.Exempt) {
		client := s.clientIP(r)
		r = r.WithContext(context.WithValue(r.Context(), ctxClientIP, client))
		banIP = net.ParseIP(client)
		traps := route == nil || !route.site.site.Routing.Banning.AllowTrapPaths
		if bans.Banned(banIP) || (traps && bans.Trap(banIP, r.URL.Path)) {
			refuseBanned(w)
			return
		}
	}

	st := s.deps.Settings()
	if route == nil {
		if st.Proxy.ServerHeader != "" {
			w.Header().Set("Server", st.Proxy.ServerHeader)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		if st.Proxy.DefaultPageHTML != "" {
			fmt.Fprint(w, st.Proxy.DefaultPageHTML)
		} else {
			fmt.Fprintf(w, defaultPage, templateEscape(hostOnly(r.Host)))
		}
		// Scanners mostly probe addresses, not host names.
		s.countForBan(banIP, r, http.StatusNotFound)
		return
	}
	rt := route.site
	start := time.Now()
	rw := &responseWriter{ResponseWriter: w, hdrs: rt.site.Routing.ResponseHeaders, server: st.Proxy.ServerHeader}
	defer func() {
		if p := recover(); p != nil {
			if p == http.ErrAbortHandler {
				panic(p)
			}
			s.deps.Log.Error("panic serving request", "site", rt.site.Name, "panic", p)
			if rw.status == 0 {
				errorPage(rw, rt.site.Routing.ErrorPages, http.StatusInternalServerError, "An internal error occurred.")
			}
		}
		d := time.Since(start)
		status := rw.status
		if status == 0 {
			status = http.StatusOK
		}
		in := r.ContentLength
		if in < 0 {
			in = 0
		}
		rt.stats.record(status, in, rw.written, d)
		rt.stats.countProto(r.ProtoMajor)
		if rt.access != nil {
			s.writeAccess(rt, r, status, rw.written, d)
		}
		s.countForBan(banIP, r, status)
	}()
	if !s.clientCertGate(rw, r, t, route, port, local) {
		return
	}
	rt.ServeHTTP(rw, r)
}

// countForBan counts an answer against the client for automatic IP
// banning. A 401 counts only for a failed sign-in (see triedPassword). 403
// does not count: it answers who the client is, not a failed sign-in, and
// is also what a ban or an IP restriction answers.
func (s *Server) countForBan(ip net.IP, r *http.Request, status int) {
	if ip == nil {
		return
	}
	switch {
	case status == http.StatusUnauthorized && triedPassword(r):
		s.deps.Bans.Record(ip, ipban.AuthFailure)
	case status == http.StatusNotFound:
		s.deps.Bans.Record(ip, ipban.NotFound)
	case status == http.StatusTooManyRequests:
		s.deps.Bans.Record(ip, ipban.RateLimited)
	}
}

// triedPassword reports whether a request that was answered 401 was a
// guess at a password: basic authentication credentials (NodeHoster's own
// or the application's), or a submitted form (a login page that answers
// 401). Everything else a 401 answers is an ordinary client whose session
// ran out: a page asked to sign in, a single-page app's expired bearer
// token, a heartbeat POST, a CORS preflight. Counting those would ban a
// whole office behind one NAT address from every site.
func triedPassword(r *http.Request) bool {
	if r.Method == http.MethodOptions {
		return false
	}
	if auth := r.Header.Get("Authorization"); auth != "" {
		scheme, _, _ := strings.Cut(strings.TrimSpace(auth), " ")
		return strings.EqualFold(scheme, "Basic")
	}
	if r.Method != http.MethodPost {
		return false
	}
	mt, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
	mt = strings.ToLower(strings.TrimSpace(mt))
	return mt == "application/x-www-form-urlencoded" || mt == "multipart/form-data"
}

// refuseBanned answers a banned client with a bare 403. Closing the
// connection instead would, behind a CDN or load balancer, drop a
// connection other clients' requests share and show up there as errors;
// Connection: close still ends a direct client's keep-alive.
func refuseBanned(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "close")
	w.WriteHeader(http.StatusForbidden)
	io.WriteString(w, "Forbidden\n")
}

// writeAccess appends a line in combined log format plus host and duration.
func (s *Server) writeAccess(rt *siteRuntime, r *http.Request, status int, size int64, d time.Duration) {
	user := "-"
	if u, _, ok := r.BasicAuth(); ok {
		user = u
	}
	ref, ua := r.Referer(), r.UserAgent()
	if ref == "" {
		ref = "-"
	}
	now, ip, ms := time.Now(), s.clientIP(r), float64(d.Microseconds())/1000
	fmt.Fprintf(rt.access, "%s - %s [%s] %q %d %d %q %q %s %.1fms\n",
		ip, user, now.Format("02/Jan/2006:15:04:05 -0700"),
		r.Method+" "+r.URL.RequestURI()+" "+r.Proto, status, size, ref, ua, r.Host, ms)
	if sh := s.deps.Ship; sh.Wants(model.LogSourceAccess) {
		sh.Ship(logship.Record{
			Time: now, Source: model.LogSourceAccess, Level: logship.AccessLevel(status), SiteID: rt.site.ID,
			Message: fmt.Sprintf("%s %s %d %d %.1fms", r.Method, r.URL.RequestURI(), status, size, ms),
			Access: &logship.AccessFields{Method: r.Method, Path: r.URL.RequestURI(), Status: status, Bytes: size, DurationMs: ms,
				ClientIP: ip, Host: r.Host, UserAgent: r.UserAgent(), Referer: r.Referer(), Protocol: r.Proto},
		})
	}
}

func templateEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "%", "%%")
	return r.Replace(s)
}

// trustsForwarded reports whether the direct peer is a trusted proxy whose
// X-Forwarded-For header may be believed.
func (s *Server) trustsForwarded(r *http.Request) bool {
	s.trustedMu.RLock()
	defer s.trustedMu.RUnlock()
	if len(s.trusted) == 0 {
		return false
	}
	return inNets(net.ParseIP(remoteIP(r)), s.trusted)
}

// clientIP is the real client address: the peer, or, behind trusted proxies,
// the right-most untrusted address in X-Forwarded-For. Only the entries
// trusted proxies appended are believed: an entry that cannot be read ends
// the walk at the peer, because everything to its left may have been
// written by the client (to pick someone else's address, or dodge a ban).
func (s *Server) clientIP(r *http.Request) string {
	if v, ok := r.Context().Value(ctxClientIP).(string); ok {
		return v
	}
	peer := remoteIP(r)
	if !s.trustsForwarded(r) {
		return peer
	}
	parts := strings.Split(forwardedFor(r), ",")
	s.trustedMu.RLock()
	defer s.trustedMu.RUnlock()
	for i := len(parts) - 1; i >= 0; i-- {
		ip := forwardedIP(parts[i])
		if ip == nil {
			return peer
		}
		if !inNets(ip, s.trusted) {
			return ip.String()
		}
	}
	return peer
}

// forwardedFor is the X-Forwarded-For chain, from every header line: a
// proxy that adds its own line rather than appending to the client's must
// not leave the client's line to be read as the whole chain.
func forwardedFor(r *http.Request) string {
	return strings.Join(r.Header.Values("X-Forwarded-For"), ",")
}

// forwardedIP reads an X-Forwarded-For entry: an address, or an address
// with a port as some load balancers write it (Azure Application Gateway:
// 203.0.113.7:51234, [2001:db8::7]:51234). nil when it is neither.
func forwardedIP(entry string) net.IP {
	entry = strings.TrimSpace(entry)
	if ip := net.ParseIP(entry); ip != nil {
		return ip
	}
	if host, _, err := net.SplitHostPort(entry); err == nil {
		return net.ParseIP(host)
	}
	if strings.HasPrefix(entry, "[") && strings.HasSuffix(entry, "]") {
		return net.ParseIP(entry[1 : len(entry)-1])
	}
	return nil
}

// ---- status for the API

func (s *Server) Traffic(id string) model.TrafficStats {
	return s.statsFor(id).snapshot()
}

func (s *Server) TakeMinute(id string) (req, errs int64, avgLatMs float64) {
	return s.statsFor(id).takeMinute()
}

func (s *Server) UpstreamStatus(id string) []model.UpstreamStatus {
	rt := s.runtime(id)
	if rt == nil || rt.pool == nil {
		return nil
	}
	return rt.pool.status()
}

func (s *Server) ForgetSite(id string) {
	s.statsMu.Lock()
	delete(s.stats, id)
	s.statsMu.Unlock()
}
