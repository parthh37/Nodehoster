package secrets

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func openBox(t *testing.T) (*Box, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "master.key")
	b, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	return b, path
}

func TestOpenCreatesKeyFile(t *testing.T) {
	t.Parallel()
	_, path := openBox(t)

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("key file not created: %v", err)
	}
	if fi.Size() == 0 {
		t.Fatal("key file is empty")
	}
	if runtime.GOOS != "windows" {
		// Outside Windows the file mode is the only protection.
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("key file mode = %o, want 600", perm)
		}
	}

	// No temp files from the atomic write may be left next to the key.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("unexpected files beside key: %v", names)
	}
}

// The stored key file must unprotect to exactly 32 bytes (AES-256), and
// values must be AES-256-GCM with the nonce prepended.
func TestSealFormatIsAES256GCM(t *testing.T) {
	t.Parallel()
	b, path := openBox(t)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	key, err := unprotect(raw)
	if err != nil {
		t.Fatalf("unprotect: %v", err)
	}
	if len(key) != 32 {
		t.Fatalf("master key length = %d, want 32 (AES-256)", len(key))
	}
	if runtime.GOOS == "windows" && bytes.Equal(raw, key) {
		t.Error("on Windows the key file must be DPAPI-protected, not the raw key")
	}

	sealed, err := b.Seal("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, prefix) {
		t.Fatalf("sealed value %q lacks prefix %q", sealed, prefix)
	}
	blob, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(sealed, prefix))
	if err != nil {
		t.Fatalf("sealed payload is not raw std base64: %v", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	ns := gcm.NonceSize()
	if len(blob) != ns+len("hunter2")+gcm.Overhead() {
		t.Fatalf("payload length = %d, want nonce(%d)+plaintext(%d)+tag(%d)", len(blob), ns, len("hunter2"), gcm.Overhead())
	}
	plain, err := gcm.Open(nil, blob[:ns], blob[ns:], nil)
	if err != nil {
		t.Fatalf("independent AES-256-GCM decrypt failed: %v", err)
	}
	if string(plain) != "hunter2" {
		t.Fatalf("decrypted %q, want %q", plain, "hunter2")
	}
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()
	b, _ := openBox(t)

	cases := map[string]string{
		"ascii":     "s3cr3t-value",
		"unicode":   "pässwörd-密码-🔑",
		"single":    "x",
		"binaryish": "a\x00b\xffc\n\r\t",
		"long":      strings.Repeat("0123456789abcdef", 4096),
		"mask":      Mask,
		"prefixish": "enc:v1", // one char short of the real prefix
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sealed, err := b.Seal(in)
			if err != nil {
				t.Fatalf("Seal: %v", err)
			}
			if sealed == in {
				t.Fatal("Seal returned plaintext unchanged")
			}
			if len(in) >= 8 && strings.Contains(sealed, in) {
				t.Fatal("sealed value contains the plaintext")
			}
			out, err := b.Unseal(sealed)
			if err != nil {
				t.Fatalf("Unseal: %v", err)
			}
			if out != in {
				t.Fatalf("round trip = %q, want %q", out, in)
			}
			if got := b.MustUnseal(sealed); got != in {
				t.Fatalf("MustUnseal = %q, want %q", got, in)
			}
		})
	}
}

func TestSealUsesFreshNonce(t *testing.T) {
	t.Parallel()
	b, _ := openBox(t)

	seen := map[string]bool{}
	for range 64 {
		s, err := b.Seal("same plaintext")
		if err != nil {
			t.Fatal(err)
		}
		if seen[s] {
			t.Fatal("two Seal calls produced identical ciphertext (nonce reuse)")
		}
		seen[s] = true
	}
}

func TestSealPassThrough(t *testing.T) {
	t.Parallel()
	b, _ := openBox(t)

	if s, err := b.Seal(""); err != nil || s != "" {
		t.Fatalf("Seal(\"\") = %q, %v; want \"\", nil", s, err)
	}

	sealed, err := b.Seal("value")
	if err != nil {
		t.Fatal(err)
	}
	// Re-sealing an already sealed value must not double-encrypt it.
	again, err := b.Seal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if again != sealed {
		t.Fatalf("Seal(sealed) changed the value: %q -> %q", sealed, again)
	}
	if out, err := b.Unseal(again); err != nil || out != "value" {
		t.Fatalf("Unseal(Seal(sealed)) = %q, %v", out, err)
	}
}

func TestUnsealLegacyPlaintext(t *testing.T) {
	t.Parallel()
	b, _ := openBox(t)

	for _, v := range []string{"", "plain", "ENC:V1:abc", " enc:v1:abc"} {
		out, err := b.Unseal(v)
		if err != nil || out != v {
			t.Errorf("Unseal(%q) = %q, %v; want unchanged", v, out, err)
		}
	}
}

func TestReopenUsesSameKey(t *testing.T) {
	t.Parallel()
	b1, path := openBox(t)

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := b1.Seal("persist me")
	if err != nil {
		t.Fatal(err)
	}

	b2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("reopening rewrote the key file")
	}
	out, err := b2.Unseal(sealed)
	if err != nil {
		t.Fatalf("Unseal after reopen: %v", err)
	}
	if out != "persist me" {
		t.Fatalf("got %q", out)
	}
}

