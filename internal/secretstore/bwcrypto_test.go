package secretstore

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

// Test vectors from the official SDK (github.com/bitwarden/sdk-internal
// and github.com/bitwarden/sdk-sm): the access token of its fake server,
// the key the SDK derives from it, the identity answer's encrypted payload
// and secrets encrypted with the organization key it holds.
const (
	sdkAccessToken      = "0.ec2c1d46-6a4b-4751-a310-af9601317f2d.C2IgxjjLF7qSshsbwe8JGcbM075YXw:X8vbvA0bduihIDe/qrzIQQ=="
	sdkAccessTokenKey   = "H9/oIRLtL9nGCQOVDjSMoEbJsjWXSOCb3qeyDt6ckzS3FhyboEDWyTP/CQfbIszNmAVg2ExFganG1FVFGXO/Jg=="
	sdkEncryptedPayload = "2.E9fE8+M/VWMfhhim1KlCbQ==|eLsHR484S/tJbIkM6spnG/HP65tj9A6Tba7kAAvUp+rYuQmGLixiOCfMsqt5OvBctDfvvr/AesBu7cZimPLyOEhqEAjn52jF0eaI38XZfeOG2VJl0LOf60Wkfh3ryAMvfvLj3G4ZCNYU8sNgoC2+IQ==|lNApuCQ4Pyakfo/wwuuajWNaEX/2MW8/3rjXB/V7n+k="
	sdkOrgKey           = "k/6PcwG7Hm/eZfvvOvP6EqGx1JKgbzYrWwpOIHsxbJHQMpIMg5Ud94AQHduSR+XMMaFiSB+nszbO4JPXj04YwA=="
	sdkOrgID            = "f4e44a7f-1190-432a-9d4a-af96013127cb"
	// The fake server's "TUX" secret: key, value and note.
	sdkTuxKey   = "2.OldQj0RJKww0WN7RSxI1wQ==|TpxAbmdx6zIVo37YJ5n1aQ==|06Imyx7jqaZ5J5amrBboCVPwvPoDKB8REJdToQwp3dA="
	sdkTuxValue = "2.oEDp566lC9VYHn6XmusxfA==|Gj23w5q2NZ4z9PNne1d0ug==|y7K5TgMJFI0T0yFwLXzAMf9OBANNT567hLQ+z7G2rac="
	sdkTuxNote  = "2.owktgGRm4r+ho4WY4U9zvA==|6Up5NQHyZ65SL3vbNI1GhQ==|vdvWvPpoB/J3aWXKBiruqOr1SK/ndkCCTjHf2vphhu4="
)

func keyB64(k bwKey) string { return base64.StdEncoding.EncodeToString(append(k.enc[:], k.mac[:]...)) }

// The SDK's test_derive_shareable_key vectors.
func TestDeriveShareableKey(t *testing.T) {
	for _, c := range []struct{ secret, name, info, want string }{
		{"&/$%F1a895g67HlX", "test_key", "", "4PV6+PcmF2w7YHRatvyMcVQtI7zvCyssv/wFWmzjiH6Iv9altjmDkuBD1aagLVaLezbthbSe+ktR+U6qswxNnQ=="},
		{"67t9b5g67$%Dh89n", "test_key", "test", "F9jVQmrACGx9VUPjuzfMYDjr726JtL300Y3Yg+VYUnVQtQ1s8oImJ5xtp1KALC9h2nav04++1LDW4iFD+infng=="},
	} {
		if got := keyB64(deriveShareableKey([]byte(c.secret), c.name, c.info)); got != c.want {
			t.Errorf("derive(%q, %q, %q) = %s, want %s", c.secret, c.name, c.info, got, c.want)
		}
	}
}

