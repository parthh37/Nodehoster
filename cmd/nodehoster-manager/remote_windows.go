package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/parthh37/nodehoster/internal/desktop"
	"github.com/parthh37/nodehoster/internal/localapi"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/remote"
	"github.com/tailscale/walk"
	. "github.com/tailscale/walk/declarative"
)

// ---- remote servers: "Connect to a server…", like IIS Manager
//
// Besides this server (over the local admin pipe), the connections tree
// lists the servers the Windows user connected to: their web console's
// HTTPS API with an API token created there, saved for this Windows
// account (remote.Saved: the token protected with DPAPI in the user's
// scope, shared with `nodehoster server add`). Selecting a server's node
// makes it the one every page works on: the pages talk to an apiClient,
// which is the pipe or HTTPS alike. What needs this computer (the Windows
// service, its data folder and log files) is disabled for remote servers.

// apiClient is what the pages use to reach the server shown: the local
// admin pipe or another server's web console (localapi.Client either way).
type apiClient interface {
	Do(ctx context.Context, method, path string, in, out any) error
	Get(ctx context.Context, path string, out any) error
	Post(ctx context.Context, path string, in, out any) error
	Put(ctx context.Context, path string, in, out any) error
	Delete(ctx context.Context, path string) error
	UploadFile(ctx context.Context, path, field, filename string, r io.Reader, out any) error
	Download(ctx context.Context, path string, w io.Writer) (string, int64, error)
	Upload(ctx context.Context, path, contentType string, body io.Reader, header http.Header, out any) error
	Stream(ctx context.Context, path string, fn func(event string, data []byte)) error
	Sites(ctx context.Context) ([]localapi.Site, error)
	Site(ctx context.Context, id string) (*localapi.Site, error)
	SiteAction(ctx context.Context, id, action string) error
	UpdateSite(ctx context.Context, s *model.Site) (*localapi.Site, error)
	ServerInfo(ctx context.Context) (*model.ServerInfo, error)
}

var _ apiClient = (*localapi.Client)(nil)

// switchClient is the apiClient of the server selected in the tree. Pages
// load off the UI thread, so the switch is atomic; gen tells a result of
// the previous server from one of the current.
type switchClient struct {
	mu   sync.Mutex
	cur  *localapi.Client
	conn *remote.Saved // nil: this server
	gen  atomic.Uint64
}

func (s *switchClient) set(c *localapi.Client, conn *remote.Saved) {
	s.mu.Lock()
	s.cur, s.conn = c, conn
	s.mu.Unlock()
	s.gen.Add(1)
}

// snapshot returns the current client, its connection and generation.
func (s *switchClient) snapshot() (*localapi.Client, *remote.Saved, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur, s.conn, s.gen.Load()
}

func (s *switchClient) c() *localapi.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

func (s *switchClient) Do(ctx context.Context, method, path string, in, out any) error {
	return s.c().Do(ctx, method, path, in, out)
}
func (s *switchClient) Get(ctx context.Context, path string, out any) error {
	return s.c().Get(ctx, path, out)
}
func (s *switchClient) Post(ctx context.Context, path string, in, out any) error {
	return s.c().Post(ctx, path, in, out)
}
func (s *switchClient) Put(ctx context.Context, path string, in, out any) error {
	return s.c().Put(ctx, path, in, out)
}
func (s *switchClient) Delete(ctx context.Context, path string) error {
	return s.c().Delete(ctx, path)
}
func (s *switchClient) UploadFile(ctx context.Context, path, field, filename string, r io.Reader, out any) error {
	return s.c().UploadFile(ctx, path, field, filename, r, out)
}
func (s *switchClient) Download(ctx context.Context, path string, w io.Writer) (string, int64, error) {
	return s.c().Download(ctx, path, w)
}
func (s *switchClient) Upload(ctx context.Context, path, contentType string, body io.Reader, header http.Header, out any) error {
	return s.c().Upload(ctx, path, contentType, body, header, out)
}
func (s *switchClient) Stream(ctx context.Context, path string, fn func(event string, data []byte)) error {
	return s.c().Stream(ctx, path, fn)
}
func (s *switchClient) Sites(ctx context.Context) ([]localapi.Site, error) { return s.c().Sites(ctx) }
func (s *switchClient) Site(ctx context.Context, id string) (*localapi.Site, error) {
	return s.c().Site(ctx, id)
}
func (s *switchClient) SiteAction(ctx context.Context, id, action string) error {
	return s.c().SiteAction(ctx, id, action)
}
func (s *switchClient) UpdateSite(ctx context.Context, site *model.Site) (*localapi.Site, error) {
	return s.c().UpdateSite(ctx, site)
}
func (s *switchClient) ServerInfo(ctx context.Context) (*model.ServerInfo, error) {
	return s.c().ServerInfo(ctx)
}

