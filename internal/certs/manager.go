// Package certs is the server certificate store: Let's Encrypt (ACME)
// issuance and renewal, imported PFX/PEM certificates and self-signed ones.
package certs

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge/dns01"
	"github.com/google/uuid"
	"github.com/parthh37/nodehoster/internal/config"
	"github.com/parthh37/nodehoster/internal/events"
	"github.com/parthh37/nodehoster/internal/model"
	"github.com/parthh37/nodehoster/internal/secrets"
	"github.com/parthh37/nodehoster/internal/store"
	"software.sslmate.com/src/go-pkcs12"
)

var ErrInUse = errors.New("certificate is in use by a binding")

type Manager struct {
	store    *store.Store
	box      *secrets.Box
	dir      string
	acmeDir  string
	log      *slog.Logger
	bus      *events.Bus
	settings func() model.Settings
	HTTP     *HTTPChallenges

	mu       sync.RWMutex
	cache    map[string]*tls.Certificate // id -> parsed pair
	managed  map[string]string           // host -> id of its managed certificate
	temp     map[string]*tls.Certificate // host -> placeholder while issuing
	issuing  map[string]bool
	failures map[string]int
	warned   map[string]string // id -> date an expiry warning was sent
}

func New(st *store.Store, box *secrets.Box, dir, acmeDir string, log *slog.Logger, bus *events.Bus, settings func() model.Settings) *Manager {
	return &Manager{
		store: st, box: box, dir: dir, acmeDir: acmeDir, log: log, bus: bus, settings: settings,
		HTTP:     newHTTPChallenges(log),
		cache:    map[string]*tls.Certificate{},
		managed:  map[string]string{},
		temp:     map[string]*tls.Certificate{},
		issuing:  map[string]bool{},
		failures: map[string]int{},
		warned:   map[string]string{},
	}
}

func (m *Manager) paths(id string) (certPath, keyPath string) {
	d := filepath.Join(m.dir, id)
	return filepath.Join(d, "cert.pem"), filepath.Join(d, "key.pem")
}

