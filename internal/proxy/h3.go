package proxy

// HTTP/3: with Settings.tls.http3 on, every HTTPS listener gets a QUIC
// (UDP) listener on the same address and port, serving the same handler
// with the same per-handshake TLS configuration (SNI certificate choice,
// client certificates, OCSP staples). HTTP/1.1 and HTTP/2 responses
// advertise it with Alt-Svc; browsers switch on a later request.

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

// protoH3 is the listener protocol of a QUIC listener.
const protoH3 = "h3"

// altSvcMaxAge is how long clients may remember the advertisement: a day,
// so that switching HTTP/3 off is noticed soon (clients fall back to TCP
// by themselves meanwhile).
const altSvcMaxAge = 86400

// h3Ports are the ports with an open QUIC listener, for Alt-Svc: all
// addresses, or specific ones.
type h3Ports struct {
	any map[int]bool
	ips map[int]map[string]bool
}

// h3Listener is a QUIC listener's socket and server.
type h3Listener struct {
	conn net.PacketConn
	srv  *http3.Server
}

// addHTTP3 wants a QUIC listener next to each HTTPS one when enabled.
func (s *Server) addHTTP3(want map[string]wantListener) {
	if !s.deps.Settings().TLS.HTTP3 {
		return
	}
	for _, w := range want {
		if w.proto == "https" {
			w.proto = protoH3
			want[protoH3+"|"+w.addr] = w
		}
	}
}

// failKey names a listen failure: an HTTPS listener and its QUIC sibling
// share an address.
func (w wantListener) failKey() string {
	if w.proto == protoH3 {
		return w.addr + " (UDP)"
	}
	return w.addr
}

func (s *Server) listenH3(w wantListener) (*listener, error) {
	st := s.deps.Settings()
	pc, err := net.ListenPacket("udp", w.addr)
	if err != nil {
		return nil, err
	}
	idle := time.Duration(max(st.Proxy.IdleTimeoutS, 10)) * time.Second
	srv := &http3.Server{
		Handler: http.HandlerFunc(s.serve),
		// The same per-handshake choice as TCP; http3 offers only "h3"
		// and QUIC requires TLS 1.3.
		TLSConfig: &tls.Config{GetConfigForClient: s.tlsConfigFor},
		QUICConfig: &quic.Config{
			// 0-RTT requests can be replayed by an attacker; applications
			// behind a proxy do not expect that.
			Allow0RTT:      false,
			MaxIdleTimeout: idle,
		},
		IdleTimeout:    idle,
		MaxHeaderBytes: 1 << 20,
		ConnContext:    quicLocalAddr,
		Logger:         s.deps.Log,
	}
	l := &listener{key: protoH3 + "|" + w.addr, proto: protoH3, addr: w.addr, port: w.port, h3: &h3Listener{conn: pc, srv: srv}}
	go func() {
		if err := srv.Serve(pc); err != nil && !errors.Is(err, http.ErrServerClosed) && !l.closing.Load() {
			s.deps.Log.Error("listener stopped", "proto", protoH3, "addr", w.addr, "err", err)
		}
	}()
	s.deps.Log.Info("listening", "proto", protoH3, "addr", w.addr)
	return l, nil
}

// quicLocalAddr gives HTTP/3 requests their local address the way
// net/http does (a *net.TCPAddr), which binding matching and the rewrite
// engine's {SERVER_PORT} read.
func quicLocalAddr(ctx context.Context, c *quic.Conn) context.Context {
	if u, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return context.WithValue(ctx, http.LocalAddrContextKey, &net.TCPAddr{IP: u.IP, Port: u.Port, Zone: u.Zone})
	}
	return ctx
}

// closeSocket stops a listener taking new connections at once. A QUIC
// socket also carries the connections in progress, so it is closed only
// by shutdown.
func (l *listener) closeSocket() {
	if l.h3 == nil {
		l.ln.Close()
	}
}

// shutdown drains a listener: requests in progress finish (HTTP/3 clients
// get GOAWAY) until ctx ends.
func (l *listener) shutdown(ctx context.Context) {
	if l.h3 == nil {
		l.srv.Shutdown(ctx)
		return
	}
	l.h3.srv.Shutdown(ctx)
	l.h3.conn.Close()
}

// updateH3Ports records the open QUIC listeners for Alt-Svc (s.mu held).
func (s *Server) updateH3Ports() {
	p := &h3Ports{any: map[int]bool{}, ips: map[int]map[string]bool{}}
	for _, l := range s.listeners {
		if l.proto != protoH3 {
			continue
		}
		host, _, _ := net.SplitHostPort(l.addr)
		if host == "" {
			p.any[l.port] = true
			continue
		}
		if p.ips[l.port] == nil {
			p.ips[l.port] = map[string]bool{}
		}
		if ip := net.ParseIP(host); ip != nil {
			p.ips[l.port][ip.String()] = true
		}
	}
	s.h3.Store(p)
}

// advertiseHTTP3 adds Alt-Svc to HTTP/1.1 and HTTP/2 responses on a port
// with a QUIC listener. Not for bindings to a specific address on a port
// whose listeners use all addresses: over QUIC the local address a request
// arrived on may be unknown (Windows), so another binding could answer.
func (s *Server) advertiseHTTP3(w http.ResponseWriter, r *http.Request, port int, local net.IP, rt *route) {
	if r.TLS == nil || r.ProtoMajor >= 3 {
		return
	}
	p := s.h3.Load()
	if p == nil {
		return
	}
	ok := p.ips[port][local.String()]
	if !ok && p.any[port] {
		ok = rt == nil || rt.ip == nil
	}
	if ok {
		w.Header().Add("Alt-Svc", `h3=":`+strconv.Itoa(port)+`"; ma=`+strconv.Itoa(altSvcMaxAge))
	}
}

// HTTP3Listeners lists the open QUIC listeners and failed ones.
func (s *Server) HTTP3Listeners() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for _, l := range s.listeners {
		if l.proto == protoH3 {
			out = append(out, "udp "+l.addr)
		}
	}
	for key, err := range s.failed {
		if addr, ok := strings.CutSuffix(key, " (UDP)"); ok {
			out = append(out, fmt.Sprintf("FAILED udp %s: %v", addr, err))
		}
	}
	slices.Sort(out)
	return out
}

// countProto counts a site's requests by HTTP version.
func (st *siteStats) countProto(major int) {
	switch major {
	case 3:
		st.h3.Add(1)
	case 2:
		st.h2.Add(1)
	default:
		st.h1.Add(1)
	}
}

// ProtocolStats are a site's requests by HTTP version since start.
type ProtocolStats struct {
	HTTP1, HTTP2, HTTP3 int64
}

func (s *Server) Protocols(id string) ProtocolStats {
	st := s.statsFor(id)
	return ProtocolStats{HTTP1: st.h1.Load(), HTTP2: st.h2.Load(), HTTP3: st.h3.Load()}
}
