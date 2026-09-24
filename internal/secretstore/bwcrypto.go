package secretstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Bitwarden Secrets Manager cryptography, as the official SDK implements
// it (github.com/bitwarden/sdk-internal: bitwarden-core auth/access_token.rs
// and auth/login/access_token.rs, bitwarden-crypto keys/shareable_key.rs,
// enc_string/symmetric.rs and hazmat/symmetric_encryption). The SDK is Rust;
// this is the small part a reader of secrets needs, in pure Go.
//
// A machine account's access token is
//
//	0.<access token id (UUID)>.<client secret>:<base64 of a 16-byte key>
//
// The id and client secret sign in to the identity server (OAuth client
// credentials, scope api.secrets). The 16-byte key is stretched into an
// AES-256-CBC + HMAC-SHA256 key that decrypts the "encrypted_payload" of
// the identity server's answer: JSON {"encryptionKey": base64}, the
// organization's key, which decrypts the secrets.

// bwKey is an AES-256-CBC key with its HMAC-SHA256 key (Bitwarden's
// "Aes256CbcHmacKey", 64 bytes: encryption key then MAC key).
type bwKey struct {
	enc [32]byte
	mac [32]byte
}

// bwAccessToken is a parsed machine account access token.
type bwAccessToken struct {
	id     string // the access token's UUID: the OAuth client_id
	secret string // the OAuth client_secret
	key    bwKey  // decrypts the identity server's payload
}

var bwUUIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// parseAccessToken reads a machine account access token. Errors never
// quote the token.
func parseAccessToken(tok string) (bwAccessToken, error) {
	first, keyB64, ok := strings.Cut(strings.TrimSpace(tok), ":")
	if !ok {
		return bwAccessToken{}, errors.New("the access token has no decryption key (it should end with :<key>==)")
	}
	parts := strings.Split(first, ".")
	if len(parts) != 3 {
		return bwAccessToken{}, errors.New("the access token has the wrong number of parts")
	}
	if parts[0] != "0" {
		return bwAccessToken{}, errors.New("the access token is of an unsupported version")
	}
	if !bwUUIDRe.MatchString(parts[1]) {
		return bwAccessToken{}, errors.New("the access token has an invalid identifier")
	}
	// The SDK writes the key with padding and accepts it without.
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(keyB64, "="))
	if err != nil {
		return bwAccessToken{}, errors.New("the access token's key is not valid base64")
	}
	if len(raw) != 16 {
		return bwAccessToken{}, fmt.Errorf("the access token's key is %d bytes instead of 16", len(raw))
	}
	return bwAccessToken{
		id:     strings.ToLower(parts[1]),
		secret: parts[2],
		key:    deriveShareableKey(raw, "accesstoken", "sm-access-token"),
	}, nil
}

// deriveShareableKey is the SDK's derive_shareable_key: a pseudo-random key
// HMAC-SHA256("bitwarden-"+name, secret), expanded with HKDF-SHA256 (info
// as given) to 64 bytes: the encryption key, then the MAC key.
func deriveShareableKey(secret []byte, name, info string) bwKey {
	m := hmac.New(sha256.New, []byte("bitwarden-"+name))
	m.Write(secret)
	prk := m.Sum(nil)
	out, err := hkdf.Expand(sha256.New, prk, info, 64)
	if err != nil {
		panic(err) // fixed sizes: cannot happen
	}
	var k bwKey
	copy(k.enc[:], out[:32])
	copy(k.mac[:], out[32:])
	return k
}

// bwKeyFromBytes reads a symmetric key in Bitwarden's byte format. Only
// the 64-byte AES-CBC-HMAC keys that organizations have are supported;
// COSE-encoded keys (longer) are not used for organization keys yet.
func bwKeyFromBytes(b []byte) (bwKey, error) {
	if len(b) != 64 {
		return bwKey{}, fmt.Errorf("the organization key is of an unsupported type (%d bytes; a 64-byte AES-256-CBC-HMAC key was expected)", len(b))
	}
	var k bwKey
	copy(k.enc[:], b[:32])
	copy(k.mac[:], b[32:])
	return k, nil
}

// decryptEncString decrypts a type 2 EncString,
// "2.<iv b64>|<ciphertext b64>|<mac b64>": AES-256-CBC with PKCS#7
// padding, authenticated by HMAC-SHA256(mac key, iv || ciphertext), which
// is checked first in constant time. Type 0 (CBC without a MAC) is refused,
// as the SDK refuses it, and so is type 7 (COSE), which organization keys
// do not produce.
func decryptEncString(s string, k bwKey) ([]byte, error) {
	typ, rest, ok := strings.Cut(s, ".")
	if !ok {
		return nil, errors.New("not an encrypted value (no type)")
	}
	if typ != "2" {
		return nil, fmt.Errorf("encrypted value of unsupported type %s", typ)
	}
	parts := strings.Split(rest, "|")
	if len(parts) != 3 {
		return nil, errors.New("malformed encrypted value")
	}
	iv, err1 := base64.StdEncoding.DecodeString(parts[0])
	data, err2 := base64.StdEncoding.DecodeString(parts[1])
	mac, err3 := base64.StdEncoding.DecodeString(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return nil, errors.New("malformed encrypted value (base64)")
	}
	if len(iv) != aes.BlockSize || len(mac) != sha256.Size {
		return nil, errors.New("malformed encrypted value (sizes)")
	}
	m := hmac.New(sha256.New, k.mac[:])
	m.Write(iv)
	m.Write(data)
	if !hmac.Equal(m.Sum(nil), mac) {
		return nil, errors.New("the encrypted value does not match its key (wrong access token or organization key)")
	}
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, errors.New("malformed encrypted value (length)")
	}
	block, err := aes.NewCipher(k.enc[:])
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, data)
	n := int(out[len(out)-1])
	if n == 0 || n > aes.BlockSize || n > len(out) {
		return nil, errors.New("malformed encrypted value (padding)")
	}
	for _, b := range out[len(out)-n:] {
		if int(b) != n {
			return nil, errors.New("malformed encrypted value (padding)")
		}
	}
	return out[:len(out)-n], nil
}

// encryptEncString is the inverse of decryptEncString with a given IV. The
// store only reads; tests use it to build values the way the SDK does.
func encryptEncString(plain []byte, k bwKey, iv []byte) string {
	n := aes.BlockSize - len(plain)%aes.BlockSize
	data := append([]byte{}, plain...)
	for range n {
		data = append(data, byte(n))
	}
	block, _ := aes.NewCipher(k.enc[:])
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(data, data)
	m := hmac.New(sha256.New, k.mac[:])
	m.Write(iv)
	m.Write(data)
	enc := base64.StdEncoding.EncodeToString
	return "2." + enc(iv) + "|" + enc(data) + "|" + enc(m.Sum(nil))
}
