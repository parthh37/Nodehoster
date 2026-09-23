package mail

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"github.com/emersion/go-msgauth/dkim"
)

// GenerateDKIMKey returns a new RSA-2048 private key in PEM, the size
// every receiver supports and that fits in a single DNS TXT record.
func GenerateDKIMKey() (string, error) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", err
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), nil
}

// ParseDKIMKey reads an RSA or Ed25519 private key in PEM (PKCS#1 or
// PKCS#8).
func ParseDKIMKey(p string) (crypto.Signer, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(p)))
	if block == nil {
		return nil, errors.New("not a PEM private key")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, checkRSA(k)
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("not a private key: %v", err)
	}
	switch k := k.(type) {
	case *rsa.PrivateKey:
		return k, checkRSA(k)
	case ed25519.PrivateKey:
		return k, nil
	}
	return nil, errors.New("only RSA and Ed25519 keys can sign DKIM")
}

func checkRSA(k *rsa.PrivateKey) error {
	if k.N.BitLen() < 1024 {
		return errors.New("RSA keys shorter than 1024 bits are rejected by receivers")
	}
	return nil
}

// DKIMRecord is the TXT record that publishes the key's public half at
// <selector>._domainkey.<domain>.
func DKIMRecord(p string) (string, error) {
	k, err := ParseDKIMKey(p)
	if err != nil {
		return "", err
	}
	switch pub := k.Public().(type) {
	case *rsa.PublicKey:
		der, err := x509.MarshalPKIXPublicKey(pub)
		if err != nil {
			return "", err
		}
		return "v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(der), nil
	case ed25519.PublicKey:
		return "v=DKIM1; k=ed25519; p=" + base64.StdEncoding.EncodeToString(pub), nil
	}
	return "", errors.New("unsupported key type")
}

// DKIMName is where the record is published.
func DKIMName(selector, domain string) string {
	return selector + "._domainkey." + domain
}

// signDKIM returns the message with a DKIM-Signature header added.
// Relaxed canonicalization survives the header folding and whitespace
// changes relays commonly make.
func signDKIM(msg []byte, domain, selector string, key crypto.Signer) ([]byte, error) {
	var out bytes.Buffer
	err := dkim.Sign(&out, bytes.NewReader(msg), &dkim.SignOptions{
		Domain:                 domain,
		Selector:               selector,
		Signer:                 key,
		HeaderCanonicalization: dkim.CanonicalizationRelaxed,
		BodyCanonicalization:   dkim.CanonicalizationRelaxed,
		HeaderKeys: []string{
			"From", "Reply-To", "Subject", "Date", "To", "Cc", "Message-ID",
			"In-Reply-To", "References", "MIME-Version", "Content-Type", "Content-Transfer-Encoding",
			"List-Unsubscribe", "List-Unsubscribe-Post",
		},
	})
	if err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
