// Package remote reaches other NodeHoster servers through the HTTPS API of
// their web console, with an API token created there: the proxy and health
// checks of a server's connections (Servers in the web console), NodeHoster
// Manager's "Connect to a server…" and `nodehoster --server`.
//
// TLS is verified against the trusted roots by default. Admin listeners
// often have self-signed certificates, so a connection may pin the
// certificate's SHA-256 fingerprint instead, shown on first connect for
// the administrator to confirm (trust on first use); verification is never
// simply turned off. Redirects are not followed: a connection only ever
// talks to the URL it was configured with.
//
// The package does not link the server, so the desktop programs use it.
package remote

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// Timeouts. Requests themselves are bounded by their context: a proxied
// log stream or upload may last as long as it needs.
const (
	DialTimeout           = 10 * time.Second
	TLSHandshakeTimeout   = 10 * time.Second
	ResponseHeaderTimeout = 5 * time.Minute // a restore answers once done
	// CheckTimeout bounds a health check (and a connection test).
	CheckTimeout = 10 * time.Second
)

// ErrFingerprintMismatch: the server presented a certificate other than the
// pinned one (renewed, replaced, or someone in between).
var ErrFingerprintMismatch = errors.New("the server's certificate does not match the pinned fingerprint")

// Fingerprint is the SHA-256 fingerprint of a DER certificate, as
// model.ParseFingerprint normalizes it.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// TLSConfig is the client TLS configuration of a connection to host: the
// certificate must match fingerprint when one is pinned, and verify for
// host against the trusted roots otherwise.
func TLSConfig(host, fingerprint string) *tls.Config {
	cfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if fingerprint == "" {
		return cfg
	}
	// The pin replaces chain and name verification (which a self-signed
	// certificate fails), it does not skip verification: the handshake
	// fails unless the leaf is exactly the pinned certificate.
	cfg.InsecureSkipVerify = true
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return ErrFingerprintMismatch
		}
		if got := Fingerprint(cs.PeerCertificates[0].Raw); got != fingerprint {
			return fmt.Errorf("%w: it presented %s", ErrFingerprintMismatch, model.FormatFingerprint(got))
		}
		return nil
	}
	return cfg
}

// NewTransport returns the transport of a connection to base (a URL
// normalized by model.NormalizeServerURL) with an optional pinned
// fingerprint. Proxy environment variables are honoured.
func NewTransport(base, fingerprint string) (*http.Transport, error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	d := &net.Dialer{Timeout: DialTimeout, KeepAlive: 30 * time.Second}
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           d.DialContext,
		TLSClientConfig:       TLSConfig(u.Hostname(), fingerprint),
		TLSHandshakeTimeout:   TLSHandshakeTimeout,
		ResponseHeaderTimeout: ResponseHeaderTimeout,
		ExpectContinueTimeout: time.Second,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
		// Bodies travel as the server sends them: a proxied response is
		// forwarded as it is, and event streams must not be buffered.
		DisableCompression: true,
		ForceAttemptHTTP2:  true,
	}, nil
}

// NewClient returns an HTTP client for a connection that never follows
// redirects (the answer is returned as it is), with no overall timeout:
// callers bound requests with their context.
func NewClient(rt http.RoundTripper) *http.Client {
	return &http.Client{
		Transport: rt,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Probe connects to base and describes the certificate it presents,
// whether or not it can be trusted, so that an administrator can compare
// its fingerprint with the server's before pinning it. Over plain HTTP
// (to this computer) there is no certificate: nil, nil.
func Probe(ctx context.Context, base string) (*model.PeerCertificate, error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" {
		return nil, nil
	}
	host := u.Hostname()
	addr := u.Host
	if u.Port() == "" {
		addr = net.JoinHostPort(host, "443")
	}
	ctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	d := tls.Dialer{
		NetDialer: &net.Dialer{Timeout: DialTimeout},
		// Only to read the certificate: nothing is sent on this connection.
		Config: &tls.Config{ServerName: host, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12},
	}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", u.Host, unwrapNet(err))
	}
	defer conn.Close()
	chain := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(chain) == 0 {
		return nil, fmt.Errorf("%s presented no certificate", u.Host)
	}
	return describeCert(chain, host), nil
}

func describeCert(chain []*x509.Certificate, host string) *model.PeerCertificate {
	leaf := chain[0]
	pc := &model.PeerCertificate{
		Fingerprint: Fingerprint(leaf.Raw),
		Subject:     leaf.Subject.String(),
		Issuer:      leaf.Issuer.String(),
		DNSNames:    append([]string{}, leaf.DNSNames...),
		NotBefore:   leaf.NotBefore,
		NotAfter:    leaf.NotAfter,
	}
	for _, ip := range leaf.IPAddresses {
		pc.DNSNames = append(pc.DNSNames, ip.String())
	}
	inter := x509.NewCertPool()
	for _, c := range chain[1:] {
		inter.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: host, Intermediates: inter}); err != nil {
		pc.VerifyError = err.Error()
	} else {
		pc.Verified = true
	}
	return pc
}

