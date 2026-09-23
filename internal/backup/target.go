package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/parthh37/nodehoster/internal/model"
)

// Target is a backup destination. Names are plain file names (no
// directories): each target keeps its archives in one folder or prefix.
type Target interface {
	// Put uploads the local file at path as name, replacing it.
	Put(ctx context.Context, name, path string) error
	// List returns the files directly in the folder or under the prefix.
	List(ctx context.Context) ([]Object, error)
	Open(ctx context.Context, name string) (io.ReadCloser, error)
	Delete(ctx context.Context, name string) error
	Close() error
}

// New connects to a destination. Secrets in d must be plain text.
func New(ctx context.Context, d model.BackupDestination) (Target, error) {
	switch d.Type {
	case model.BackupFolder:
		if d.Folder != nil {
			return &folderTarget{dir: d.Folder.Path}, nil
		}
	case model.BackupS3:
		if d.S3 != nil {
			return newS3(*d.S3, httpClient), nil
		}
	case model.BackupAzure:
		if d.Azure != nil {
			return newAzure(*d.Azure, httpClient), nil
		}
	case model.BackupSFTP:
		if d.SFTP != nil {
			return dialSFTP(ctx, *d.SFTP)
		}
	}
	return nil, fmt.Errorf("destination %q is not configured", d.Name)
}

// httpClient has no overall timeout (uploads can be large); requests are
// bounded by their context, and a stalled connection by the transport.
var httpClient = &http.Client{Transport: &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	TLSHandshakeTimeout:   15 * time.Second,
	ResponseHeaderTimeout: 2 * time.Minute,
	IdleConnTimeout:       60 * time.Second,
	MaxIdleConnsPerHost:   2,
}}

// Probe checks that a destination is reachable and writable: it lists the
// folder, then writes and deletes a small file.
func Probe(ctx context.Context, t Target, tmpDir string) error {
	if _, err := t.List(ctx); err != nil {
		return fmt.Errorf("list: %w", err)
	}
	var rnd [6]byte
	rand.Read(rnd[:])
	name := "nodehoster-write-test-" + hex.EncodeToString(rnd[:]) + ".txt"
	f, err := os.CreateTemp(tmpDir, "probe-*")
	if err != nil {
		return err
	}
	f.WriteString("NodeHoster backup destination test. This file is deleted right away.\r\n")
	f.Close()
	defer os.Remove(f.Name())
	if err := t.Put(ctx, name, f.Name()); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if err := t.Delete(ctx, name); err != nil {
		return fmt.Errorf("delete the test file %s: %w", name, err)
	}
	return nil
}

// checkObjectName refuses names that would leave the folder or prefix.
func checkObjectName(name string) error {
	if name == "" || strings.ContainsAny(name, "/\\\x00:") || name == "." || name == ".." {
		return errors.New("invalid file name")
	}
	return nil
}

func fileSize(path string) (int64, error) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

// sizeString formats a byte count for error messages.
func sizeString(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