// Load reads every certificate from disk into memory.
func (m *Manager) Load(ctx context.Context) error {
	list, err := m.store.ListCertificates(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range list {
		if c.Managed && len(c.Domains) == 1 {
			m.managed[c.Domains[0]] = c.ID
		}
		cp, kp := m.paths(c.ID)
		pair, err := tls.LoadX509KeyPair(cp, kp)
		if err != nil {
			if c.Status == "valid" {
				m.log.Warn("certificate files unreadable", "cert", c.Name, "err", err)
			}
			continue
		}
		m.cache[c.ID] = &pair
	}
	return nil
}

// Get returns a loaded certificate by id.
func (m *Manager) Get(id string) *tls.Certificate {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cache[id]
}

// ForHost returns the managed (automatic) certificate for a host name. While
// the real certificate is being issued, a self-signed placeholder keeps the
// TLS handshake working instead of failing outright.
func (m *Manager) ForHost(host string) *tls.Certificate {
	m.mu.RLock()
	id, ok := m.managed[host]
	var c *tls.Certificate
	if ok {
		c = m.cache[id]
	}
	t := m.temp[host]
	m.mu.RUnlock()
	if c != nil {
		return c
	}
	if t != nil {
		return t
	}
	pair, err := selfSigned([]string{host}, 7)
	if err != nil {
		return nil
	}
	m.mu.Lock()
	m.temp[host] = pair
	m.mu.Unlock()
	return pair
}

// EnsureManaged makes sure an automatic certificate exists (or is being
// obtained) for each host.
func (m *Manager) EnsureManaged(ctx context.Context, hosts []string) {
	for _, h := range hosts {
		now := time.Now()
		c := &model.Certificate{
			ID: uuid.NewString(), Name: h, Source: model.CertACME, Domains: []string{h},
			ACME:      &model.ACMEOptions{Challenge: "http-01", KeyType: m.settings().ACME.KeyType},
			AutoRenew: true, Managed: true, Status: "pending", CreatedAt: now, UpdatedAt: now,
		}
		// Reserve the host under the write lock so two reloads in quick
		// succession cannot both create a certificate (and an ACME order).
		m.mu.Lock()
		if _, ok := m.managed[h]; ok {
			m.mu.Unlock()
			continue
		}
		m.managed[h] = c.ID
		m.mu.Unlock()
		if err := m.store.PutCertificate(ctx, c); err != nil {
			m.log.Error("create managed certificate", "host", h, "err", err)
			m.mu.Lock()
			delete(m.managed, h)
			m.mu.Unlock()
			continue
		}
		m.log.Info("requesting automatic certificate", "host", h)
		go m.issue(c.ID)
	}
}

// RequestACME creates a certificate record and obtains it in the background.
func (m *Manager) RequestACME(ctx context.Context, name string, domains []string, opts model.ACMEOptions, autoRenew bool) (*model.Certificate, error) {
	domains = normalizeDomains(domains)
	if len(domains) == 0 {
		return nil, errors.New("add at least one domain")
	}
	if opts.Challenge == "" {
		opts.Challenge = "http-01"
	}
	for _, d := range domains {
		if strings.HasPrefix(d, "*.") && opts.Challenge != "dns-01" {
			return nil, errors.New("wildcard certificates require the DNS-01 challenge")
		}
	}
	if opts.Challenge == "dns-01" && opts.DNSProviderID == "" {
		return nil, errors.New("select a DNS provider for the DNS-01 challenge")
	}
	if name == "" {
		name = domains[0]
	}
	now := time.Now()
	c := &model.Certificate{
		ID: uuid.NewString(), Name: name, Source: model.CertACME, Domains: domains, ACME: &opts,
		AutoRenew: autoRenew, Status: "pending", CreatedAt: now, UpdatedAt: now,
	}
	if err := m.store.PutCertificate(ctx, c); err != nil {
		return nil, err
	}
	go m.issue(c.ID)
	return c, nil
}

func normalizeDomains(in []string) []string {
	var out []string
	for _, d := range in {
		d = strings.ToLower(strings.TrimSpace(d))
		if d != "" && !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	return out
}

// Renew obtains a fresh certificate now.
func (m *Manager) Renew(ctx context.Context, id string) error {
	c, err := m.store.GetCertificate(ctx, id)
	if err != nil {
		return err
	}
	if c.Source != model.CertACME {
		return errors.New("only ACME certificates can be renewed")
	}
	m.mu.Lock()
	delete(m.failures, id)
	m.mu.Unlock()
	go m.issue(id)
	return nil
}

// issue runs one ACME order for a certificate record.
func (m *Manager) issue(id string) {
	m.mu.Lock()
	if m.issuing[id] {
		m.mu.Unlock()
		return
	}
	m.issuing[id] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.issuing, id)
		m.mu.Unlock()
	}()

	ctx := context.Background()
	c, err := m.store.GetCertificate(ctx, id)
	if err != nil {
		return
	}
	renewal := c.NotAfter != nil
	now := time.Now()
	c.LastAttempt = &now
	err = m.obtain(c)
	c.UpdatedAt = time.Now()
	// An order takes a while; the record may have been renamed, had
	// auto-renew switched off, or been deleted meanwhile.
	fresh, ferr := m.store.GetCertificate(ctx, id)
	if ferr != nil {
		m.mu.Lock()
		delete(m.cache, id)
		m.mu.Unlock()
		os.RemoveAll(filepath.Join(m.dir, id))
		return
	}
	c.Name, c.AutoRenew = fresh.Name, fresh.AutoRenew
	if err != nil {
		m.mu.Lock()
		m.failures[id]++
		n := m.failures[id]
		m.mu.Unlock()
		c.LastError = err.Error()
		if c.NotAfter == nil || time.Now().After(*c.NotAfter) {
			c.Status = "error"
		}
		// Back off: 1h, 2h, 4h ... up to a day, to stay within CA rate limits.
		retry := time.Now().Add(min(time.Hour<<min(n-1, 5), 24*time.Hour))
		c.RenewAfter = &retry
		m.store.PutCertificate(ctx, c)
		m.bus.Error(events.CertFailed, "", "certificate %s (%s) could not be obtained: %v", c.Name, strings.Join(c.Domains, ", "), err)
		return
	}
	m.mu.Lock()
	delete(m.failures, id)
	for _, d := range c.Domains {
		delete(m.temp, d)
	}
	m.mu.Unlock()
	c.LastError = ""
	m.store.PutCertificate(ctx, c)
	if renewal {
		m.bus.Info(events.CertRenewed, "", "certificate %s renewed, valid until %s", c.Name, c.NotAfter.Format("2006-01-02"))
	} else {
		m.bus.Info(events.CertIssued, "", "certificate %s issued for %s", c.Name, strings.Join(c.Domains, ", "))
	}
}

