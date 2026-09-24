package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/remote"
	"github.com/parthh37/nodehoster/internal/secrets"
	"github.com/parthh37/nodehoster/internal/store"
)

// Server connections: other NodeHoster servers managed from this one (see
// model.ServerConnection). They are kept in a settings document rather
// than in Settings, so that saving the settings page never touches them,
// and travel in configuration backups with their tokens sealed.
const serversDoc = "servers"

// Health checks of the connections.
const (
	serverCheckEvery = 30 * time.Second
	serverCheckLimit = 8 // checks running at once
	// serverDownAfter consecutive failed checks raise remote.down: one
	// lost answer is not an outage.
	serverDownAfter = 2
)

// ErrServerNotFound: no connection has that ID.
var ErrServerNotFound = fmt.Errorf("no such server connection: %w", store.ErrNotFound)

type serverState struct {
	writeMu sync.Mutex // serializes changes (and their saving)

	mu     sync.Mutex // guards the fields below
	list   []model.ServerConnection
	health map[string]*serverHealth
	conns  map[string]*serverConn
}

// serverHealth is a connection's last check and what the monitor has
// reported of it.
type serverHealth struct {
	h        model.ServerHealth
	failures int  // consecutive failed checks
	down     bool // remote.down was raised and not yet remote.up
	// Whether the server applies the role limit, as the last check that
	// read the token's identity found (limitsKnown), kept while the server
	// is unreachable: the proxy relays non-administrators' requests only
	// to a server that does.
	limitsKnown, roleLimits bool
}

// serverConn is the HTTP client of a connection, rebuilt when its URL or
// pinned certificate changes. Connections are reused between requests.
type serverConn struct {
	base, fingerprint string
	tr                *http.Transport
	client            *http.Client
}

func (c *Core) openServers(ctx context.Context) error {
	c.servers.health = map[string]*serverHealth{}
	c.servers.conns = map[string]*serverConn{}
	var list []model.ServerConnection
	if err := c.Store.GetDoc(ctx, serversDoc, &list); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	c.servers.list = list
	return nil
}

// ServerConnections returns the connections, tokens sealed.
func (c *Core) ServerConnections() []model.ServerConnection {
	c.servers.mu.Lock()
	defer c.servers.mu.Unlock()
	return slices.Clone(c.servers.list)
}

// ServerViews returns the connections the filter admits (all with nil),
// tokens masked, with their health.
func (c *Core) ServerViews(keep func(model.ServerConnection) bool) []model.ServerView {
	c.servers.mu.Lock()
	defer c.servers.mu.Unlock()
	out := make([]model.ServerView, 0, len(c.servers.list))
	for _, s := range c.servers.list {
		if keep == nil || keep(s) {
			out = append(out, c.serverViewLocked(s))
		}
	}
	return out
}

// ServerView returns one connection, token masked, with its health.
func (c *Core) ServerView(id string) (model.ServerView, error) {
	c.servers.mu.Lock()
	defer c.servers.mu.Unlock()
	for _, s := range c.servers.list {
		if s.ID == id {
			return c.serverViewLocked(s), nil
		}
	}
	return model.ServerView{}, ErrServerNotFound
}

func (c *Core) serverViewLocked(s model.ServerConnection) model.ServerView {
	if s.Token != "" {
		s.Token = secrets.Mask
	}
	v := model.ServerView{ServerConnection: s}
	if h := c.servers.health[s.ID]; h != nil {
		v.Health = h.h
	}
	return v
}

// ServerConnection returns a connection, token sealed.
func (c *Core) ServerConnection(id string) (model.ServerConnection, error) {
	c.servers.mu.Lock()
	defer c.servers.mu.Unlock()
	for _, s := range c.servers.list {
		if s.ID == id {
			return s, nil
		}
	}
	return model.ServerConnection{}, ErrServerNotFound
}

