// Package core wires the subsystems together and owns the lifecycle of
// sites: every configuration change goes through here so that the store,
// the process manager, the proxy and the certificate manager stay in step.
package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/parthh37/nodehoster/internal/auth"
	"github.com/parthh37/nodehoster/internal/certs"
	"github.com/parthh37/nodehoster/internal/config"
	"github.com/parthh37/nodehoster/internal/deploy"
	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/ipban"
	"github.com/parthh37/nodehoster/internal/logship"
	"github.com/parthh37/nodehoster/internal/mail"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/nodeversions"
	"github.com/parthh37/nodehoster/internal/procmgr"
	"github.com/parthh37/nodehoster/internal/proxy"
	"github.com/parthh37/nodehoster/internal/rewrite"
	"github.com/parthh37/nodehoster/internal/secrets"
	"github.com/parthh37/nodehoster/internal/store"
	"github.com/parthh37/nodehoster/internal/tasks"
	"github.com/parthh37/nodehoster/internal/update"
	"golang.org/x/crypto/bcrypt"
)

const settingsKey = "settings"

type Core struct {
	Paths     config.Paths
	Boot      config.Bootstrap
	Log       *slog.Logger
	Store     *store.Store
	Box       *secrets.Box
	Bus       *events.Bus
	Auth      *auth.Service
	Procs     *procmgr.Manager
	Proxy     *proxy.Server
	Certs     *certs.Manager
	Nodes     *nodeversions.Manager
	Deploy    *deploy.Deployer
	Mail      *mail.Server
	Bans      *ipban.Manager
	Tasks     *tasks.Scheduler
	Ship      *logship.Shipper
	StartedAt time.Time
	IsService bool

	// Set by main before Start: where the web console listens, or why it
	// could not.
	AdminURL   string
	AdminError string

	settingsMu sync.RWMutex
	settings   model.Settings

	// sitesMu serializes configuration changes; reads use the cache.
	sitesMu sync.Mutex
	cacheMu sync.RWMutex
	sites   map[string]*model.Site
	running map[string]bool // desired state of non-node sites

	backups backupState
	updates updateState
	// UpdateFeed is where new releases come from (tests replace it).
	UpdateFeed *update.Feed

	ctx    context.Context // ends at Shutdown
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// Open initializes every subsystem but does not start serving.
func Open(paths config.Paths, boot config.Bootstrap, log *slog.Logger) (*Core, error) {
	if err := paths.Ensure(); err != nil {
		return nil, err
	}
	st, err := store.Open(paths.DB)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	box, err := secrets.Open(filepath.Join(paths.Data, "master.key"))
	if err != nil {
		st.Close()
		return nil, err
	}
	c := &Core{
		Paths: paths, Boot: boot, Log: log, Store: st, Box: box, StartedAt: time.Now(),
		sites: map[string]*model.Site{}, running: map[string]bool{},
	}
	ctx := context.Background()
	c.settings = model.DefaultSettings()
	if err := st.GetDoc(ctx, settingsKey, &c.settings); err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	c.settings.Mail.ApplyDefaults() // settings saved before the SMTP server existed
	if c.settings.Mime.UnknownTypes == "" {
		c.settings.Mime.UnknownTypes = model.UnknownMimeServe
	}
	c.settings.IPBan.ApplyDefaults() // settings saved before IP banning existed
	c.settings.Updates.ApplyDefaults()
	c.UpdateFeed = update.NewFeed(update.DefaultFeed)

	c.Ship = newShipper(log, c.siteName)
	c.Bus = events.New(st, log, c.Settings, c.siteName)
	if err := c.openBans(ctx); err != nil {
		return nil, fmt.Errorf("load IP bans: %w", err)
	}
	c.Bus.OnEmit = func(e model.Event) {
		if c.Ship.Wants(model.LogSourceEvent) {
			c.Ship.Ship(eventRecord(e))
		}
	}
	c.Auth = auth.New(st, box)
	c.Nodes = nodeversions.New(paths.Node, paths.Tmp, log, func() string { return "" })
	c.Certs = certs.New(st, box, paths.Certs, paths.ACME, log, c.Bus, c.Settings)
	if err := c.Certs.Load(ctx); err != nil {
		return nil, fmt.Errorf("load certificates: %w", err)
	}
	c.Procs, err = procmgr.New(procmgr.Options{
		Log: log, Bus: c.Bus,
		SitesDir: paths.Sites, LogsDir: paths.SiteLogs, RunDir: paths.Run,
		Settings: c.Settings, ResolveNode: c.Nodes.Resolve, Unseal: box.MustUnseal,
		IsLocationTarget: c.isLocationTarget,
		OnLog: func(siteID string, l model.LogLine) {
			if c.Ship.Wants(model.LogSourceApp) {
				c.Ship.Ship(appRecord(siteID, l))
			}
		},
	})
	if err != nil {
		return nil, err
	}
	affKey, err := c.affinityKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("session affinity key: %w", err)
	}
	c.Proxy = proxy.New(proxy.Deps{
		Log: log, Bus: c.Bus, Procs: c.Procs, Certs: c.Certs, Settings: c.Settings,
		SitesDir: paths.Sites, LogsDir: paths.SiteLogs, AffinityKey: affKey, Bans: c.Bans, Ship: c.Ship,
	})
	c.Mail, err = mail.New(mail.Options{
		Dir: paths.Mail, LogFile: filepath.Join(paths.Logs, "smtp.log"), Log: log, Bus: c.Bus,
		Settings: func() model.MailSettings { return c.Settings().Mail },
		Unseal:   box.MustUnseal, Cert: c.Certs.Get,
	})
	if err != nil {
		return nil, err
	}
	c.Tasks = tasks.New(tasks.Options{
		Store: st, Bus: c.Bus, Log: log, LogsDir: paths.SiteLogs, Runner: taskRunner{c.Procs},
	})
	c.Deploy = deploy.New(deploy.Options{
		Store: st, Box: box, Log: log, Bus: c.Bus, SitesDir: paths.Sites, Settings: c.Settings,
		ResolveNode: c.Nodes.Resolve, Activate: c.activateRelease, InUse: c.Tasks.Releases,
	})

	sites, err := st.ListSites(ctx)
	if err != nil {
		return nil, err
	}
	for _, s := range sites {
		s.ApplyDefaults()
		c.sites[s.ID] = s
		c.Procs.Apply(s)
		c.Tasks.Apply(s)
	}
	c.applyLogShipping(c.settings.LogShipping)
	c.attachShipper()
	return c, nil
}