func (m *Manager) obtain(c *model.Certificate) error {
	s := m.settings()
	opts := model.ACMEOptions{Challenge: "http-01"}
	if c.ACME != nil {
		opts = *c.ACME
	}
	kt := opts.KeyType
	if kt == "" {
		kt = s.ACME.KeyType
	}
	client, err := m.newClient(s.ACME, kt)
	if err != nil {
		return err
	}
	switch opts.Challenge {
	case "dns-01":
		var prov *model.DNSProvider
		for i := range s.DNSProviders {
			if s.DNSProviders[i].ID == opts.DNSProviderID {
				prov = &s.DNSProviders[i]
			}
		}
		if prov == nil {
			return errors.New("the selected DNS provider no longer exists")
		}
		creds := map[string]string{}
		for k, v := range prov.Credentials {
			creds[k] = m.box.MustUnseal(v)
		}
		p, err := buildDNSProvider(prov.Provider, creds)
		if err != nil {
			return err
		}
		if err := client.Challenge.SetDNS01Provider(p, dns01.AddDNSTimeout(30*time.Second)); err != nil {
			return err
		}
	default:
		if err := client.Challenge.SetHTTP01Provider(m.HTTP); err != nil {
			return err
		}
	}
	res, err := client.Certificate.Obtain(certificate.ObtainRequest{Domains: c.Domains, Bundle: true})
	if err != nil {
		return err
	}
	return m.save(c, res.Certificate, res.PrivateKey)
}

// save writes PEM material, loads it and fills the record's metadata.
func (m *Manager) save(c *model.Certificate, certPEM, keyPEM []byte) error {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("certificate and key do not match: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return err
	}
	pair.Leaf = leaf
	cp, kp := m.paths(c.ID)
	os.MkdirAll(filepath.Dir(cp), 0o700)
	if err := config.WriteFileAtomic(kp, keyPEM, 0o600); err != nil {
		return err
	}
	if err := config.WriteFileAtomic(cp, certPEM, 0o644); err != nil {
		return err
	}
	fillMeta(c, leaf, m.settings().ACME.RenewBeforeDays)
	m.mu.Lock()
	m.cache[c.ID] = &pair
	m.mu.Unlock()
	return nil
}

func fillMeta(c *model.Certificate, leaf *x509.Certificate, renewBeforeDays int) {
	nb, na := leaf.NotBefore, leaf.NotAfter
	c.NotBefore, c.NotAfter = &nb, &na
	c.Issuer = leaf.Issuer.CommonName
	if c.Issuer == "" && len(leaf.Issuer.Organization) > 0 {
		c.Issuer = leaf.Issuer.Organization[0]
	}
	c.Subject = leaf.Subject.CommonName
	c.Serial = leaf.SerialNumber.Text(16)
	sum := sha256.Sum256(leaf.Raw)
	c.Fingerprint = strings.ToUpper(hex.EncodeToString(sum[:]))
	if len(c.Domains) == 0 {
		c.Domains = append([]string{}, leaf.DNSNames...)
		for _, ip := range leaf.IPAddresses {
			c.Domains = append(c.Domains, ip.String())
		}
		if len(c.Domains) == 0 && leaf.Subject.CommonName != "" {
			c.Domains = []string{leaf.Subject.CommonName}
		}
	}
	// Renew at two thirds of the lifetime (30 days before expiry for a 90-day
	// certificate), which adapts to the CA moving to shorter lifetimes.
	renew := nb.Add(na.Sub(nb) * 2 / 3)
	if renewBeforeDays > 0 {
		renew = na.AddDate(0, 0, -renewBeforeDays)
	}
	c.RenewAfter = &renew
	c.Status = "valid"
	if time.Now().After(na) {
		c.Status = "expired"
	}
}