// CreateServer adds a connection.
func (c *Core) CreateServer(ctx context.Context, in model.ServerConnection) (model.ServerView, error) {
	c.servers.writeMu.Lock()
	defer c.servers.writeMu.Unlock()
	in.ID = uuid.NewString()
	in.CreatedAt = time.Now().UTC()
	in.UpdatedAt = in.CreatedAt
	if in.Token == secrets.Mask {
		in.Token = ""
	}
	if err := c.prepareServer(&in, nil); err != nil {
		return model.ServerView{}, err
	}
	list := append(c.ServerConnections(), in)
	if err := c.saveServers(ctx, list); err != nil {
		return model.ServerView{}, err
	}
	c.checkServerSoon(in.ID)
	return c.ServerView(in.ID)
}

// UpdateServer changes a connection. A masked token keeps the stored one,
// unless the URL points elsewhere: the token was created for that server
// and must not be sent to another without being entered again.
func (c *Core) UpdateServer(ctx context.Context, id string, in model.ServerConnection) (model.ServerView, error) {
	c.servers.writeMu.Lock()
	defer c.servers.writeMu.Unlock()
	list := c.ServerConnections()
	i := slices.IndexFunc(list, func(s model.ServerConnection) bool { return s.ID == id })
	if i < 0 {
		return model.ServerView{}, ErrServerNotFound
	}
	cur := list[i]
	in.ID, in.CreatedAt, in.UpdatedAt = id, cur.CreatedAt, time.Now().UTC()
	if err := c.prepareServer(&in, &cur); err != nil {
		return model.ServerView{}, err
	}
	list[i] = in
	if err := c.saveServers(ctx, list); err != nil {
		return model.ServerView{}, err
	}
	c.servers.mu.Lock()
	if in.URL != cur.URL || in.Fingerprint != cur.Fingerprint || in.Token != cur.Token {
		delete(c.servers.health, id) // another server, or another way in: start over
	}
	c.servers.mu.Unlock()
	c.checkServerSoon(id)
	return c.ServerView(id)
}

// DeleteServer removes a connection.
func (c *Core) DeleteServer(ctx context.Context, id string) (model.ServerConnection, error) {
	c.servers.writeMu.Lock()
	defer c.servers.writeMu.Unlock()
	list := c.ServerConnections()
	i := slices.IndexFunc(list, func(s model.ServerConnection) bool { return s.ID == id })
	if i < 0 {
		return model.ServerConnection{}, ErrServerNotFound
	}
	gone := list[i]
	list = slices.Delete(list, i, i+1)
	if err := c.saveServers(ctx, list); err != nil {
		return model.ServerConnection{}, err
	}
	return gone, nil
}

// prepareServer normalizes, validates and seals a connection; cur is the
// stored one when it is being changed.
func (c *Core) prepareServer(in *model.ServerConnection, cur *model.ServerConnection) error {
	in.ApplyDefaults()
	var others []model.ServerConnection
	for _, s := range c.ServerConnections() {
		if s.ID != in.ID {
			others = append(others, s)
		}
	}
	masked := in.Token == secrets.Mask
	if masked {
		if cur == nil || cur.Token == "" {
			return &model.ValidationError{Field: "token", Message: "enter an API token created on that server"}
		}
		if !sameServer(in.URL, cur.URL) {
			return &model.ValidationError{Field: "token", Message: "enter the token again: the URL now points to another server"}
		}
		in.Token = "placeholder" // validated below, restored after
	}
	if err := in.Validate(others); err != nil {
		return err
	}
	if masked {
		in.Token = cur.Token
		return nil
	}
	sealed, err := c.Box.Seal(in.Token)
	if err != nil {
		return err
	}
	in.Token = sealed
	return nil
}

// sameServer reports whether two normalized URLs reach the same server
// (scheme, host and port; the path may change).
func sameServer(a, b string) bool {
	origin := func(u string) string {
		scheme, rest, _ := strings.Cut(u, "://")
		host, _, _ := strings.Cut(rest, "/")
		return scheme + "://" + host
	}
	return origin(a) == origin(b)
}

