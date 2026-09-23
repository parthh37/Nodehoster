package certs

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"
	"github.com/parthh37/nodehoster/internal/config"
	"github.com/parthh37/nodehoster/internal/model"
)

var directories = map[string]string{
	"letsencrypt":         lego.LEDirectoryProduction,
	"letsencrypt-staging": lego.LEDirectoryStaging,
	"zerossl":             "https://acme.zerossl.com/v2/DV90",
}

func directoryURL(d string) string {
	if u, ok := directories[d]; ok {
		return u
	}
	if strings.HasPrefix(d, "https://") {
		return d
	}
	return lego.LEDirectoryProduction
}

func keyType(k string) certcrypto.KeyType {
	switch strings.ToLower(k) {
	case "ec384":
		return certcrypto.EC384
	case "rsa2048":
		return certcrypto.RSA2048
	case "rsa3072":
		return certcrypto.RSA3072
	case "rsa4096":
		return certcrypto.RSA4096
	default:
		return certcrypto.EC256
	}
}

// acmeUser implements registration.User and is persisted per directory and
// email, so accounts are reused rather than re-registered on every order.
type acmeUser struct {
	Email        string                 `json:"email"`
	Registration *registration.Resource `json:"registration"`
	KeyPEM       string                 `json:"key"`
	key          crypto.PrivateKey
}

func (u *acmeUser) GetEmail() string                        { return u.Email }
func (u *acmeUser) GetRegistration() *registration.Resource { return u.Registration }
func (u *acmeUser) GetPrivateKey() crypto.PrivateKey        { return u.key }

var accountMu sync.Mutex

// newClient returns a lego client with a registered account.
func (m *Manager) newClient(s model.ACMESettings, kt string) (*lego.Client, error) {
	if !s.AgreeTOS {
		return nil, errors.New("accept the certificate authority's terms of service in Settings → ACME first")
	}
	dirURL := directoryURL(s.Directory)
	sum := sha256.Sum256([]byte(dirURL + "|" + s.Email))
	path := filepath.Join(m.acmeDir, hex.EncodeToString(sum[:8]), "account.json")

	accountMu.Lock()
	defer accountMu.Unlock()

	user := &acmeUser{Email: s.Email}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, user); err != nil {
			return nil, fmt.Errorf("read ACME account: %w", err)
		}
		block, _ := pem.Decode([]byte(user.KeyPEM))
		if block == nil {
			return nil, errors.New("ACME account key is corrupt")
		}
		k, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		user.key = k
	} else {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		der, _ := x509.MarshalECPrivateKey(k)
		user.key = k
		user.KeyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
	}

	cfg := lego.NewConfig(user)
	cfg.CADirURL = dirURL
	cfg.Certificate.KeyType = keyType(kt)
	cfg.UserAgent = "NodeHoster/" + config.Version
	client, err := lego.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", dirURL, err)
	}
	if user.Registration == nil {
		var reg *registration.Resource
		if s.EABKeyID != "" {
			reg, err = client.Registration.RegisterWithExternalAccountBinding(registration.RegisterEABOptions{
				TermsOfServiceAgreed: true, Kid: s.EABKeyID, HmacEncoded: m.box.MustUnseal(s.EABHMAC),
			})
		} else {
			reg, err = client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
		}
		if err != nil {
			return nil, fmt.Errorf("register ACME account: %w", err)
		}
		user.Registration = reg
		data, _ := json.MarshalIndent(user, "", "  ")
		os.MkdirAll(filepath.Dir(path), 0o700)
		if err := config.WriteFileAtomic(path, data, 0o600); err != nil {
			return nil, err
		}
		m.log.Info("registered ACME account", "directory", dirURL, "email", s.Email)
	}
	return client, nil
}

// HTTPChallenges answers ACME HTTP-01 challenges. The proxy serves
// /.well-known/acme-challenge/ from it on every HTTP listener; when nothing
// listens on port 80, a temporary listener is opened for the challenge.
type HTTPChallenges struct {
	log       *slog.Logger
	HasPort80 func() bool

	mu     sync.Mutex
	tokens map[string]string
	tmp    *http.Server
	refs   int
}

func newHTTPChallenges(log *slog.Logger) *HTTPChallenges {
	return &HTTPChallenges{log: log, tokens: map[string]string{}}
}

const challengePrefix = "/.well-known/acme-challenge/"

// ServeHTTP answers a challenge request. It reports false when the request
// is not a known challenge so the caller can route it normally.
func (h *HTTPChallenges) ServeHTTP(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, challengePrefix) {
		return false
	}
	token := strings.TrimPrefix(r.URL.Path, challengePrefix)
	h.mu.Lock()
	ka, ok := h.tokens[token]
	h.mu.Unlock()
	if !ok {
		return false
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(ka))
	return true
}

func (h *HTTPChallenges) Present(domain, token, keyAuth string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.tokens[token] = keyAuth
	if h.HasPort80 != nil && h.HasPort80() {
		return nil
	}
	h.refs++
	if h.tmp != nil {
		return nil
	}
	l, err := net.Listen("tcp", ":80")
	if err != nil {
		h.refs--
		return fmt.Errorf("no site is bound to port 80 and port 80 could not be opened for the HTTP-01 challenge: %w", err)
	}
	srv := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !h.ServeHTTP(w, r) {
				http.NotFound(w, r)
			}
		}),
	}
	h.tmp = srv
	go srv.Serve(l)
	h.log.Info("opened temporary port 80 listener for ACME challenge")
	return nil
}

func (h *HTTPChallenges) CleanUp(domain, token, keyAuth string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.tokens, token)
	if h.tmp != nil {
		h.refs--
		if h.refs <= 0 {
			h.tmp.Close()
			h.tmp, h.refs = nil, 0
		}
	}
	return nil
}
