package backup

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/scrypt"
)

// Encrypted payloads are AES-256-GCM in 64 KiB chunks (the STREAM
// construction that age and Tink use), so an archive of any size is
// encrypted and verified in constant memory, and a truncated or reordered
// file is detected. The key comes from the passphrase through scrypt.
//
// Layout: a 40-byte header, then the chunks.
//
//	magic "NHBKENC1" | kdf=1 (scrypt) | log2 N | r | p | salt[16] |
//	nonce prefix[7] | chunk size (uint32, big endian) | reserved[1]
//
// Chunk i is sealed with the nonce prefix || i (uint32) || last (0 or 1)
// and the header as additional data, so the parameters cannot be swapped
// either.
const (
	cryptMagic   = "NHBKENC1"
	headerSize   = 8 + 4 + 16 + 7 + 4 + 1
	chunkSize    = 64 << 10
	defaultLogN  = 15 // scrypt N = 32768, r = 8, p = 1: ~32 MB and ~0.1 s
	maxLogN      = 20
	saltSize     = 16
	noncePrefLen = 7
)

// ScryptLogN is scrypt's cost; tests lower it.
var ScryptLogN uint8 = defaultLogN

// ErrWrongPassphrase is returned when an encrypted archive does not open
// with the passphrase given.
var ErrWrongPassphrase = errors.New("the passphrase is wrong, or the archive is damaged")

// ErrNotEncrypted is returned by NewDecrypter for data without the header.
var ErrNotEncrypted = errors.New("not an encrypted NodeHoster backup")

type encrypter struct {
	w      io.Writer
	aead   cipher.AEAD
	header []byte
	nonce  [12]byte
	n      uint32
	buf    []byte
	closed bool
}

// NewEncrypter returns a writer that encrypts everything written to it
// into w under passphrase. Close writes the final chunk; it does not close w.
func NewEncrypter(w io.Writer, passphrase string) (io.WriteCloser, error) {
	if passphrase == "" {
		return nil, errors.New("no passphrase")
	}
	h := make([]byte, headerSize)
	copy(h, cryptMagic)
	h[8], h[9], h[10], h[11] = 1, ScryptLogN, 8, 1
	if _, err := rand.Read(h[12 : 12+saltSize+noncePrefLen]); err != nil {
		return nil, err
	}
	binary.BigEndian.PutUint32(h[35:39], chunkSize)
	aead, err := deriveAEAD(passphrase, h)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(h); err != nil {
		return nil, err
	}
	e := &encrypter{w: w, aead: aead, header: h, buf: make([]byte, 0, chunkSize)}
	copy(e.nonce[:noncePrefLen], h[28:35])
	return e, nil
}

func (e *encrypter) Write(p []byte) (int, error) {
	if e.closed {
		return 0, errors.New("write after close")
	}
	total := len(p)
	for len(p) > 0 {
		// A full buffer is only flushed once more data arrives: the last
		// chunk must be marked as such, and may be full.
		if len(e.buf) == chunkSize {
			if err := e.flush(false); err != nil {
				return total - len(p), err
			}
		}
		n := copy(e.buf[len(e.buf):chunkSize], p)
		e.buf = e.buf[:len(e.buf)+n]
		p = p[n:]
	}
	return total, nil
}

func (e *encrypter) flush(last bool) error {
	if e.n == ^uint32(0) {
		return errors.New("archive too large to encrypt")
	}
	binary.BigEndian.PutUint32(e.nonce[noncePrefLen:11], e.n)
	e.nonce[11] = 0
	if last {
		e.nonce[11] = 1
	}
	out := e.aead.Seal(nil, e.nonce[:], e.buf, e.header)
	e.n++
	e.buf = e.buf[:0]
	_, err := e.w.Write(out)
	return err
}

func (e *encrypter) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true
	return e.flush(true)
}

type decrypter struct {
	r      *bufio.Reader
	aead   cipher.AEAD
	header []byte
	nonce  [12]byte
	n      uint32
	in     []byte
	plain  []byte // backing store of out
	out    []byte
	done   bool
	err    error
}

// NewDecrypter reads the header from r and returns a reader of the
// plaintext. A wrong passphrase fails here, on the first chunk.
func NewDecrypter(r io.Reader, passphrase string) (io.Reader, error) {
	h := make([]byte, headerSize)
	if _, err := io.ReadFull(r, h); err != nil || !bytes.Equal(h[:8], []byte(cryptMagic)) {
		return nil, ErrNotEncrypted
	}
	if h[8] != 1 || h[9] == 0 || h[9] > maxLogN || h[10] == 0 || h[11] == 0 || h[11] > 16 {
		return nil, fmt.Errorf("unsupported encryption parameters")
	}
	if binary.BigEndian.Uint32(h[35:39]) != chunkSize {
		return nil, fmt.Errorf("unsupported encryption chunk size")
	}
	aead, err := deriveAEAD(passphrase, h)
	if err != nil {
		return nil, err
	}
	d := &decrypter{r: bufio.NewReaderSize(r, chunkSize+64), aead: aead, header: h, in: make([]byte, chunkSize+aead.Overhead()), plain: make([]byte, 0, chunkSize)}
	copy(d.nonce[:noncePrefLen], h[28:35])
	if err := d.next(); err != nil {
		if errors.Is(err, errAuth) {
			return nil, ErrWrongPassphrase
		}
		return nil, err
	}
	return d, nil
}

var errAuth = errors.New("authentication failed")

func (d *decrypter) next() error {
	n, err := io.ReadFull(d.r, d.in)
	last := false
	switch {
	case err == io.EOF, n == 0 && err == nil:
		return errors.New("the encrypted archive is truncated")
	case err == io.ErrUnexpectedEOF:
		last = true
	case err != nil:
		return err
	default:
		if _, perr := d.r.Peek(1); perr == io.EOF {
			last = true
		}
	}
	if n < d.aead.Overhead() {
		return errors.New("the encrypted archive is truncated")
	}
	binary.BigEndian.PutUint32(d.nonce[noncePrefLen:11], d.n)
	d.nonce[11] = 0
	if last {
		d.nonce[11] = 1
	}
	out, oerr := d.aead.Open(d.plain[:0], d.nonce[:], d.in[:n], d.header)
	if oerr != nil {
		if last {
			// Cut exactly at a chunk boundary, a middle chunk looks like
			// the last one: it opens as a middle chunk.
			d.nonce[11] = 0
			if _, err := d.aead.Open(d.plain[:0], d.nonce[:], d.in[:n], d.header); err == nil {
				return errors.New("the encrypted archive is truncated")
			}
		}
		return errAuth
	}
	d.out = out
	d.n++
	d.done = last
	return nil
}

func (d *decrypter) Read(p []byte) (int, error) {
	for len(d.out) == 0 {
		if d.err != nil {
			return 0, d.err
		}
		if d.done {
			return 0, io.EOF
		}
		if err := d.next(); err != nil {
			if errors.Is(err, errAuth) {
				err = errors.New("the encrypted archive is damaged")
			}
			d.err = err
			return 0, err
		}
	}
	n := copy(p, d.out)
	d.out = d.out[n:]
	return n, nil
}

func deriveAEAD(passphrase string, h []byte) (cipher.AEAD, error) {
	key, err := scrypt.Key([]byte(passphrase), h[12:12+saltSize], 1<<h[9], int(h[10]), int(h[11]), 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