// remoteConn is the connection of the server shown (nil: this server). UI
// thread.
func (m *manager) remoteConn() *remote.Saved {
	_, conn, _ := m.sw.snapshot()
	return conn
}

func (m *manager) remote() bool { return m.remoteConn() != nil }

// ---- commands that need this computer

// viewingRemote is set while a remote server is shown (UI thread only):
// setEnabled keeps the local-only commands disabled meanwhile.
var viewingRemote bool

// localOnly are the commands that act on this computer: its Windows
// service, its data folder and files on its disk.
var localOnly = map[*command]bool{}

// allowedHere reports whether a command may be enabled for the server
// shown.
func allowedHere(c *command) bool { return !viewingRemote || !localOnly[c] }

// markLocalOnly registers the commands that cannot work on a remote
// server. Called once the pages made theirs.
func (m *manager) markLocalOnly() {
	for _, c := range []*command{
		m.cmdStart, m.cmdStop, m.cmdRestart, // the Windows service here
		m.cmdSettings, // restarts the service here afterwards
		m.cmdDataFolder, m.cmdServerLog,
		m.sitesPg.cmds.explore, m.sitePg.cmds.explore,
		m.sitePg.logFolder, m.sitePg.taskLogs, m.node.folder,
	} {
		if c != nil {
			localOnly[c] = true
		}
	}
}

// applyLocalOnly disables the local-only commands for a remote server, and
// enables them back for this one (the pages narrow them again on their
// next update).
func (m *manager) applyLocalOnly() {
	for c := range localOnly {
		c.setEnabled(!viewingRemote)
	}
}

// ---- the connections tree's servers

// addSavedRoots lists the saved connections after this server's node.
func (m *manager) addSavedRoots() {
	list, err := remote.LoadSaved()
	if err != nil {
		m.flash = "Could not read the saved server connections: " + err.Error()
		m.flashBad, m.flashAt = true, time.Now()
		return
	}
	for i := range list {
		m.nav.roots = append(m.nav.roots, remoteRoot(&list[i]))
	}
}

func remoteRoot(s *remote.Saved) *navItem {
	return &navItem{kind: navServer, text: s.Name, conn: s, image: ico(desktop.IconGlobe)}
}

// rootOf returns the server node an item belongs to.
func rootOf(it *navItem) *navItem {
	for it.parent != nil {
		it = it.parent
	}
	return it
}

// activateRoot makes a server's node the one the pages work on: its
// client, its pages under it (moved from the previous server's node), and
// fresh state. It reports whether the server could be selected.
func (m *manager) activateRoot(root *navItem) bool {
	if root == m.nav.root {
		return true
	}
	cl := m.pipe
	if root.conn != nil {
		if root.conn.Token == "" {
			notify(m.mw, "NodeHoster Manager", "The token of "+root.conn.Name+" cannot be read",
				"It was saved by another Windows account or on another computer. Remove the connection and connect again.", "", walk.TaskDialogSystemIconError)
			return false
		}
		var err error
		if cl, err = localapi.ConnectRemote(root.conn.URL, root.conn.Token, root.conn.Fingerprint); err != nil {
			m.errorBox("Connecting to "+root.conn.Name, err)
			return false
		}
	}
	if m.cur != nil && m.cur.hide != nil {
		m.cur.hide() // stops a log stream of the previous server
	}
	old := m.nav.root
	m.rebuilding = true
	root.children, old.children = old.children, nil
	for _, c := range root.children {
		c.parent = root
	}
	m.nav.root = root
	m.nav.sites.children = nil
	m.sw.set(cl, root.conn)
	viewingRemote = root.conn != nil
	m.applyLocalOnly()
	m.sites, m.info, m.account, m.connErr, m.service = nil, nil, "", nil, ""
	m.site, m.pendingSite, m.resumeSite = "", "", ""
	m.nav.PublishItemsReset(nil)
	m.tree.SetExpanded(root, true)
	m.rebuilding = false

	host, _ := os.Hostname()
	title := "NodeHoster Manager — " + host
	if root.conn != nil {
		title = "NodeHoster Manager — " + root.conn.Name + " (" + root.conn.URL + ")"
	}
	m.mw.SetTitle(title)
	m.updateStatusBar()
	m.updateBanner()
	m.updateCommands()
	m.refresh(true)
	return true
}