// Check reads a server's health with the token: its information (version,
// load), its sites by state and who the token is. It never takes longer
// than CheckTimeout. A server that answers but refuses the token is not
// reachable: the connection is of no use until it is fixed.
func Check(ctx context.Context, c *http.Client, base, token string) model.ServerHealth {
	ctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	now := time.Now()
	h := model.ServerHealth{CheckedAt: &now}

	var info model.ServerInfo
	start := time.Now()
	err := GetJSON(ctx, c, base, token, "/api/server/info", &info)
	h.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		var se *StatusError
		if errors.As(err, &se) && se.Status == http.StatusNotFound {
			err = fmt.Errorf("%s does not answer like a NodeHoster web console (HTTP 404 for /api/server/info)", base)
		}
		h.Error = Describe(err)
		return h
	}
	h.Reachable = true
	h.Version, h.Commit, h.Hostname, h.OS = info.Version, info.Commit, info.Hostname, info.OS
	h.CPUPercent, h.CPUCount, h.MemTotal, h.MemUsed = info.CPUPercent, info.CPUCount, info.MemTotal, info.MemUsed

	var sites []struct {
		Status struct {
			State model.SiteState `json:"state"`
		} `json:"status"`
	}
	if err := GetJSON(ctx, c, base, token, "/api/sites", &sites); err == nil {
		h.Sites = len(sites)
		for _, s := range sites {
			switch s.Status.State {
			case model.StateRunning:
				h.Running++
			case model.StateDegraded:
				h.Degraded++
			case model.StateFailed:
				h.Failed++
			case model.StateStopped:
				h.Stopped++
			}
		}
	}
	var me struct {
		User   model.User `json:"user"`
		Access struct {
			Role model.Role `json:"role"`
		} `json:"access"`
	}
	// With the limit set to admin, which takes nothing away: a server
	// that echoes it applies the limits the proxy sends for other roles.
	limit := http.Header{model.RoleLimitHeader: {string(model.RoleAdmin)}}
	if hdr, err := getJSON(ctx, c, base, token, "/api/auth/me", limit, &me); err == nil {
		h.User, h.Role = me.User.Username, me.Access.Role
		if h.Role == "" {
			h.Role = me.User.Role // servers from before per-site permissions
		}
		h.RoleLimits = hdr.Get(model.RoleLimitAppliedHeader) == string(model.RoleAdmin)
	}
	return h
}

// StatusError is an HTTP error answer of a remote server.
type StatusError struct {
	Status  int
	Message string
}

func (e *StatusError) Error() string {
	if e.Message == "" {
		return http.StatusText(e.Status)
	}
	return e.Message
}

// maxJSON bounds a JSON answer read by GetJSON.
const maxJSON = 32 << 20

// GetJSON GETs base+path with the token and decodes the JSON answer.
func GetJSON(ctx context.Context, c *http.Client, base, token, path string, out any) error {
	_, err := getJSON(ctx, c, base, token, path, nil, out)
	return err
}

// getJSON is GetJSON with more request headers, returning the answer's.
func getJSON(ctx context.Context, c *http.Client, base, token, path string, hdr http.Header, out any) (http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return resp.Header, ReadError(resp)
	}
	return resp.Header, json.NewDecoder(io.LimitReader(resp.Body, maxJSON)).Decode(out)
}

// ReadError turns an error answer into a StatusError, with the API's
// message when it sent one.
func ReadError(resp *http.Response) error {
	var body struct {
		Error string `json:"error"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	msg := ""
	if json.Unmarshal(data, &body) == nil {
		msg = body.Error
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		msg = fmt.Sprintf("the server redirects to %s: enter the web console's own URL", resp.Header.Get("Location"))
	}
	if msg == "" {
		msg = resp.Status
	}
	return &StatusError{Status: resp.StatusCode, Message: msg}
}

// Describe turns a failure to reach a server into a sentence for an
// administrator.
func Describe(err error) string {
	var se *StatusError
	var ua x509.UnknownAuthorityError
	var hn x509.HostnameError
	var ci x509.CertificateInvalidError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &se) && se.Status == http.StatusUnauthorized:
		return "the server refused the API token: it was revoked, has expired or was mistyped"
	case errors.As(err, &se):
		return fmt.Sprintf("the server answered %d: %s", se.Status, se.Message)
	case errors.Is(err, ErrFingerprintMismatch):
		return unwrapURL(err).Error() + ". If the server's certificate was replaced, compare its new fingerprint and pin it again"
	case errors.As(err, &ua):
		return "the server's certificate is not issued by a trusted authority (self-signed?): test the connection and pin its fingerprint"
	case errors.As(err, &hn):
		return "the server's certificate is not valid for this host name: " + hn.Error()
	case errors.As(err, &ci):
		return "the server's certificate is not valid: " + ci.Error()
	case errors.Is(err, context.DeadlineExceeded):
		return "the server did not answer in time"
	}
	return unwrapNet(unwrapURL(err)).Error()
}

func unwrapURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// unwrapNet shortens "dial tcp 10.0.0.5:8484: connect: connection refused"
// to what matters.
func unwrapNet(err error) error {
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Err != nil {
		op := oe.Op
		if op == "dial" {
			op = "cannot connect"
		}
		if se, ok := oe.Err.(interface{ Unwrap() error }); ok && se.Unwrap() != nil {
			return fmt.Errorf("%s: %w", op, se.Unwrap())
		}
		return fmt.Errorf("%s: %w", op, oe.Err)
	}
	return err
}