// Start opens listeners, starts auto-start sites and background jobs.
func (c *Core) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	c.ctx, c.cancel = ctx, cancel
	c.cacheMu.RLock()
	var auto []*model.Site
	for _, s := range c.sites {
		if s.AutoStart {
			auto = append(auto, s)
		}
	}
	c.cacheMu.RUnlock()
	for _, s := range auto {
		if s.RunsNode() {
			if err := c.Procs.Start(s.ID); err != nil {
				c.Bus.Error(events.SiteFailed, s.ID, "%s could not start: %v", s.Name, err)
			}
		} else {
			c.setRunning(s.ID, true)
		}
	}
	c.reload()
	c.Mail.Start()
	c.Tasks.Start()
	c.Bus.Info(events.ServerStarted, "", "NodeHoster %s started", config.Version)
	c.reportUpdate()

	c.wg.Go(func() { c.Certs.Run(ctx) })
	c.wg.Go(func() { c.metricsLoop(ctx) })
	c.wg.Go(func() { c.housekeeping(ctx) })
	c.wg.Go(func() { c.invalidateCaches(ctx) })
	c.wg.Go(func() { c.backupLoop(ctx) })
	c.wg.Go(func() { c.updateLoop(ctx) })
}

// Shutdown stops listeners, then processes, then closes the database.
func (c *Core) Shutdown() {
	c.backups.close() // before Wait: a manual backup adds itself to c.wg
	if c.cancel != nil {
		c.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c.Proxy.Shutdown(ctx)
	c.Mail.Shutdown(ctx)
	c.Tasks.Shutdown() // before the processes: runs are stopped through the agent
	c.Procs.Shutdown()
	c.wg.Wait()
	if err := c.Bans.Flush(); err != nil {
		c.Log.Error("save IP bans", "err", err)
	}
	c.Ship.Close()
	c.Store.Close()
}

// ---- settings

func (c *Core) Settings() model.Settings {
	c.settingsMu.RLock()
	defer c.settingsMu.RUnlock()
	return c.settings
}

// UpdateSettings merges masked secrets, validates and applies settings.
func (c *Core) UpdateSettings(ctx context.Context, in model.Settings) (model.Settings, error) {
	cur := c.Settings()
	if in.ACME.EABHMAC == secrets.Mask {
		in.ACME.EABHMAC = cur.ACME.EABHMAC
	} else if sealed, err := c.Box.Seal(in.ACME.EABHMAC); err == nil {
		in.ACME.EABHMAC = sealed
	}
	for i := range in.DNSProviders {
		p := &in.DNSProviders[i]
		if p.ID == "" {
			p.ID = uuid.NewString()
		}
		var old *model.DNSProvider
		for j := range cur.DNSProviders {
			if cur.DNSProviders[j].ID == p.ID {
				old = &cur.DNSProviders[j]
			}
		}
		for k, v := range p.Credentials {
			if v == secrets.Mask {
				if old != nil {
					p.Credentials[k] = old.Credentials[k]
				} else {
					p.Credentials[k] = "" // never store the mask itself
				}
			} else if sealed, err := c.Box.Seal(v); err == nil {
				p.Credentials[k] = sealed
			}
		}
	}
	for i := range in.Webhooks {
		if in.Webhooks[i].ID == "" {
			in.Webhooks[i].ID = uuid.NewString()
		}
		if !strings.HasPrefix(in.Webhooks[i].URL, "https://") && !strings.HasPrefix(in.Webhooks[i].URL, "http://") {
			return cur, &model.ValidationError{Field: fmt.Sprintf("webhooks[%d].url", i), Message: "must be an http(s) URL"}
		}
	}
	if in.PortRangeStart < 1024 || in.PortRangeEnd > 65535 || in.PortRangeEnd-in.PortRangeStart < 16 {
		return cur, &model.ValidationError{Field: "portRangeStart", Message: "use a range of at least 16 ports between 1024 and 65535"}
	}
	if in.ACME.Email != "" && !strings.Contains(in.ACME.Email, "@") {
		return cur, &model.ValidationError{Field: "acme.email", Message: "not an email address"}
	}
	if in.TLS.MinVersion != "1.2" && in.TLS.MinVersion != "1.3" {
		in.TLS.MinVersion = "1.2"
	}
	if in.LogMaxSizeMB <= 0 {
		in.LogMaxSizeMB = 20
	}
	if in.LogMaxFiles <= 0 {
		in.LogMaxFiles = 10
	}
	if err := in.Mime.Validate(); err != nil {
		return cur, err
	}
	in.IPBan.ApplyDefaults()
	if err := in.IPBan.Validate(); err != nil {
		return cur, err
	}
	if err := c.prepareMail(&in.Mail, cur.Mail); err != nil {
		return cur, err
	}
	if err := c.prepareSSO(&in.SSO, cur.SSO); err != nil {
		return cur, err
	}
	if err := c.prepareBackup(&in.Backup, cur.Backup); err != nil {
		return cur, err
	}
	if err := c.prepareLogShipping(&in.LogShipping, cur.LogShipping); err != nil {
		return cur, err
	}
	in.Updates.ApplyDefaults()
	if err := in.Updates.Validate(); err != nil {
		return cur, err
	}
	if err := c.Store.PutDoc(ctx, settingsKey, in); err != nil {
		return cur, err
	}
	c.settingsMu.Lock()
	c.settings = in
	c.settingsMu.Unlock()
	c.Procs.SetPortRange(in.PortRangeStart, in.PortRangeEnd)
	c.applyBans()
	c.reload()
	c.Mail.Apply(in.Mail)
	c.applyLogShipping(in.LogShipping)
	if in.ACME != cur.ACME && in.ACME.AgreeTOS && in.ACME.Email != "" {
		go c.Certs.RetryPending(context.Background())
	}
	return in, nil
}

// MaskedSettings is Settings with secrets replaced for the API.
func (c *Core) MaskedSettings() model.Settings {
	s := c.Settings()
	if s.ACME.EABHMAC != "" {
		s.ACME.EABHMAC = secrets.Mask
	}
	provs := make([]model.DNSProvider, len(s.DNSProviders))
	for i, p := range s.DNSProviders {
		creds := map[string]string{}
		for k, v := range p.Credentials {
			if v != "" {
				creds[k] = secrets.Mask
			} else {
				creds[k] = ""
			}
		}
		p.Credentials = creds
		provs[i] = p
	}
	s.DNSProviders = provs
	if s.DNSProviders == nil {
		s.DNSProviders = []model.DNSProvider{}
	}
	if s.Webhooks == nil {
		s.Webhooks = []model.WebhookTarget{}
	}
	if s.Mime.Types == nil {
		s.Mime.Types = []model.MimeMap{}
	}
	m := &s.Mail
	if m.SmartHost.Password != "" {
		m.SmartHost.Password = secrets.Mask
	}
	m.Users = slices.Clone(m.Users)
	for i := range m.Users {
		if m.Users[i].PasswordHash != "" {
			m.Users[i].PasswordHash = secrets.Mask
		}
		m.Users[i].Password = ""
	}
	m.DKIM = slices.Clone(m.DKIM)
	for i := range m.DKIM {
		if m.DKIM[i].PrivateKey != "" {
			m.DKIM[i].PrivateKey = secrets.Mask
		}
	}
	maskSSO(&s.SSO)
	maskBackup(&s.Backup)
	maskLogShipping(&s.LogShipping)
	return s
}

// prepareMail validates the SMTP server settings and seals their
// secrets, keeping masked ones from cur: the smart host password, user
// passwords (hashed with bcrypt, matched by user name) and DKIM keys (a
// new key without one gets a generated key; matched by domain and
// selector).
func (c *Core) prepareMail(in *model.MailSettings, cur model.MailSettings) error {
	in.ApplyDefaults()
	h := &in.SmartHost
	switch h.Password {
	case secrets.Mask:
		h.Password = cur.SmartHost.Password
	case "":
	default:
		sealed, err := c.Box.Seal(h.Password)
		if err != nil {
			return err
		}
		h.Password = sealed
	}
	if h.Username == "" {
		h.Password = ""
	}
	for i := range in.Users {
		u := &in.Users[i]
		u.Username = strings.TrimSpace(u.Username)
		if u.Password != "" {
			hash, err := bcrypt.GenerateFromPassword([]byte(u.Password), bcrypt.DefaultCost)
			if err != nil {
				return err
			}
			u.PasswordHash, u.Password = string(hash), ""
			continue
		}
		// Never trust a hash sent by a client: keep the stored one.
		u.PasswordHash = ""
		for _, o := range cur.Users {
			if strings.EqualFold(o.Username, u.Username) {
				u.PasswordHash = o.PasswordHash
			}
		}
		if u.PasswordHash == "" {
			return &model.ValidationError{Field: fmt.Sprintf("mail.users[%d].password", i), Message: "set a password"}
		}
	}
	if err := in.Validate(); err != nil {
		return err
	}
	for i := range in.DKIM {
		d := &in.DKIM[i]
		f := fmt.Sprintf("mail.dkim[%d]", i)
		var pemKey string
		switch d.PrivateKey {
		case secrets.Mask:
			d.PrivateKey = ""
			for _, o := range cur.DKIM {
				if o.Domain == d.Domain && o.Selector == d.Selector {
					d.PrivateKey = o.PrivateKey
				}
			}
			if d.PrivateKey == "" {
				return &model.ValidationError{Field: f + ".privateKey", Message: "the key was not found; remove this entry and add it again"}
			}
			pemKey = c.Box.MustUnseal(d.PrivateKey)
		case "":
			k, err := mail.GenerateDKIMKey()
			if err != nil {
				return err
			}
			pemKey = k
		default:
			if _, err := mail.ParseDKIMKey(d.PrivateKey); err != nil {
				return &model.ValidationError{Field: f + ".privateKey", Message: err.Error()}
			}
			pemKey = d.PrivateKey
		}
		rec, err := mail.DKIMRecord(pemKey)
		if err != nil {
			return &model.ValidationError{Field: f + ".privateKey", Message: err.Error()}
		}
		d.DNSName, d.DNSRecord = mail.DKIMName(d.Selector, d.Domain), rec
		if d.PrivateKey == "" || d.PrivateKey == pemKey {
			if d.PrivateKey, err = c.Box.Seal(pemKey); err != nil {
				return err
			}
		}
	}
	if id := in.CertificateID; id != "" && c.Certs.Get(id) == nil {
		if _, err := c.Store.GetCertificate(context.Background(), id); err != nil {
			return &model.ValidationError{Field: "mail.certificateId", Message: "the selected certificate does not exist"}
		}
	}
	if in.Enabled {
		for _, s := range c.Sites() {
			for _, b := range s.Bindings {
				if b.Port == in.Port && (b.IP == "" || in.ListenIP == "" || b.IP == in.ListenIP) {
					return &model.ValidationError{Field: "mail.port", Message: fmt.Sprintf("port %d is used by site %q", b.Port, s.Name)}
				}
			}
		}
	}
	return nil
}

// ---- sites

func (c *Core) siteName(id string) string {
	c.cacheMu.RLock()
	defer c.cacheMu.RUnlock()
	if s, ok := c.sites[id]; ok {
		return s.Name
	}
	return id
}

func (c *Core) isLocationTarget(id string) bool {
	c.cacheMu.RLock()
	defer c.cacheMu.RUnlock()
	for _, s := range c.sites {
		for _, l := range s.Routing.Locations {
			if l.Kind == "site" && l.SiteID == id {
				return true
			}
		}
	}
	return false
}

// Sites returns the cached sites ordered by name.
func (c *Core) Sites() []*model.Site {
	c.cacheMu.RLock()
	out := make([]*model.Site, 0, len(c.sites))
	for _, s := range c.sites {
		out = append(out, s)
	}
	c.cacheMu.RUnlock()
	slices.SortFunc(out, func(a, b *model.Site) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out
}

func (c *Core) Site(id string) (*model.Site, error) {
	c.cacheMu.RLock()
	defer c.cacheMu.RUnlock()
	s, ok := c.sites[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return s, nil
}

func (c *Core) setRunning(id string, v bool) {
	c.cacheMu.Lock()
	c.running[id] = v
	c.cacheMu.Unlock()
}

// IsRunning is the site's desired running state.
func (c *Core) IsRunning(s *model.Site) bool {
	if s.RunsNode() {
		return c.Procs.Running(s.ID)
	}
	c.cacheMu.RLock()
	defer c.cacheMu.RUnlock()
	return c.running[s.ID]
}

// reload pushes the current configuration to the proxy and requests
// automatic certificates for running https bindings.
func (c *Core) reload() {
	sites := c.Sites()
	// Workers serve no HTTP: the proxy never sees them (and so they cannot
	// be the target of a location either).
	routed := make([]*model.Site, 0, len(sites))
	for _, s := range sites {
		if s.Type != model.SiteWorker {
			routed = append(routed, s)
		}
	}
	// A stopped node site still gets its bindings routed, so visitors see a
	// "not running" page instead of the default page.
	c.Proxy.Reload(routed, func(s *model.Site) bool {
		return s.Type == model.SiteNode || c.IsRunning(s)
	})
	var hosts []string
	for _, s := range sites {
		if !c.IsRunning(s) {
			continue
		}
		for _, b := range s.Bindings {
			if b.Protocol == "https" && b.CertMode == model.CertModeAuto && b.Host != "" && !slices.Contains(hosts, b.Host) {
				hosts = append(hosts, b.Host)
			}
		}
	}
	if len(hosts) > 0 {
		go c.Certs.EnsureManaged(context.Background(), hosts)
	}
}

func clone(s *model.Site) *model.Site {
	b, _ := json.Marshal(s)
	var out model.Site
	json.Unmarshal(b, &out)
	return &out
}

// prepare validates an incoming site and seals its secrets, merging masked
// values from the existing version.
func (c *Core) prepare(in *model.Site, existing *model.Site) error {
	in.Name = strings.TrimSpace(in.Name)
	for i := range in.Bindings {
		if in.Bindings[i].ID == "" {
			in.Bindings[i].ID = uuid.NewString()
		}
	}
	for i := range in.Tasks {
		if in.Tasks[i].ID == "" {
			in.Tasks[i].ID = uuid.NewString()
		}
	}
	in.ApplyDefaults()
	if err := in.Validate(); err != nil {
		return err
	}
	if err := rewrite.Validate(in.Routing); err != nil {
		return err
	}
	if m := c.Settings().Mail; m.Enabled {
		for i, b := range in.Bindings {
			if b.Port == m.Port && (b.IP == "" || m.ListenIP == "" || b.IP == m.ListenIP) {
				return &model.ValidationError{Field: fmt.Sprintf("bindings[%d].port", i), Message: fmt.Sprintf("port %d is used by the SMTP server", b.Port)}
			}
		}
	}
	var others []*model.Site
	for _, s := range c.Sites() {
		others = append(others, s)
	}
	if err := model.ValidateBindings(in, others); err != nil {
		return err
	}
	for i, l := range in.Routing.Locations {
		if l.Kind != "site" {
			continue
		}
		if t, err := c.Site(l.SiteID); err == nil && t.Type == model.SiteWorker {
			return &model.ValidationError{Field: fmt.Sprintf("routing.locations[%d].siteId", i), Message: fmt.Sprintf("%q is a background worker; it serves no HTTP to mount", t.Name)}
		}
	}
	for _, b := range in.Bindings {
		if b.Protocol == "https" && b.CertMode == model.CertModeManual {
			if c.Certs.Get(b.CertificateID) == nil {
				if _, err := c.Store.GetCertificate(context.Background(), b.CertificateID); err != nil {
					return &model.ValidationError{Field: "bindings", Message: "the selected certificate does not exist"}
				}
			}
		}
	}

	seal := func(v, old string) string {
		if v == secrets.Mask {
			return old
		}
		s, _ := c.Box.Seal(v)
		return s
	}
	var ex model.Site
	if existing != nil {
		ex = *existing
	}
	if in.Node != nil {
		var oldNode model.NodeConfig
		if ex.Node != nil {
			oldNode = *ex.Node
		}
		for i := range in.Node.Env {
			e := &in.Node.Env[i]
			if !e.Secret {
				if e.Value == secrets.Mask {
					e.Value = ""
				}
				continue
			}
			old := ""
			for _, o := range oldNode.Env {
				if o.Name == e.Name && o.Secret {
					old = o.Value
				}
			}
			e.Value = seal(e.Value, old)
		}
		in.Node.RunAs.Password = seal(in.Node.RunAs.Password, oldNode.RunAs.Password)
	}
	// Task variables: a masked secret keeps the stored value of the same
	// variable of the same task (matched by task id).
	for i := range in.Tasks {
		t := &in.Tasks[i]
		var oldEnv []model.EnvVar
		for _, o := range ex.Tasks {
			if o.ID == t.ID {
				oldEnv = o.Env
			}
		}
		for j := range t.Env {
			e := &t.Env[j]
			if !e.Secret {
				if e.Value == secrets.Mask {
					e.Value = ""
				}
				continue
			}
			old := ""
			for _, o := range oldEnv {
				if o.Name == e.Name && o.Secret {
					old = o.Value
				}
			}
			e.Value = seal(e.Value, old)
		}
	}
	in.Deploy.Git.Token = seal(in.Deploy.Git.Token, ex.Deploy.Git.Token)
	in.Deploy.WebhookSecret = seal(in.Deploy.WebhookSecret, ex.Deploy.WebhookSecret)

	// Basic auth: hash new passwords, keep existing hashes otherwise.
	for i := range in.Routing.BasicAuth.Users {
		u := &in.Routing.BasicAuth.Users[i]
		if u.Password != "" {
			h, err := bcrypt.GenerateFromPassword([]byte(u.Password), bcrypt.DefaultCost)
			if err != nil {
				return err
			}
			u.PasswordHash, u.Password = string(h), ""
			continue
		}
		for _, o := range ex.Routing.BasicAuth.Users {
			if o.Username == u.Username {
				u.PasswordHash = o.PasswordHash
			}
		}
		if u.PasswordHash == "" {
			return &model.ValidationError{Field: fmt.Sprintf("routing.basicAuth.users[%d].password", i), Message: "set a password"}
		}
	}
	return nil
}

// CreateSite validates, stores and applies a new site, starting it when
// it is set to start automatically.
func (c *Core) CreateSite(ctx context.Context, in *model.Site) (*model.Site, error) {
	return c.createSite(ctx, in, in.AutoStart)
}

func (c *Core) createSite(ctx context.Context, in *model.Site, start bool) (*model.Site, error) {
	c.sitesMu.Lock()
	defer c.sitesMu.Unlock()
	in.ID = uuid.NewString()
	in.ActiveRelease = ""
	if err := c.prepare(in, nil); err != nil {
		return nil, err
	}
	now := time.Now()
	in.CreatedAt, in.UpdatedAt = now, now
	if err := c.Store.PutSite(ctx, in); err != nil {
		return nil, err
	}
	c.cacheMu.Lock()
	c.sites[in.ID] = in
	c.cacheMu.Unlock()
	c.Procs.Apply(in)
	c.Tasks.Apply(in)
	if start {
		c.startSite(in)
	}
	c.reload()
	return in, nil
}

// UpdateSite replaces a site's configuration and applies it live.
func (c *Core) UpdateSite(ctx context.Context, id string, in *model.Site) (*model.Site, error) {
	c.sitesMu.Lock()
	defer c.sitesMu.Unlock()
	return c.updateSite(ctx, id, in)
}

// updateSite is UpdateSite with sitesMu held, for a read-modify-write.
func (c *Core) updateSite(ctx context.Context, id string, in *model.Site) (*model.Site, error) {
	existing, err := c.Site(id)
	if err != nil {
		return nil, err
	}
	in.ID = id
	in.CreatedAt = existing.CreatedAt
	in.ActiveRelease = existing.ActiveRelease
	if in.Type != existing.Type {
		return nil, &model.ValidationError{Field: "type", Message: "the site type cannot be changed; create a new site"}
	}
	if err := c.prepare(in, existing); err != nil {
		return nil, err
	}
	in.UpdatedAt = time.Now()
	if err := c.Store.PutSite(ctx, in); err != nil {
		return nil, err
	}
	c.cacheMu.Lock()
	c.sites[id] = in
	c.cacheMu.Unlock()
	c.Procs.Apply(in)
	c.Tasks.Apply(in)
	c.reload()
	return in, nil
}

// activateRelease is called by the deployer to switch a site to a release.
func (c *Core) activateRelease(ctx context.Context, id, release string) error {
	c.sitesMu.Lock()
	defer c.sitesMu.Unlock()
	existing, err := c.Site(id)
	if err != nil {
		return err
	}
	s := clone(existing)
	s.ActiveRelease = release
	s.UpdatedAt = time.Now()
	if err := c.Store.PutSite(ctx, s); err != nil {
		return err
	}
	c.cacheMu.Lock()
	c.sites[id] = s
	c.cacheMu.Unlock()
	c.Procs.Apply(s) // a running node site recycles onto the new release
	c.Tasks.Apply(s) // later task runs use the new release
	if s.RunsNode() && !c.Procs.Running(id) && s.AutoStart {
		c.Procs.Start(id)
	}
	c.reload()
	return nil
}

// DeleteSite stops and removes a site, optionally with its files. The
// site leaves the configuration under the lock; stopping its processes and
// task runs waits for their shutdown timeouts (a minute or more) and
// happens after, so that other sites can be edited meanwhile. Nothing can
// start or change the site in between: it is no longer found. It leaves
// the database once its runs have ended, so none records anything after.
func (c *Core) DeleteSite(ctx context.Context, id string, deleteFiles bool) error {
	c.sitesMu.Lock()
	existing, err := c.Site(id)
	if err != nil {
		c.sitesMu.Unlock()
		return err
	}
	for _, s := range c.Sites() {
		for _, l := range s.Routing.Locations {
			if l.Kind == "site" && l.SiteID == id {
				c.sitesMu.Unlock()
				return fmt.Errorf("site %q mounts this site at %s; remove that location first", s.Name, l.Path)
			}
		}
	}
	c.cacheMu.Lock()
	delete(c.sites, id)
	wasRunning := c.running[id]
	delete(c.running, id)
	c.cacheMu.Unlock()
	c.sitesMu.Unlock()
	c.reload()
	c.Proxy.ForgetSite(id)

	c.Tasks.Remove(id)
	c.Procs.Remove(id)
	c.Procs.ForgetLogs(id)
	// The site is gone from the running configuration already: a client
	// that stopped waiting must not leave it in the database.
	if err := c.Store.DeleteSite(context.WithoutCancel(ctx), id); err != nil {
		// Still stored: put it back (a Node.js site stays stopped).
		c.sitesMu.Lock()
		c.cacheMu.Lock()
		c.sites[id] = existing
		c.running[id] = wasRunning
		c.cacheMu.Unlock()
		c.Procs.Apply(existing)
		c.Tasks.Apply(existing)
		c.sitesMu.Unlock()
		c.reload()
		return err
	}
	if deleteFiles {
		os.RemoveAll(filepath.Join(c.Paths.Sites, id))
		os.RemoveAll(filepath.Join(c.Paths.SiteLogs, id))
	}
	return nil
}

func (c *Core) startSite(s *model.Site) error {
	if s.RunsNode() {
		return c.Procs.Start(s.ID)
	}
	c.setRunning(s.ID, true)
	c.Bus.Info(events.SiteStarted, s.ID, "%s started", s.Name)
	return nil
}

func (c *Core) StartSite(id string) error {
	s, err := c.Site(id)
	if err != nil {
		return err
	}
	if err := c.startSite(s); err != nil {
		return err
	}
	c.reload()
	return nil
}

func (c *Core) StopSite(id string) error {
	s, err := c.Site(id)
	if err != nil {
		return err
	}
	if s.RunsNode() {
		if err := c.Procs.Stop(id); err != nil {
			return err
		}
	} else {
		c.setRunning(id, false)
		c.Bus.Info(events.SiteStopped, id, "%s stopped", s.Name)
	}
	c.reload()
	return nil
}

func (c *Core) RestartSite(id string) error {
	s, err := c.Site(id)
	if err != nil {
		return err
	}
	if s.RunsNode() {
		err = c.Procs.Restart(id)
	} else {
		c.setRunning(id, true)
	}
	c.Proxy.PurgeCache(id, "")
	c.reload()
	return err
}

func (c *Core) RecycleSite(id string) error {
	s, err := c.Site(id)
	if err != nil {
		return err
	}
	if !s.RunsNode() {
		return c.RestartSite(id)
	}
	err = c.Procs.Recycle(id, "requested")
	c.Proxy.PurgeCache(id, "")
	c.reload()
	return err
}

// Status is the live state of a site.
func (c *Core) Status(s *model.Site) model.SiteStatus {
	var st model.SiteStatus
	if s.RunsNode() {
		st, _ = c.Procs.Status(s.ID)
		st.Upstreams = c.Proxy.UpstreamStatus(s.ID) // other servers when load balanced
	} else {
		st = model.SiteStatus{SiteID: s.ID, State: model.StateStopped, Instances: []model.InstanceStatus{}}
		if c.IsRunning(s) {
			st.State = model.StateRunning
		}
		st.Upstreams = c.Proxy.UpstreamStatus(s.ID)
		if s.Type == model.SiteProxy && st.State == model.StateRunning {
			healthy := 0
			for _, u := range st.Upstreams {
				if u.Healthy {
					healthy++
				}
			}
			if healthy < len(st.Upstreams) {
				st.State = model.StateDegraded
			}
		}
	}
	if st.Instances == nil {
		st.Instances = []model.InstanceStatus{}
	}
	st.SiteID = s.ID
	st.Traffic = c.Proxy.Traffic(s.ID)
	st.Cache = c.Proxy.CacheStats(s.ID)
	return st
}

// Masked returns a copy of a site with secrets hidden, for the API.
func Masked(s *model.Site) *model.Site {
	m := clone(s)
	mask := func(v string) string {
		if v == "" {
			return ""
		}
		return secrets.Mask
	}
	if m.Node != nil {
		for i := range m.Node.Env {
			if m.Node.Env[i].Secret {
				m.Node.Env[i].Value = mask(m.Node.Env[i].Value)
			}
		}
		m.Node.RunAs.Password = mask(m.Node.RunAs.Password)
	}
	for i := range m.Tasks {
		for j := range m.Tasks[i].Env {
			if e := &m.Tasks[i].Env[j]; e.Secret {
				e.Value = mask(e.Value)
			}
		}
	}
	m.Deploy.Git.Token = mask(m.Deploy.Git.Token)
	m.Deploy.WebhookSecret = mask(m.Deploy.WebhookSecret)
	for i := range m.Routing.BasicAuth.Users {
		m.Routing.BasicAuth.Users[i].PasswordHash = ""
		m.Routing.BasicAuth.Users[i].Password = ""
	}
	if m.Bindings == nil {
		m.Bindings = []model.Binding{}
	}
	return m
}

// CertificateUsage lists the bindings that use each certificate.
type CertUse struct {
	SiteID   string `json:"siteId"`
	SiteName string `json:"siteName"`
	Binding  string `json:"binding"`
}

func (c *Core) CertificateUsage(cert *model.Certificate) []CertUse {
	out := []CertUse{}
	for _, s := range c.Sites() {
		for _, b := range s.Bindings {
			if b.Protocol != "https" {
				continue
			}
			used := b.CertMode == model.CertModeManual && b.CertificateID == cert.ID
			if cert.Managed && b.CertMode == model.CertModeAuto && len(cert.Domains) == 1 && cert.Domains[0] == b.Host {
				used = true
			}
			if used {
				out = append(out, CertUse{SiteID: s.ID, SiteName: s.Name, Binding: b.String()})
			}
		}
	}
	if m := c.Settings().Mail; m.CertificateID == cert.ID {
		// SiteID is empty: the console links this entry to the SMTP settings.
		out = append(out, CertUse{SiteName: "SMTP server", Binding: fmt.Sprintf("STARTTLS on port %d", m.Port)})
	}
	return out
}

// NodeVersionInUse reports which sites pin a Node.js version.
func (c *Core) NodeVersionInUse(v string) []string {
	var names []string
	for _, s := range c.Sites() {
		if s.RunsNode() && s.Node.NodeVersion == v {
			names = append(names, s.Name)
		}
	}
	if c.Settings().DefaultNodeVersion == v {
		names = append(names, "(server default)")
	}
	return names
}

// ---- background jobs

// metricsLoop stores one metrics point per site (and the server) a minute.
func (c *Core) metricsLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			ts := now.Truncate(time.Minute)
			var tot model.MetricPoint
			var latSum float64
			for _, s := range c.Sites() {
				req, errs, lat := c.Proxy.TakeMinute(s.ID)
				cpu, mem := c.Procs.Usage(s.ID)
				p := model.MetricPoint{Time: ts, Requests: req, Errors: errs, AvgLatency: lat, CPUPercent: cpu, MemoryBytes: mem}
				c.Store.AddMetrics(ctx, s.ID, p)
				tot.Requests += req
				tot.Errors += errs
				latSum += lat * float64(req)
			}
			if tot.Requests > 0 {
				tot.AvgLatency = latSum / float64(tot.Requests)
			}
			tot.Time = ts
			tot.CPUPercent, tot.MemoryBytes = hostUsage()
			c.Store.AddMetrics(ctx, "", tot)
		}
	}
}

// housekeeping prunes old history daily.
func (c *Core) housekeeping(ctx context.Context) {
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		days := c.Settings().LogRetentionDays
		if days <= 0 {
			days = 30
		}
		if err := c.Store.Prune(ctx, time.Duration(days)*24*time.Hour); err != nil {
			c.Log.Warn("prune history", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// taskRunner starts scheduled task runs through the process manager.
type taskRunner struct{ procs *procmgr.Manager }

func (r taskRunner) StartTask(site *model.Site, task model.ScheduledTask, runID string, out io.Writer) (tasks.Process, error) {
	p, err := r.procs.StartTask(site, task, runID, out)
	if err != nil {
		return nil, err // not a typed nil inside the interface
	}
	return p, nil
}