// ---- connecting

// connectDialog asks for a server's web console URL and an API token,
// shows its certificate when it is not trusted (to pin after comparing its
// fingerprint with the server's), checks the token, saves the connection
// for this Windows account and shows the server.
func connectDialog(m *manager) {
	list, err := remote.LoadSaved()
	if err != nil {
		m.errorBox("Reading the saved connections", err)
		return
	}
	var name, addr, token *walk.LineEdit
	var s remote.Saved
	ok := runDialog(m.mw, "Connect to a server", Size{Width: 520}, []Widget{
		intro(desktop.IconGlobe, "Manage another NodeHoster server through its web console. On that server, create an API token for this (Account → API tokens), limited to what you need to do there."),
		Composite{Layout: Grid{Columns: 2, MarginsZero: true, Spacing: 8}, Children: []Widget{
			Label{Text: "Web console URL:"},
			LineEdit{AssignTo: &addr, CueBanner: "https://web02:8484"},
			Label{Text: "API token:"},
			LineEdit{AssignTo: &token, PasswordMode: true, CueBanner: "nh_…"},
			Label{Text: "Name:"},
			LineEdit{AssignTo: &name, CueBanner: "Optional: the host name"},
		}},
		hint("The token is saved for your Windows account only, protected by Windows (DPAPI). The command line uses the same connections: nodehoster --server <name>."),
	}, func(dlg *walk.Dialog) bool {
		s = remote.Saved{Name: name.Text(), URL: addr.Text(), Token: token.Text()}
		if err := s.Validate(list); err != nil {
			return invalid(dlg, err.Error())
		}
		return true
	})
	if !ok {
		return
	}
	done := m.begin("Connecting to " + s.URL)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*remote.CheckTimeout)
		cert, err := remote.Probe(ctx, s.URL)
		cancel()
		m.mw.Synchronize(func() {
			done()
			if err != nil {
				m.errorBox("Connecting to "+s.URL, err)
				return
			}
			if cert != nil && !cert.Verified && !trustCertificate(m, s.URL, cert) {
				return
			}
			if cert != nil && !cert.Verified {
				s.Fingerprint = cert.Fingerprint
			}
			finishConnect(m, s)
		})
	}()
}

// trustCertificate shows a certificate that is not trusted, for the user
// to compare its fingerprint with the server's before pinning it.
func trustCertificate(m *manager, url string, c *model.PeerCertificate) bool {
	content := fmt.Sprintf("Subject: %s\nIssuer: %s\nValid until: %s\n\nSHA-256 fingerprint:\n%s\n\nCompare it with the fingerprint of that server's web console certificate (its Certificates page shows it). Trust it only if they are the same: otherwise something between the servers may be listening in.",
		c.Subject, c.Issuer, c.NotAfter.Local().Format("2006-01-02"), model.FormatFingerprint(c.Fingerprint))
	return ask(m.mw, "Connect to a server", "The certificate of "+url+" is not trusted", content,
		walk.TaskDialogSystemIconWarning,
		[2]string{"Trust this certificate", "Pin it: this connection accepts this certificate only."}) == 0
}

// finishConnect checks the token and saves the connection.
func finishConnect(m *manager, s remote.Saved) {
	done := m.begin("Checking the token on " + s.URL)
	go func() {
		var h model.ServerHealth
		tr, err := remote.NewTransport(s.URL, s.Fingerprint)
		if err == nil {
			h = remote.Check(context.Background(), remote.NewClient(tr), s.URL, s.Token)
			tr.CloseIdleConnections()
		}
		var list []remote.Saved
		if err == nil && h.Reachable {
			// Read again: another window or the command line may have
			// saved one meanwhile.
			if list, err = remote.LoadSaved(); err == nil {
				if err = s.Validate(list); err == nil {
					err = remote.StoreSaved(append(list, s))
				}
			}
		}
		m.mw.Synchronize(func() {
			done()
			switch {
			case err != nil:
				m.errorBox("Connecting to "+s.URL, err)
			case !h.Reachable:
				notify(m.mw, "NodeHoster Manager", "Cannot use "+s.URL, h.Error, "", walk.TaskDialogSystemIconError)
			default:
				m.flashStatus(fmt.Sprintf("Connected to %s: NodeHoster %s, as %s", s.Name, h.Version, h.User), false)
				root := remoteRoot(&s)
				m.nav.roots = append(m.nav.roots, root)
				m.nav.PublishItemsReset(nil)
				m.tree.SetExpanded(m.nav.root, true)
				m.tree.SetCurrentItem(root) // selects it: navigate activates it
			}
		})
	}()
}

