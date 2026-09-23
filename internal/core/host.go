package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/parthh37/nodehoster/internal/config"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
)

func hostUsage() (float64, uint64) {
	var c float64
	if p, err := cpu.Percent(0, false); err == nil && len(p) > 0 {
		c = p[0]
	}
	var used uint64
	if v, err := mem.VirtualMemory(); err == nil {
		used = v.Used
	}
	return c, used
}

func (c *Core) ServerInfo() model.ServerInfo {
	hostname, _ := os.Hostname()
	info := model.ServerInfo{
		Version: config.Version, Commit: config.Commit, Hostname: hostname,
		OS: runtime.GOOS + "/" + runtime.GOARCH, StartedAt: c.StartedAt,
		CPUCount: runtime.NumCPU(), DataDir: c.Paths.Data, Listeners: c.Proxy.Listeners(),
		GoVersion: runtime.Version(), IsService: c.IsService,
		AdminURL: c.AdminURL, AdminError: c.AdminError,
	}
	if h, err := host.Info(); err == nil {
		info.OS = fmt.Sprintf("%s %s (%s)", h.Platform, h.PlatformVersion, h.KernelArch)
	}
	if p, err := cpu.Percent(200*time.Millisecond, false); err == nil && len(p) > 0 {
		info.CPUPercent = p[0]
	}
	if v, err := mem.VirtualMemory(); err == nil {
		info.MemTotal, info.MemUsed = v.Total, v.Used
	}
	if d, err := disk.Usage(c.Paths.Data); err == nil {
		info.DiskTotal, info.DiskFree = d.Total, d.Free
	}
	if info.Listeners == nil {
		info.Listeners = []string{}
	}
	return info
}

// Backup is the portable configuration export. Secrets stay sealed with
// this server's master key, so a backup restored elsewhere needs secrets
// re-entered; configuration is fully portable.
type Backup struct {
	Version      string               `json:"version"`
	ExportedAt   time.Time            `json:"exportedAt"`
	Hostname     string               `json:"hostname"`
	Settings     model.Settings       `json:"settings"`
	Sites        []*model.Site        `json:"sites"`
	Certificates []*model.Certificate `json:"certificates"`
}

func (c *Core) Backup(ctx context.Context) ([]byte, error) {
	certs, err := c.Store.ListCertificates(ctx)
	if err != nil {
		return nil, err
	}
	hostname, _ := os.Hostname()
	b := Backup{Version: config.Version, ExportedAt: time.Now(), Hostname: hostname, Settings: c.Settings(), Sites: c.Sites(), Certificates: certs}
	return json.MarshalIndent(b, "", "  ")
}

// Restore replaces settings and sites from a backup. Existing sites with
// the same id are overwritten; others are kept.
func (c *Core) Restore(ctx context.Context, data []byte) error {
	_, err := c.restoreConfig(ctx, data, nil)
	return err
}

// restoreConfig is Restore. Certificates in withFiles had their files
// restored and keep their record as backed up; other certificates the
// server does not have become pending (ACME ones are re-issued, others
// must be imported again).
func (c *Core) restoreConfig(ctx context.Context, data []byte, withFiles map[string]bool) (*Backup, error) {
	var b Backup
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("not a NodeHoster backup: %w", err)
	}
	if b.Sites == nil && b.Settings.PortRangeStart == 0 {
		return nil, fmt.Errorf("not a NodeHoster backup")
	}
	b.Settings.Mail.ApplyDefaults() // a backup from before the SMTP server existed
	if b.Settings.Mime.UnknownTypes == "" {
		b.Settings.Mime.UnknownTypes = model.UnknownMimeServe
	}
	b.Settings.IPBan.ApplyDefaults()
	// A backup from before scheduled backups existed keeps this server's
	// backup schedule and destinations, rather than switching them off.
	var probe struct {
		Settings map[string]json.RawMessage `json:"settings"`
	}
	if json.Unmarshal(data, &probe) == nil {
		if _, ok := probe.Settings["backup"]; !ok {
			b.Settings.Backup = c.Settings().Backup
		}
	}
	if err := c.Store.PutDoc(ctx, settingsKey, b.Settings); err != nil {
		return nil, err
	}
	c.settingsMu.Lock()
	c.settings = b.Settings
	c.settingsMu.Unlock()
	c.applyBans()
	c.sitesMu.Lock()
	for _, s := range b.Sites {
		s.ApplyDefaults()
		if err := c.Store.PutSite(ctx, s); err != nil {
			c.sitesMu.Unlock()
			return nil, fmt.Errorf("restore site %s: %w", s.Name, err)
		}
		c.cacheMu.Lock()
		c.sites[s.ID] = s
		c.cacheMu.Unlock()
		c.Procs.Apply(s)
		c.Tasks.Apply(s)
	}
	c.sitesMu.Unlock()
	for _, cert := range b.Certificates {
		if withFiles[cert.ID] {
			c.Store.PutCertificate(ctx, cert)
			continue
		}
		if _, err := c.Store.GetCertificate(ctx, cert.ID); err != nil {
			// Metadata only; ACME certificates are re-issued, others must be re-imported.
			cert.Status = "pending"
			c.Store.PutCertificate(ctx, cert)
		}
	}
	c.reload()
	c.Mail.Apply(c.Settings().Mail)
	return &b, nil
}