// Import adds a certificate from a PFX/PKCS#12 file or PEM data.
func (m *Manager) Import(ctx context.Context, name string, data, keyData []byte, password string) (*model.Certificate, error) {
	var certPEM, keyPEM []byte
	if bytes.Contains(data, []byte("-----BEGIN")) {
		certPEM = pemBlocks(data, "CERTIFICATE")
		keyPEM = pemKey(data)
		if len(keyData) > 0 {
			keyPEM = pemKey(keyData)
		}
		if len(certPEM) == 0 {
			return nil, errors.New("no certificate found in the PEM data")
		}
		if len(keyPEM) == 0 {
			return nil, errors.New("no private key found; upload the key file too")
		}
	} else {
		key, leaf, chain, err := pkcs12.DecodeChain(data, password)
		if err != nil {
			return nil, fmt.Errorf("read PFX: %w (is the password correct?)", err)
		}
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return nil, err
		}
		keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
		certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})
		for _, ca := range chain {
			certPEM = append(certPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw})...)
		}
	}
	now := time.Now()
	c := &model.Certificate{ID: uuid.NewString(), Name: name, Source: model.CertImported, CreatedAt: now, UpdatedAt: now}
	if err := m.save(c, certPEM, keyPEM); err != nil {
		return nil, err
	}
	c.RenewAfter = nil
	if c.Name == "" {
		c.Name = c.Subject
	}
	if err := m.store.PutCertificate(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

func pemBlocks(data []byte, typ string) []byte {
	var out []byte
	for {
		var b *pem.Block
		b, data = pem.Decode(data)
		if b == nil {
			return out
		}
		if b.Type == typ {
			out = append(out, pem.EncodeToMemory(b)...)
		}
	}
}

func pemKey(data []byte) []byte {
	for {
		var b *pem.Block
		b, data = pem.Decode(data)
		if b == nil {
			return nil
		}
		if strings.HasSuffix(b.Type, "PRIVATE KEY") {
			return pem.EncodeToMemory(b)
		}
	}
}

// SelfSigned creates a self-signed certificate.
func (m *Manager) SelfSigned(ctx context.Context, name string, domains []string, days int) (*model.Certificate, error) {
	domains = normalizeDomains(domains)
	if len(domains) == 0 {
		return nil, errors.New("add at least one domain")
	}
	if days <= 0 {
		days = 365
	}
	certPEM, keyPEM, err := selfSignedPEM(domains, days)
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = domains[0] + " (self-signed)"
	}
	now := time.Now()
	c := &model.Certificate{ID: uuid.NewString(), Name: name, Source: model.CertSelfSigned, Domains: domains, CreatedAt: now, UpdatedAt: now}
	if err := m.save(c, certPEM, keyPEM); err != nil {
		return nil, err
	}
	c.RenewAfter = nil
	return c, m.store.PutCertificate(ctx, c)
}

func selfSignedPEM(domains []string, days int) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: domains[0], Organization: []string{"NodeHoster"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(0, 0, days),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, d := range domains {
		if ip := net.ParseIP(d); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, d)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	kder, _ := x509.MarshalPKCS8PrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder}), nil
}

func selfSigned(domains []string, days int) (*tls.Certificate, error) {
	c, k, err := selfSignedPEM(domains, days)
	if err != nil {
		return nil, err
	}
	pair, err := tls.X509KeyPair(c, k)
	return &pair, err
}

// LoadOrCreateSelfSigned is used for the admin console's own certificate.
func LoadOrCreateSelfSigned(certPath, keyPath string, hosts []string) (*tls.Certificate, error) {
	if pair, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		if leaf, err := x509.ParseCertificate(pair.Certificate[0]); err == nil && time.Until(leaf.NotAfter) > 30*24*time.Hour {
			return &pair, nil
		}
	}
	c, k, err := selfSignedPEM(hosts, 825)
	if err != nil {
		return nil, err
	}
	if err := config.WriteFileAtomic(keyPath, k, 0o600); err != nil {
		return nil, err
	}
	if err := config.WriteFileAtomic(certPath, c, 0o644); err != nil {
		return nil, err
	}
	pair, err := tls.X509KeyPair(c, k)
	return &pair, err
}

// Update changes the editable fields.
func (m *Manager) Update(ctx context.Context, id, name string, autoRenew bool) (*model.Certificate, error) {
	c, err := m.store.GetCertificate(ctx, id)
	if err != nil {
		return nil, err
	}
	if name != "" {
		c.Name = name
	}
	c.AutoRenew = autoRenew
	c.UpdatedAt = time.Now()
	return c, m.store.PutCertificate(ctx, c)
}