// disconnect removes the connection of the server shown and goes back to
// this server.
func (m *manager) disconnect() {
	conn := m.remoteConn()
	if conn == nil {
		return
	}
	if ask(m.mw, "Remove connection", "Remove the connection to "+conn.Name+"?",
		"NodeHoster Manager and the command line forget it and its token. Nothing changes on that server: revoke the token there if nothing else uses it.",
		walk.TaskDialogSystemIconWarning, [2]string{"Remove the connection", ""}) != 0 {
		return
	}
	list, err := remote.LoadSaved()
	if err == nil {
		list = slices.DeleteFunc(list, func(s remote.Saved) bool { return strings.EqualFold(s.Name, conn.Name) })
		err = remote.StoreSaved(list)
	}
	if err != nil {
		m.errorBox("Removing the connection", err)
		return
	}
	gone := m.nav.root
	m.activateRoot(m.nav.roots[0])
	m.nav.roots = slices.DeleteFunc(m.nav.roots, func(r *navItem) bool { return r == gone })
	m.nav.PublishItemsReset(nil)
	m.tree.SetExpanded(m.nav.root, true)
	m.tree.SetCurrentItem(m.nav.root)
}

// ---- how a remote server shows

// whoami names who the manager acts as: the Windows account on this
// server's pipe, the token's user (and role) on a remote server.
func (m *manager) whoami(ctx context.Context, cl *localapi.Client) string {
	if !cl.Remote() {
		var who struct{ Account string }
		if cl.Get(ctx, "/api/local/whoami", &who) == nil {
			return who.Account
		}
		return ""
	}
	var me struct {
		User   model.User `json:"user"`
		Access struct {
			Role model.Role `json:"role"`
		} `json:"access"`
	}
	if cl.Get(ctx, "/api/auth/me", &me) != nil {
		return ""
	}
	role := string(me.Access.Role)
	if me.Access.Role == model.RoleSites {
		role = "some sites"
	}
	return me.User.Username + " (" + role + ")"
}

// updateRemoteStatus is updateStatusBar for a remote server.
func (m *manager) updateRemoteStatus(conn *remote.Saved) {
	if m.connErr == nil && m.service == "running" {
		m.sbService.SetIcon(dotIcon(desktop.LevelOK))
		m.sbService.SetText("Server: " + conn.Name)
		who := m.account
		if who == "" {
			who = "the token's user"
		}
		m.sbConn.SetIcon(ico(desktop.IconHTTPS))
		m.sbConn.SetText("Connected to " + conn.URL + " over HTTPS as " + who)
	} else {
		m.sbService.SetIcon(dotIcon(desktop.LevelDown))
		m.sbService.SetText("Server: " + conn.Name)
		m.sbConn.SetIcon(ico(desktop.IconError))
		if m.connErr != nil {
			m.sbConn.SetText("Not connected: " + m.connErr.Error())
		} else {
			m.sbConn.SetText("Connecting to " + conn.URL + "…")
		}
	}
	m.updateActivity()
}

// updateRemoteBanner is updateBanner for a remote server: why it cannot
// be managed, or what cannot be done from here.
func (m *manager) updateRemoteBanner(conn *remote.Saved) {
	switch {
	case m.connErr != nil:
		m.banner.show(barError, "Cannot connect to "+conn.Name+": "+m.connErr.Error(), "Retry", func() { m.refresh(true) })
	case m.info != nil:
		m.banner.show(barInfo, fmt.Sprintf("Managing %s (NodeHoster %s) over HTTPS with an API token: its role there applies. Service control, the data folder and log files are not available for remote servers.", conn.Name, m.info.Version), "", nil)
	default:
		m.banner.hide()
	}
}
