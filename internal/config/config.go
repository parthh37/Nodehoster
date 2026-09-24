// Package config holds the bootstrap configuration: the few settings needed
// before the database is open (where the data lives, where the admin console
// listens). Everything else is a model.Settings stored in the database.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Build information, stamped by the linker (-X).
var (
	Version = "dev"
	Commit  = "none"
)

type AdminConfig struct {
	Listen        string `json:"listen"`                  // "0.0.0.0:8484"
	TLS           string `json:"tls"`                     // selfsigned | certificate | none
	CertificateID string `json:"certificateId,omitempty"` // when TLS == certificate
}

type Bootstrap struct {
	Admin    AdminConfig `json:"admin"`
	LogLevel string      `json:"logLevel"` // debug | info | warn | error
}

// Paths is the on-disk layout under the data directory.
type Paths struct {
	Data     string // root, C:\ProgramData\NodeHoster
	DB       string // nodehoster.db
	Config   string // nodehoster.json
	Logs     string // server logs
	SiteLogs string // logs/sites/<id>/
	Certs    string // certs/<id>/{cert.pem,key.pem}
	ACME     string // acme accounts
	Node     string // node runtimes: node/<version>/
	Sites    string // sites/<id>/{releases,shared}
	Mail     string // mail/{queue,failed,pickup}: the SMTP server's spool
	Run      string // run: the agent script hosted applications load
	Tmp      string
}

// DefaultDataDir is %ProgramData%\NodeHoster on Windows; elsewhere it is
// /var/lib/nodehoster when running as root and ~/.nodehoster otherwise, so the
// server can be developed and tested on any OS.
func DefaultDataDir() string {
	if v := os.Getenv("NODEHOSTER_DATA"); v != "" {
		return v
	}
	if runtime.GOOS == "windows" {
		pd := os.Getenv("ProgramData")
		if pd == "" {
			pd = `C:\ProgramData`
		}
		return filepath.Join(pd, "NodeHoster")
	}
	if os.Geteuid() == 0 {
		return "/var/lib/nodehoster"
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".nodehoster")
}

func NewPaths(data string) Paths {
	abs, err := filepath.Abs(data)
	if err == nil {
		data = abs
	}
	return Paths{
		Data:     data,
		DB:       filepath.Join(data, "nodehoster.db"),
		Config:   filepath.Join(data, "nodehoster.json"),
		Logs:     filepath.Join(data, "logs"),
		SiteLogs: filepath.Join(data, "logs", "sites"),
		Certs:    filepath.Join(data, "certs"),
		ACME:     filepath.Join(data, "acme"),
		Node:     filepath.Join(data, "node"),
		Sites:    filepath.Join(data, "sites"),
		Mail:     filepath.Join(data, "mail"),
		Run:      filepath.Join(data, "run"),
		Tmp:      filepath.Join(data, "tmp"),
	}
}

// Ensure creates the layout and restricts who may use it; see secure.
func (p Paths) Ensure() error {
	for _, d := range []string{p.Data, p.Logs, p.SiteLogs, p.Certs, p.ACME, p.Node, p.Sites, p.Mail, p.Run, p.Tmp} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	return p.secure()
}

func DefaultBootstrap() Bootstrap {
	return Bootstrap{
		Admin:    AdminConfig{Listen: "0.0.0.0:8484", TLS: "selfsigned"},
		LogLevel: "info",
	}
}

// LoadBootstrap reads nodehoster.json, creating it with defaults if missing.
func LoadBootstrap(path string) (Bootstrap, error) {
	b := DefaultBootstrap()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return b, SaveBootstrap(path, b)
	}
	if err != nil {
		return b, err
	}
	if err := json.Unmarshal(data, &b); err != nil {
		return b, fmt.Errorf("parse %s: %w", path, err)
	}
	if b.Admin.Listen == "" {
		b.Admin.Listen = DefaultBootstrap().Admin.Listen
	}
	if b.Admin.TLS == "" {
		b.Admin.TLS = "selfsigned"
	}
	return b, nil
}

func SaveBootstrap(path string, b Bootstrap) error {
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, data, 0o640)
}

// WriteFileAtomic writes to a temporary file and renames it over the target,
// so a crash never leaves a half-written file behind.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	_ = os.Chmod(tmp, perm)
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
