package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"strconv"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// HostKeyError is returned when an SFTP server's host key is not the one
// configured (or none is configured yet). Presented is the key's
// fingerprint, for the administrator to compare with the server's own
// (ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub) before saving it: a
// key is never trusted just because it was the first one seen.
type HostKeyError struct {
	Presented, Expected string
}

func (e *HostKeyError) Error() string {
	if e.Expected == "" {
		return "the server's host key is " + e.Presented + "; check it against the server and save it as the destination's host key"
	}
	return fmt.Sprintf("the server's host key %s does not match the configured %s: the server was reinstalled, or the connection is being intercepted", e.Presented, e.Expected)
}

type sftpTarget struct {
	ssh  *ssh.Client
	c    *sftp.Client
	dir  string
	done chan struct{}
}

// HostKeyFingerprint is the SHA256:… fingerprint of an SSH public key.
func HostKeyFingerprint(k ssh.PublicKey) string { return ssh.FingerprintSHA256(k) }

func dialSFTP(ctx context.Context, cfg model.SFTPDest) (*sftpTarget, error) {
	var auth []ssh.AuthMethod
	if cfg.PrivateKey != "" {
		var signer ssh.Signer
		var err error
		if cfg.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(cfg.PrivateKey), []byte(cfg.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(cfg.PrivateKey))
		}
		if err != nil {
			var missing *ssh.PassphraseMissingError
			if errors.As(err, &missing) {
				return nil, errors.New("the private key is protected by a passphrase: enter it")
			}
			return nil, fmt.Errorf("the private key cannot be read: %w", err)
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}
	if cfg.Password != "" {
		pw := cfg.Password
		auth = append(auth, ssh.Password(pw), ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
			answers := make([]string, len(qs))
			for i := range qs {
				answers[i] = pw
			}
			return answers, nil
		}))
	}
	var hkErr *HostKeyError
	conf := &ssh.ClientConfig{
		User: cfg.Username,
		Auth: auth,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			fp := ssh.FingerprintSHA256(key)
			if cfg.HostKey == "" || fp != cfg.HostKey {
				hkErr = &HostKeyError{Presented: fp, Expected: cfg.HostKey}
				return hkErr
			}
			return nil
		},
		Timeout: 30 * time.Second,
	}
	port := cfg.Port
	if port == 0 {
		port = 22
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(port))
	d := net.Dialer{Timeout: 30 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", addr, err)
	}
	// The handshake has no context of its own: a deadline bounds it.
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	} else {
		conn.SetDeadline(time.Now().Add(time.Minute))
	}
	sc, chans, reqs, err := ssh.NewClientConn(conn, addr, conf)
	if err != nil {
		conn.Close()
		if hkErr != nil {
			return nil, hkErr
		}
		return nil, fmt.Errorf("SSH to %s: %w", addr, err)
	}
	conn.SetDeadline(time.Time{})
	client := ssh.NewClient(sc, chans, reqs)
	c, err := sftp.NewClient(client)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("%s does not offer SFTP: %w", addr, err)
	}
	// Close the connection when the context ends, so a stalled transfer
	// does not hang a backup forever.
	t := &sftpTarget{ssh: client, c: c, dir: cfg.Directory, done: make(chan struct{})}
	if t.dir == "" {
		t.dir = "."
	}
	go func() {
		select {
		case <-ctx.Done():
			client.Close()
		case <-t.done:
		}
	}()
	return t, nil
}

func (t *sftpTarget) join(name string) string { return path.Join(t.dir, name) }

func (t *sftpTarget) Put(ctx context.Context, name, p string) error {
	if err := checkObjectName(name); err != nil {
		return err
	}
	src, err := os.Open(p)
	if err != nil {
		return err
	}
	defer src.Close()
	if err := t.c.MkdirAll(t.dir); err != nil {
		if _, serr := t.c.Stat(t.dir); serr != nil {
			return fmt.Errorf("create %s: %w", t.dir, err)
		}
	}
	final := t.join(name)
	tmp := final + ".partial"
	dst, err := t.c.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	_, err = dst.ReadFrom(readerCtx{ctx, src})
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		if err = t.c.PosixRename(tmp, final); err != nil {
			// Servers without the posix-rename extension refuse to
			// rename over an existing file.
			t.c.Remove(final)
			err = t.c.Rename(tmp, final)
		}
	}
	if err != nil {
		t.c.Remove(tmp)
		return fmt.Errorf("write %s: %w", final, err)
	}
	return nil
}

func (t *sftpTarget) List(ctx context.Context) ([]Object, error) {
	entries, err := t.c.ReadDir(t.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return []Object{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", t.dir, err)
	}
	out := []Object{}
	for _, e := range entries {
		if e.Mode().IsRegular() {
			out = append(out, Object{Name: e.Name(), Size: e.Size(), Modified: e.ModTime()})
		}
	}
	return out, nil
}

func (t *sftpTarget) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	if err := checkObjectName(name); err != nil {
		return nil, err
	}
	return t.c.Open(t.join(name))
}

func (t *sftpTarget) Delete(ctx context.Context, name string) error {
	if err := checkObjectName(name); err != nil {
		return err
	}
	if err := t.c.Remove(t.join(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (t *sftpTarget) Close() error {
	close(t.done)
	t.c.Close()
	return t.ssh.Close()
}