// The SDK's can_decode_access_token and malformed_tokens tests.
func TestParseAccessToken(t *testing.T) {
	tok, err := parseAccessToken(sdkAccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if tok.id != "ec2c1d46-6a4b-4751-a310-af9601317f2d" || tok.secret != "C2IgxjjLF7qSshsbwe8JGcbM075YXw" {
		t.Errorf("token = %q, %q", tok.id, tok.secret)
	}
	if got := keyB64(tok.key); got != sdkAccessTokenKey {
		t.Errorf("key = %s, want %s", got, sdkAccessTokenKey)
	}
	// Without base64 padding: accepted.
	if _, err := parseAccessToken(strings.TrimSuffix(sdkAccessToken, "==")); err != nil {
		t.Errorf("unpadded key: %v", err)
	}
	for _, bad := range []string{
		"1.ec2c1d46-6a4b-4751-a310-af9601317f2d.C2IgxjjLF7qSshsbwe8JGcbM075YXw:X8vbvA0bduihIDe/qrzIQQ==", // version
		"0.ec2c1d46-6a4b-4751-a310-af9601317f2d.C2IgxjjLF7qSshsbwe8JGcbM075YXw.X8vbvA0bduihIDe/qrzIQQ==", // no key
		"ec2c1d46-6a4b-4751-a310-af9601317f2d.C2IgxjjLF7qSshsbwe8JGcbM075YXw:X8vbvA0bduihIDe/qrzIQQ==",   // parts
		"0.not-a-uuid.C2IgxjjLF7qSshsbwe8JGcbM075YXw:X8vbvA0bduihIDe/qrzIQQ==",
		"0.ec2c1d46-6a4b-4751-a310-af9601317f2d.C2IgxjjLF7qSshsbwe8JGcbM075YXw:X8vbvA0bduihIDe/qrzI", // 15 bytes
		"0.ec2c1d46-6a4b-4751-a310-af9601317f2d.C2IgxjjLF7qSshsbwe8JGcbM075YXw:!!!!",
	} {
		_, err := parseAccessToken(bad)
		if err == nil {
			t.Errorf("parseAccessToken(%q) accepted", bad)
			continue
		}
		if strings.Contains(err.Error(), "C2Igx") {
			t.Errorf("the error quotes the token: %v", err)
		}
	}
}

// The payload of the SDK's fake identity server decrypts, with the key
// derived from its access token, into the organization key, which decrypts
// the fake server's secrets.
func TestDecryptSDKFixtures(t *testing.T) {
	tok, _ := parseAccessToken(sdkAccessToken)
	payload, err := decryptEncString(sdkEncryptedPayload, tok.key)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"encryptionKey":"` + sdkOrgKey + `"}`; string(payload) != want {
		t.Fatalf("payload = %s", payload)
	}
	raw, _ := base64.StdEncoding.DecodeString(sdkOrgKey)
	org, err := bwKeyFromBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	for enc, want := range map[string]string{sdkTuxKey: "TUX", sdkTuxValue: "🐧", sdkTuxNote: ""} {
		got, err := decryptEncString(enc, org)
		if err != nil || string(got) != want {
			t.Errorf("decrypt = %q, %v; want %q", got, err, want)
		}
	}
	// With the wrong key the MAC does not match.
	if _, err := decryptEncString(sdkTuxValue, tok.key); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Errorf("wrong key: %v", err)
	}
}

func TestEncStringRoundTripAndTampering(t *testing.T) {
	k := deriveShareableKey([]byte("0123456789abcdef"), "test", "")
	iv := bytes.Repeat([]byte{7}, 16)
	for _, plain := range []string{"", "x", "exactly 16 bytes", strings.Repeat("long value ", 50)} {
		enc := encryptEncString([]byte(plain), k, iv)
		got, err := decryptEncString(enc, k)
		if err != nil || string(got) != plain {
			t.Errorf("round trip %q = %q, %v", plain, got, err)
		}
	}
	enc := encryptEncString([]byte("secret"), k, iv)
	parts := strings.Split(strings.TrimPrefix(enc, "2."), "|")
	data, _ := base64.StdEncoding.DecodeString(parts[1])
	data[0] ^= 1
	tampered := "2." + parts[0] + "|" + base64.StdEncoding.EncodeToString(data) + "|" + parts[2]
	for _, bad := range []string{
		tampered,
		"0." + parts[0] + "|" + parts[1],                  // AES-CBC without a MAC: refused, as by the SDK
		"7.AAAA",                                          // COSE
		"2." + parts[0] + "|" + parts[1],                  // missing MAC
		"2.@@@|" + parts[1] + "|" + parts[2],              // base64
		strings.TrimPrefix(enc, "2."),                     // no type
		"2." + parts[0] + "|" + parts[1] + "|" + parts[0], // short MAC
	} {
		if _, err := decryptEncString(bad, k); err == nil {
			t.Errorf("decrypt(%q) accepted", bad)
		}
	}
	if _, err := bwKeyFromBytes(make([]byte, 32)); err == nil {
		t.Error("a 32-byte key was accepted as an organization key")
	}
}
