package backup

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// sftpServer is an in-process SSH server with the sftp subsystem, serving
// dir, accepting password "pw" for user "backup" or the client key.
type sftpServer struct {
	addr        string
	fingerprint string
	clientKey   string // PEM of a private key the server accepts
}

func startSFTP(t *testing.T, dir string) *sftpServer {
	t.Helper()
	_, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	clientPub, clientPriv, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKey(clientPriv, "")
	if err != nil {
		t.Fatal(err)
	}
	sshClientPub, _ := ssh.NewPublicKey(clientPub)
	conf := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if c.User() == "backup" && string(pw) == "pw" {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if c.User() == "backup" && string(k.Marshal()) == string(sshClientPub.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
	}
	conf.AddHostKey(hostSigner)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go serveSSH(c, conf, dir)
		}
	}()
	return &sftpServer{addr: l.Addr().String(), fingerprint: ssh.FingerprintSHA256(hostSigner.PublicKey()), clientKey: string(pem.EncodeToMemory(block))}
}

func serveSSH(c net.Conn, conf *ssh.ServerConfig, dir string) {
	_, chans, reqs, err := ssh.NewServerConn(c, conf)
	if err != nil {
		c.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(ssh.UnknownChannelType, "no")
			continue
		}
		ch, in, err := nc.Accept()
		if err != nil {
			continue
		}
		go func() {
			for req := range in {
				ok := req.Type == "subsystem" && string(req.Payload[4:]) == "sftp"
				req.Reply(ok, nil)
				if ok {
					srv, err := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(dir))
					if err == nil {
						srv.Serve()
					}
					ch.Close()
				}
			}
		}()
	}
}

func (s *sftpServer) dest(dir string) model.SFTPDest {
	host, port, _ := net.SplitHostPort(s.addr)
	p, _ := strconv.Atoi(port)
	return model.SFTPDest{Host: host, Port: p, Username: "backup", Password: "pw", Directory: dir, HostKey: s.fingerprint}
}

func TestSFTPTarget(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	srv := startSFTP(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Relative to the server's working directory (root): SFTP paths are
	// POSIX paths, and the test server on Windows would read "C:/..." as a
	// relative name.
	cfg := srv.dest("backups/web")
	tg, err := dialSFTP(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	exercise(t, tg)
	tg.Close()
	if _, err := os.Stat(filepath.Join(root, "backups", "web", "notes with space & $.txt")); err != nil {
		t.Errorf("uploaded file missing: %v", err)
	}

	// Key authentication.
	cfg.Password, cfg.PrivateKey = "", srv.clientKey
	tg, err = dialSFTP(ctx, cfg)
	if err != nil {
		t.Fatalf("key auth: %v", err)
	}
	tg.Close()

	cfg.Password, cfg.PrivateKey = "wrong", ""
	if _, err := dialSFTP(ctx, cfg); err == nil || errors.As(err, new(*HostKeyError)) {
		t.Errorf("wrong password: %v", err)
	}
}

func TestSFTPHostKeyVerification(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	srv := startSFTP(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// No key configured yet: refused, and the server's key is reported
	// for the administrator to confirm; nothing is written.
	cfg := srv.dest(filepath.ToSlash(root))
	cfg.HostKey = ""
	_, err := dialSFTP(ctx, cfg)
	var hk *HostKeyError
	if !errors.As(err, &hk) || hk.Presented != srv.fingerprint || hk.Expected != "" {
		t.Fatalf("no host key: err = %v", err)
	}

	// Another server's key: refused, loudly.
	cfg.HostKey = "SHA256:" + strings.Repeat("A", 43)
	_, err = dialSFTP(ctx, cfg)
	if !errors.As(err, &hk) || hk.Presented != srv.fingerprint || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("wrong host key: err = %v", err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("files written despite the host key mismatch: %v", entries)
	}

	cfg.HostKey = srv.fingerprint
	tg, err := dialSFTP(ctx, cfg)
	if err != nil {
		t.Fatalf("right host key: %v", err)
	}
	tg.Close()
}

func TestFolderTarget(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "not", "yet")
	exercise(t, &folderTarget{dir: dir})
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".partial") {
			t.Errorf("left a partial file: %s", e.Name())
		}
	}
}

func TestFolderTargetExplainsAccessDenied(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows ignores a directory's mode bits; its ACLs decide access")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	os.Mkdir(locked, 0o500)
	defer os.Chmod(locked, 0o700)
	err := (&folderTarget{dir: locked}).Put(context.Background(), "a.zip", tempFile(t, "x"))
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Errorf("err = %v", err)
	}
}