// Delete removes a certificate. The caller checks that no binding uses it.
func (m *Manager) Delete(ctx context.Context, id string) error {
	c, err := m.store.GetCertificate(ctx, id)
	if err != nil {
		return err
	}
	if err := m.store.DeleteCertificate(ctx, id); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.cache, id)
	for h, mid := range m.managed {
		if mid == id {
			delete(m.managed, h)
		}
	}
	for _, d := range c.Domains {
		delete(m.temp, d) // a placeholder while it was issued
	}
	m.mu.Unlock()
	os.RemoveAll(filepath.Join(m.dir, id))
	m.log.Info("certificate deleted", "name", c.Name)
	return nil
}

// Export returns the certificate as a PFX or as a zip of PEM files.
func (m *Manager) Export(id, format, password string) (data []byte, filename, contentType string, err error) {
	c, err := m.store.GetCertificate(context.Background(), id)
	if err != nil {
		return nil, "", "", err
	}
	cp, kp := m.paths(id)
	certPEM, err := os.ReadFile(cp)
	if err != nil {
		return nil, "", "", errors.New("certificate has not been issued yet")
	}
	keyPEM, err := os.ReadFile(kp)
	if err != nil {
		return nil, "", "", err
	}
	base := safeFileName(c.Name)
	if format == "pfx" {
		pair, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return nil, "", "", err
		}
		leaf, _ := x509.ParseCertificate(pair.Certificate[0])
		var chain []*x509.Certificate
		for _, der := range pair.Certificate[1:] {
			if ca, err := x509.ParseCertificate(der); err == nil {
				chain = append(chain, ca)
			}
		}
		pfx, err := pkcs12.Modern.Encode(pair.PrivateKey.(crypto.PrivateKey), leaf, chain, password)
		if err != nil {
			return nil, "", "", err
		}
		return pfx, base + ".pfx", "application/x-pkcs12", nil
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range map[string][]byte{"fullchain.pem": certPEM, "privkey.pem": keyPEM} {
		w, _ := zw.Create(name)
		w.Write(content)
	}
	zw.Close()
	return buf.Bytes(), base + "-pem.zip", "application/zip", nil
}

func safeFileName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "certificate"
	}
	return b.String()
}

// Run renews due certificates and warns about expiring ones until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	// First pass shortly after start, once listeners are up for HTTP-01.
	first := time.After(20 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return
		case <-first:
		case <-t.C:
		}
		m.maintenance(ctx)
	}
}

func (m *Manager) maintenance(ctx context.Context) {
	list, err := m.store.ListCertificates(ctx)
	if err != nil {
		return
	}
	now := time.Now()
	warnDays := m.settings().CertExpiryWarnDays
	today := now.Format("2006-01-02")
	for _, c := range list {
		if c.Source == model.CertACME && c.AutoRenew && (c.Status == "pending" || c.RenewAfter == nil || now.After(*c.RenewAfter)) {
			go m.issue(c.ID)
			continue
		}
		if c.NotAfter == nil {
			continue
		}
		if now.After(*c.NotAfter) && c.Status != "expired" {
			c.Status = "expired"
			m.store.PutCertificate(ctx, c)
		}
		left := time.Until(*c.NotAfter)
		if warnDays > 0 && left < time.Duration(warnDays)*24*time.Hour {
			m.mu.Lock()
			already := m.warned[c.ID] == today
			m.warned[c.ID] = today
			m.mu.Unlock()
			if !already {
				m.bus.Warn(events.CertExpiring, "", "certificate %s expires in %d day(s) (%s)", c.Name, int(left.Hours()/24), c.NotAfter.Format("2006-01-02"))
			}
		}
	}
}

// RetryPending immediately retries ACME certificates that are waiting or
// failed, for example right after the ACME settings were corrected.
func (m *Manager) RetryPending(ctx context.Context) {
	list, err := m.store.ListCertificates(ctx)
	if err != nil {
		return
	}
	for _, c := range list {
		if c.Source == model.CertACME && (c.Status == "pending" || c.Status == "error") {
			m.mu.Lock()
			delete(m.failures, c.ID)
			m.mu.Unlock()
			go m.issue(c.ID)
		}
	}
}