func (c *Core) saveServers(ctx context.Context, list []model.ServerConnection) error {
	if list == nil {
		list = []model.ServerConnection{}
	}
	if err := c.Store.PutDoc(ctx, serversDoc, list); err != nil {
		return err
	}
	c.servers.mu.Lock()
	defer c.servers.mu.Unlock()
	c.servers.list = list
	for id, sc := range c.servers.conns {
		if !slices.ContainsFunc(list, func(s model.ServerConnection) bool {
			return s.ID == id && s.URL == sc.base && s.Fingerprint == sc.fingerprint
		}) {
			sc.tr.CloseIdleConnections()
			delete(c.servers.conns, id)
		}
	}
	for id := range c.servers.health {
		if !slices.ContainsFunc(list, func(s model.ServerConnection) bool { return s.ID == id }) {
			delete(c.servers.health, id)
		}
	}
	return nil
}

// restoreServers replaces the connections with a backup's (tokens already
// resealed for this server).
func (c *Core) restoreServers(ctx context.Context, list []model.ServerConnection) error {
	c.servers.writeMu.Lock()
	defer c.servers.writeMu.Unlock()
	for i := range list {
		list[i].ApplyDefaults()
	}
	return c.saveServers(ctx, list)
}

// ServerClient returns what a request to a connection needs: the
// connection, its HTTP client (no redirects followed, TLS verified or
// pinned) and its token in clear.
func (c *Core) ServerClient(id string) (model.ServerConnection, *http.Client, string, error) {
	s, err := c.ServerConnection(id)
	if err != nil {
		return s, nil, "", err
	}
	token, err := c.Box.Unseal(s.Token)
	if err != nil {
		return s, nil, "", fmt.Errorf("the token of %s cannot be read: %w; enter it again", s.Name, err)
	}
	c.servers.mu.Lock()
	defer c.servers.mu.Unlock()
	sc := c.servers.conns[id]
	if sc == nil || sc.base != s.URL || sc.fingerprint != s.Fingerprint {
		if sc != nil {
			sc.tr.CloseIdleConnections()
		}
		tr, err := remote.NewTransport(s.URL, s.Fingerprint)
		if err != nil {
			return s, nil, "", err
		}
		sc = &serverConn{base: s.URL, fingerprint: s.Fingerprint, tr: tr, client: remote.NewClient(tr)}
		c.servers.conns[id] = sc
	}
	return s, sc.client, token, nil
}

// CheckServer checks a connection now and records the result.
func (c *Core) CheckServer(ctx context.Context, id string) (model.ServerHealth, error) {
	s, client, token, err := c.ServerClient(id)
	if errors.Is(err, ErrServerNotFound) {
		return model.ServerHealth{}, err
	}
	var h model.ServerHealth
	if err != nil {
		now := time.Now()
		h = model.ServerHealth{CheckedAt: &now, Error: err.Error()}
	} else {
		h = remote.Check(ctx, client, s.URL, token)
	}
	if err := ctx.Err(); err != nil {
		return h, err // shutting down (or the caller left): not the server's fault
	}
	return c.recordServerHealth(s, h), nil
}

// recordServerHealth keeps a check's result and raises remote.down after
// serverDownAfter failures in a row, remote.up when a server that was
// reported down answers again. It returns the health as recorded.
func (c *Core) recordServerHealth(s model.ServerConnection, h model.ServerHealth) model.ServerHealth {
	c.servers.mu.Lock()
	if !slices.ContainsFunc(c.servers.list, func(x model.ServerConnection) bool { return x.ID == s.ID && x.URL == s.URL }) {
		c.servers.mu.Unlock()
		return h // deleted or changed while it was checked
	}
	st := c.servers.health[s.ID]
	if st == nil {
		st = &serverHealth{}
		c.servers.health[s.ID] = st
	}
	prev := st.h
	h.Since = prev.Since
	if prev.CheckedAt == nil || prev.Reachable != h.Reachable || h.Since == nil {
		h.Since = h.CheckedAt
	}
	if h.Reachable && h.User != "" {
		st.limitsKnown, st.roleLimits = true, h.RoleLimits
	}
	var up, down bool
	if h.Reachable {
		st.failures = 0
		up, st.down = st.down, false
	} else {
		st.failures++
		if st.failures >= serverDownAfter && !st.down {
			st.down, down = true, true
		}
	}
	st.h = h
	c.servers.mu.Unlock()

	switch {
	case down:
		c.Bus.Warn(events.RemoteDown, "", "Server %s (%s) is unreachable: %s", s.Name, s.URL, h.Error)
	case up:
		c.Bus.Info(events.RemoteUp, "", "Server %s (%s) is reachable again", s.Name, s.URL)
	}
	return h
}