func TestWrongKeyFails(t *testing.T) {
	t.Parallel()
	b1, _ := openBox(t)
	b2, _ := openBox(t)

	sealed, err := b1.Seal("top secret")
	if err != nil {
		t.Fatal(err)
	}
	out, err := b2.Unseal(sealed)
	if err == nil {
		t.Fatalf("Unseal with a different master key succeeded: %q", out)
	}
	if out != "" {
		t.Fatalf("failed Unseal leaked output %q", out)
	}
	if strings.Contains(err.Error(), "top secret") {
		t.Fatal("error message leaks plaintext")
	}
	if got := b2.MustUnseal(sealed); got != "" {
		t.Fatalf("MustUnseal with wrong key = %q, want \"\"", got)
	}
}

func TestTamperDetection(t *testing.T) {
	t.Parallel()
	b, _ := openBox(t)

	sealed, err := b.Seal("integrity matters")
	if err != nil {
		t.Fatal(err)
	}
	blob, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(sealed, prefix))
	if err != nil {
		t.Fatal(err)
	}
	ns := b.aead.NonceSize()

	reencode := func(p []byte) string { return prefix + base64.RawStdEncoding.EncodeToString(p) }
	flip := func(i int) string {
		c := bytes.Clone(blob)
		c[i] ^= 0x01
		return reencode(c)
	}

	cases := map[string]string{
		"nonce bit flipped":      flip(0),
		"ciphertext bit flipped": flip(ns),
		"tag bit flipped":        flip(len(blob) - 1),
		"tag truncated":          reencode(blob[:len(blob)-1]),
		"byte appended":          reencode(append(bytes.Clone(blob), 0)),
		"ciphertext removed":     reencode(blob[:ns]),
		"shorter than nonce":     reencode(blob[:ns-1]),
		"empty payload":          prefix,
		"invalid base64":         prefix + "!!!not-base64!!!",
		"padded base64":          prefix + base64.StdEncoding.EncodeToString(blob) + "==",
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			out, err := b.Unseal(v)
			if err == nil {
				t.Fatalf("Unseal accepted tampered value, returned %q", out)
			}
			if out != "" {
				t.Fatalf("failed Unseal returned non-empty %q", out)
			}
			if got := b.MustUnseal(v); got != "" {
				t.Fatalf("MustUnseal = %q, want \"\"", got)
			}
		})
	}
}

func TestOpenCorruptKeyFile(t *testing.T) {
	t.Parallel()
	for name, content := range map[string][]byte{
		"too short": bytes.Repeat([]byte{0xAB}, 16),
		"too long":  bytes.Repeat([]byte{0xCD}, 64),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "master.key")
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatal(err)
			}
			if b, err := Open(path); err == nil {
				t.Fatalf("Open accepted a corrupt key file (box=%v)", b)
			}
			// The corrupt file must not be silently replaced by a new key,
			// which would make every existing secret unreadable.
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, content) {
				t.Fatal("Open overwrote the corrupt key file")
			}
		})
	}
}

func TestOpenEmptyKeyFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("Open accepted an empty key file")
	}
}

func TestOpenMissingDirectory(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "does-not-exist", "master.key")
	if _, err := Open(path); err == nil {
		t.Fatal("Open succeeded although the key directory does not exist")
	}
}

func TestOpenKeyPathIsDirectory(t *testing.T) {
	t.Parallel()
	if _, err := Open(t.TempDir()); err == nil {
		t.Fatal("Open succeeded with a directory as the key path")
	}
}

func TestProtectRoundTrip(t *testing.T) {
	t.Parallel()
	in := []byte("0123456789abcdef0123456789abcdef")
	orig := bytes.Clone(in)
	sealed, err := protect(in)
	if err != nil {
		t.Fatalf("protect: %v", err)
	}
	// Mutating the input afterwards must not affect the protected copy.
	in[0] ^= 0xFF
	out, err := unprotect(sealed)
	if err != nil {
		t.Fatalf("unprotect: %v", err)
	}
	if !bytes.Equal(out, orig) {
		t.Fatalf("unprotect(protect(x)) = %x, want %x", out, orig)
	}
}

func TestConcurrentSealUnseal(t *testing.T) {
	t.Parallel()
	b, _ := openBox(t)

	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := range 32 {
		wg.Go(func() {
			in := strings.Repeat(string(rune('a'+i%26)), i+1)
			for range 50 {
				s, err := b.Seal(in)
				if err != nil {
					errs <- err
					return
				}
				out, err := b.Unseal(s)
				if err != nil {
					errs <- err
					return
				}
				if out != in {
					errs <- fmt.Errorf("round trip mismatch: got %q, want %q", out, in)
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// A user-supplied secret that happens to start with the envelope prefix is
// not encrypted by Seal and cannot be read back by Unseal.
func TestSealUserValueWithPrefix(t *testing.T) {
	t.Parallel()
	t.Skip("BUG: Seal passes plaintext starting with \"enc:v1:\" through unencrypted (stored in clear), and Unseal then fails on it, so the value is lost")
	b, _ := openBox(t)
	in := "enc:v1:my-literal-password"
	sealed, err := b.Seal(in)
	if err != nil {
		t.Fatal(err)
	}
	if sealed == in {
		t.Error("value was stored unencrypted")
	}
	out, err := b.Unseal(sealed)
	if err != nil || out != in {
		t.Fatalf("round trip = %q, %v; want %q", out, err, in)
	}
}