// ServerRoleLimits reports whether a connection's server applies the role
// limit (model.RoleLimitHeader); known is false until a check has read it
// since the connection last changed.
func (c *Core) ServerRoleLimits(id string) (limits, known bool) {
	c.servers.mu.Lock()
	defer c.servers.mu.Unlock()
	if st := c.servers.health[id]; st != nil {
		return st.roleLimits, st.limitsKnown
	}
	return false, false
}

// TestServer tries a connection being set up: the certificate the server
// presents, and whether the token works when the TLS connection can be
// trusted as configured. With in.ID set and the token masked, the stored
// token is used, if the URL still points to the same server.
func (c *Core) TestServer(ctx context.Context, in model.ServerTest) (model.ServerTestResult, error) {
	base, err := model.NormalizeServerURL(in.URL)
	if err != nil {
		return model.ServerTestResult{}, &model.ValidationError{Field: "url", Message: err.Error()}
	}
	fp, err := model.ParseFingerprint(in.Fingerprint)
	if err != nil {
		return model.ServerTestResult{}, &model.ValidationError{Field: "fingerprint", Message: err.Error()}
	}
	token := strings.TrimSpace(in.Token)
	if token == secrets.Mask {
		cur, err := c.ServerConnection(in.ID)
		if err != nil {
			return model.ServerTestResult{}, &model.ValidationError{Field: "token", Message: "enter an API token created on that server"}
		}
		if !sameServer(base, cur.URL) {
			return model.ServerTestResult{}, &model.ValidationError{Field: "token", Message: "enter the token again: the URL now points to another server"}
		}
		if token, err = c.Box.Unseal(cur.Token); err != nil {
			return model.ServerTestResult{}, &model.ValidationError{Field: "token", Message: "the stored token cannot be read: enter it again"}
		}
	}
	if token == "" {
		return model.ServerTestResult{}, &model.ValidationError{Field: "token", Message: "enter an API token created on that server"}
	}

	var res model.ServerTestResult
	now := time.Now()
	res.Health.CheckedAt = &now
	cert, err := remote.Probe(ctx, base)
	if err != nil {
		res.Health.Error = err.Error()
		return res, nil
	}
	res.Certificate = cert
	switch {
	case cert == nil: // plain HTTP to this computer
		res.Trusted = true
	case fp != "":
		res.Trusted = cert.Fingerprint == fp
		if !res.Trusted {
			res.Health.Error = "the server presented another certificate than the pinned one: compare its fingerprint below with the server's"
		}
	default:
		res.Trusted = cert.Verified
		if !res.Trusted {
			res.Health.Error = "the certificate is not trusted (" + cert.VerifyError + "): compare its fingerprint with the server's and pin it to connect"
		}
	}
	if !res.Trusted {
		return res, nil // the token is not sent to a server that is not trusted
	}
	tr, err := remote.NewTransport(base, fp)
	if err != nil {
		return res, err
	}
	defer tr.CloseIdleConnections()
	res.Health = remote.Check(ctx, remote.NewClient(tr), base, token)
	return res, nil
}

// checkServerSoon checks one connection in the background, so that a new
// or changed connection shows its health without waiting for the monitor.
func (c *Core) checkServerSoon(id string) {
	if c.ctx == nil {
		return // not started
	}
	// Not in c.wg: a request may add a connection while the server shuts
	// down. The check ends with c.ctx, and then records nothing.
	go c.CheckServer(c.ctx, id)
}

// serverMonitor checks every connection every serverCheckEvery, a few at
// a time, until ctx ends.
func (c *Core) serverMonitor(ctx context.Context) {
	t := time.NewTicker(serverCheckEvery)
	defer t.Stop()
	for {
		c.checkAllServers(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (c *Core) checkAllServers(ctx context.Context) {
	sem := make(chan struct{}, serverCheckLimit)
	var wg sync.WaitGroup
	for _, s := range c.ServerConnections() {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return
		}
		wg.Go(func() {
			defer func() { <-sem }()
			c.CheckServer(ctx, s.ID)
		})
	}
	wg.Wait()
}
